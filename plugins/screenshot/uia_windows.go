//go:build windows

package main

import (
	"image"
	"log"
	"runtime"
	"time"
)

// UI Automation (UIAutomationCore.dll type library). The numbers used with
// comCall below are vtable slots taken from that library:
//
//	IUIAutomation          10 ElementFromHandleBuildCache, 20 CreateCacheRequest
//	IUIAutomation2         61 put_ConnectionTimeout, 63 put_TransactionTimeout
//	IUIAutomationCacheRequest 3 AddProperty, 7 put_TreeScope, 11 put_AutomationElementMode
//	IUIAutomationElement   19 GetCachedChildren, 70 get_CachedIsOffscreen, 75 get_CachedBoundingRectangle
//	IUIAutomationElementArray 3 get_Length, 4 GetElement
var (
	clsidUIAutomation8 = mustGUID("{E22AD333-B25F-460C-83D0-0581107395C9}")
	clsidUIAutomation  = mustGUID("{FF48DBA4-60EF-4201-AA87-54103EEF594E}")
	iidUIAutomation    = mustGUID("{30CBE57D-D9D0-452A-AB13-7AC5AC4825EE}")
	iidUIAutomation2   = mustGUID("{34723AFF-0C9D-49D0-9896-7AB52DF8CD8A}")
)

const (
	uiaBoundingRectangle = 30001
	uiaIsOffscreen       = 30022
	uiaSubtree           = 7 // TreeScope_Element | Children | Descendants

	snapElementMin    = 10   // physical pixels; smaller elements are lifted away
	snapElementBudget = 4000 // elements kept per window
	snapElementDepth  = 64
	snapWindowLimit   = 24
	// One application must not hold up the others: a window whose tree cannot
	// be read in this time keeps window-level snapping.
	uiaConnectMillis     = 500
	uiaTransactionMillis = 2000
	snapRefineTime       = 6 * time.Second
)

// uiaReader reads element rectangles. It belongs to one OS thread, which
// must not be the GUI thread: providers may call back into our own windows.
type uiaReader struct{ automation, request uintptr }

func newUIAReader() (*uiaReader, error) {
	u := &uiaReader{}
	hr := int32(call(ole32, "CoCreateInstance", ptr(&clsidUIAutomation8), 0, 1, ptr(&iidUIAutomation), ptr(&u.automation)))
	if hr < 0 || u.automation == 0 {
		hr = int32(call(ole32, "CoCreateInstance", ptr(&clsidUIAutomation), 0, 1, ptr(&iidUIAutomation), ptr(&u.automation)))
	}
	if hr < 0 || u.automation == 0 {
		return nil, hresult("无法使用界面自动化", hr)
	}
	var newer uintptr
	if comCall(u.automation, 0, ptr(&iidUIAutomation2), ptr(&newer)) >= 0 && newer != 0 {
		comCall(newer, 61, uint32(uiaConnectMillis))
		comCall(newer, 63, uint32(uiaTransactionMillis))
		comRelease(newer)
	}
	// One cached request returns a whole subtree in a single cross-process
	// call; without it every rectangle would be a round trip of its own.
	hr = comCall(u.automation, 20, ptr(&u.request))
	if hr >= 0 {
		hr = comCall(u.request, 3, uiaBoundingRectangle)
	}
	if hr >= 0 {
		hr = comCall(u.request, 3, uiaIsOffscreen)
	}
	if hr >= 0 {
		hr = comCall(u.request, 7, uiaSubtree)
	}
	if hr >= 0 {
		hr = comCall(u.request, 11, 0) // AutomationElementMode_None: cached values only
	}
	if hr < 0 {
		u.close()
		return nil, hresult("无法使用界面自动化", hr)
	}
	return u, nil
}

func (u *uiaReader) close() {
	comRelease(u.request)
	comRelease(u.automation)
	u.request, u.automation = 0, 0
}

// elements returns the visible parts of a window, clipped to clip. Nil means
// the window exposes nothing useful or did not answer in time.
func (u *uiaReader) elements(hwnd uintptr, clip image.Rectangle) []snapTarget {
	var root uintptr
	if comCall(u.automation, 10, hwnd, u.request, ptr(&root)) < 0 || root == 0 {
		return nil
	}
	defer comRelease(root)
	budget := snapElementBudget
	return u.children(root, clip, 1, &budget)
}

func (u *uiaReader) children(element uintptr, clip image.Rectangle, depth int, budget *int) []snapTarget {
	var array uintptr
	if depth > snapElementDepth || comCall(element, 19, ptr(&array)) < 0 || array == 0 {
		return nil
	}
	defer comRelease(array)
	var count int32
	comCall(array, 3, ptr(&count))
	var out []snapTarget
	for i := 0; i < int(count) && *budget > 0; i++ {
		var child uintptr
		if comCall(array, 4, i, ptr(&child)) < 0 || child == 0 {
			continue
		}
		*budget--
		var offscreen int32
		var r rect
		comCall(child, 70, ptr(&offscreen))
		known := comCall(child, 75, ptr(&r)) >= 0
		bounds := r.image().Intersect(clip)
		switch {
		case offscreen != 0:
			// Scrolled out of view, together with everything inside it.
		case !known || bounds.Dx() < snapElementMin || bounds.Dy() < snapElementMin:
			// Groups without a usable rectangle still lead to real controls.
			out = append(out, u.children(child, clip, depth+1, budget)...)
		default:
			out = append(out, snapTarget{bounds: bounds, loose: true, children: u.children(child, bounds, depth+1, budget)})
		}
		comRelease(child)
	}
	return out
}

// refineSnaps replaces window-level snapping with interface elements, one
// window at a time while the selection is open. The selection already covers
// the desktop, so elements cannot be looked up under the pointer later.
func (d *desktop) refineSnaps(s *selectionWindow, windows []snapTarget, pointer image.Point) {
	defer d.active.Add(-1)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr := int32(call(ole32, "CoInitializeEx", 0, 0)); hr >= 0 {
		defer call(ole32, "CoUninitialize")
	}
	reader, err := newUIAReader()
	if err != nil {
		log.Print(err)
		return
	}
	defer reader.close()
	own := uint32(call(kernel32, "GetCurrentProcessId"))
	deadline := time.Now().Add(snapRefineTime)
	for _, i := range snapOrder(windows, pointer, snapWindowLimit) {
		if s.closed.Load() || time.Now().After(deadline) {
			return
		}
		window := windows[i]
		var process uint32
		call(user32, "GetWindowThreadProcessId", window.hwnd, ptr(&process))
		if process == own || call(user32, "IsWindow", window.hwnd) == 0 {
			continue // our pins have no parts; never wait on our own GUI thread
		}
		parts := reader.elements(window.hwnd, window.bounds)
		if len(parts) == 0 {
			continue
		}
		_ = d.post(func() {
			if d.selection == s && i < len(s.snaps) && s.snaps[i].hwnd == window.hwnd {
				s.snaps[i].children, s.snaps[i].loose = parts, true
				invalidate(s.hwnd)
			}
		})
	}
}
