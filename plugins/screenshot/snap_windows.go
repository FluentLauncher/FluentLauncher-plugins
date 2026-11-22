//go:build windows

package main

import (
	"image"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	snapMinSize = 24   // physical pixels; smaller children are decoration, not capture targets
	snapSlop    = 4    // a press that moves less than this is a click on the highlighted window
	snapBudget  = 4000 // windows visited per capture, a guard against pathological trees
	snapDepth   = 8
)

func windowClass(hwnd uintptr) string {
	var name [64]uint16
	n := call(user32, "GetClassNameW", hwnd, ptr(&name[0]), len(name))
	return windows.UTF16ToString(name[:n])
}

// snapTargets lists what is visible on the desktop right now, front to back.
// It only reads window geometry and may run off the GUI thread. Walking with
// GetWindow needs no callback; the budget bounds a Z-order that changes midway.
func snapTargets() []snapTarget {
	budget := snapBudget
	var out []snapTarget
	for hwnd := call(user32, "GetTopWindow", 0); hwnd != 0 && budget > 0; hwnd = call(user32, "GetWindow", hwnd, 2) {
		budget--
		bounds, ok := snapTopLevel(hwnd)
		if !ok {
			continue
		}
		out = append(out, snapTarget{bounds, snapChildren(hwnd, bounds, 1, &budget)})
	}
	return out
}

func snapTopLevel(hwnd uintptr) (image.Rectangle, bool) {
	if call(user32, "IsWindowVisible", hwnd) == 0 || call(user32, "IsIconic", hwnd) != 0 {
		return image.Rectangle{}, false
	}
	// Windows on other virtual desktops and suspended store apps stay "visible".
	var cloaked uint32
	if call(dwmapi, "DwmGetWindowAttribute", hwnd, 14, ptr(&cloaked), 4) == 0 && cloaked != 0 {
		return image.Rectangle{}, false
	}
	style := call(user32, "GetWindowLongPtrW", hwnd, coord(-20))
	if style&wsExTransparent != 0 {
		return image.Rectangle{}, false // click-through overlays cover other windows without being one
	}
	if style&wsExLayered != 0 {
		var key, flags uint32
		var alpha byte
		if call(user32, "GetLayeredWindowAttributes", hwnd, ptr(&key), ptr(&alpha), ptr(&flags)) != 0 && flags&2 != 0 && alpha == 0 {
			return image.Rectangle{}, false
		}
	}
	switch class := windowClass(hwnd); {
	case class == "Progman", class == "WorkerW":
		return image.Rectangle{}, false // the desktop itself snaps to a monitor instead
	case strings.HasPrefix(class, "FLScreenshot") && class != "FLScreenshotPin":
		return image.Rectangle{}, false // our own chrome; pins are part of the picture
	}
	// The extended frame excludes the invisible resize border around a window.
	var r rect
	if call(dwmapi, "DwmGetWindowAttribute", hwnd, 9, ptr(&r), 16) != 0 && call(user32, "GetWindowRect", hwnd, ptr(&r)) == 0 {
		return image.Rectangle{}, false
	}
	bounds := r.image()
	if call(user32, "IsZoomed", hwnd) != 0 {
		// A maximized frame hangs a few pixels over the neighbouring monitors.
		bounds = bounds.Intersect(currentMonitor(bounds.Min.Add(bounds.Size().Div(2))))
	}
	return bounds, !bounds.Empty()
}

func snapChildren(parent uintptr, clip image.Rectangle, depth int, budget *int) []snapTarget {
	if depth > snapDepth {
		return nil
	}
	var out []snapTarget
	for hwnd := call(user32, "GetWindow", parent, 5); hwnd != 0 && *budget > 0; hwnd = call(user32, "GetWindow", hwnd, 2) {
		*budget--
		if call(user32, "IsWindowVisible", hwnd) == 0 || call(user32, "GetWindowLongPtrW", hwnd, coord(-20))&wsExTransparent != 0 {
			continue
		}
		var r rect
		if call(user32, "GetWindowRect", hwnd, ptr(&r)) == 0 {
			continue
		}
		bounds := r.image().Intersect(clip)
		if bounds.Dx() < snapMinSize || bounds.Dy() < snapMinSize {
			continue
		}
		out = append(out, snapTarget{bounds, snapChildren(hwnd, bounds, depth+1, budget)})
	}
	return out
}

// snapCandidate is the window a plain click selects: the one under the pointer
// while nothing is selected, or under the press until it turns into a drag.
// Coordinates are local to the frozen image, like the selection itself.
func (s *selectionWindow) snapCandidate() image.Rectangle {
	if len(s.snaps) == 0 || !s.d.preferences.SnapWindows || s.busy || s.snapOff || s.dragMode > 1 || !s.selected.Empty() {
		return image.Rectangle{}
	}
	p := s.cursor
	if s.dragMode == 1 {
		p = s.anchor
	}
	p = p.Add(s.desktopBounds.Min)
	r := snapAt(s.snaps, p)
	if r.Empty() {
		r = currentMonitor(p) // bare desktop
	}
	return r.Sub(s.desktopBounds.Min).Intersect(s.image.Bounds())
}
