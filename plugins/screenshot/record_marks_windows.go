//go:build windows

package main

import (
	"errors"
	"image"
	"strconv"

	"golang.org/x/sys/windows"
)

// Marks drawn while recording live in a layered window over the region. It
// is deliberately not excluded from capture: what is drawn is recorded.
// Without a tool the window is click-through; with one it takes the mouse
// but never the keyboard focus of the recorded application.

var recordMarkTools = []recordItem{
	{id: "rect", icon: "rect", hint: "矩形标注 · 再点一次或右键退出", width: 32},
	{id: "pen", icon: "pen", hint: "画笔 · 再点一次或右键退出", width: 32},
	{id: "arrow", icon: "arrow", hint: "箭头 · 再点一次或右键退出", width: 32},
	{id: "number", icon: "number", hint: "序号 · 点击画面依次标 1、2、3", width: 32},
}

// A color no theme uses: pixels still holding it after drawing are empty.
const markKey = 0x010203

func isMarkTool(id string) bool {
	for _, tool := range recordMarkTools {
		if tool.id == id {
			return true
		}
	}
	return false
}

func (w *recordWindow) selectTool(id string) {
	if w.tool == id {
		id = ""
	}
	w.tool, w.draft = id, nil
	w.syncMarks()
}

func (w *recordWindow) undoMark() {
	if len(w.marks) > 0 {
		w.marks = w.marks[:len(w.marks)-1]
	}
	w.syncMarks()
}

func (w *recordWindow) closeMarks() {
	if w.overlay != 0 {
		call(user32, "DestroyWindow", w.overlay)
		w.overlay = 0
	}
	w.markSurface.close()
	w.markSurface, w.tool, w.draft = nil, "", nil
}

// syncMarks shows, hides and repaints the overlay after any change.
func (w *recordWindow) syncMarks() {
	invalidate(w.hwnd)
	if w.tool == "" && len(w.marks) == 0 && w.draft == nil {
		if w.overlay != 0 {
			call(user32, "ShowWindow", w.overlay, 0)
		}
		return
	}
	if w.overlay == 0 {
		w.overlay = w.d.createWindow("FLScreenshotMarks", "录制标注", wsExTopmost|wsExToolwindow|wsExLayered|wsExNoactivate|wsExTransparent, w.d.controller, w.region)
		if w.overlay != 0 {
			var err error
			if w.markSurface, err = newDIBSurface(0, w.region.Size()); err != nil {
				call(user32, "DestroyWindow", w.overlay)
				w.overlay = 0
			}
		}
		if w.overlay == 0 {
			w.tool = ""
			w.d.report(w.hwnd, errors.New("无法创建录制标注层"))
			return
		}
	}
	style := call(user32, "GetWindowLongPtrW", w.overlay, coord(-20)) | wsExTransparent
	if w.tool != "" {
		style &^= wsExTransparent
	}
	call(user32, "SetWindowLongPtrW", w.overlay, coord(-20), style)
	w.renderMarks()
	windowAt(w.overlay, w.region, false)
	windowAt(w.hwnd, w.bounds, false) // the bar stays above the marks
}

// renderMarks presents the marks with per-pixel alpha. While a tool is
// active, empty pixels keep the faintest alpha so the window gets the mouse.
func (w *recordWindow) renderMarks() {
	s := w.markSurface
	if w.overlay == 0 || s == nil {
		return
	}
	bounds := image.Rectangle{Max: s.size}
	fill(s.dc, bounds, rgbRef(markKey))
	canvas := &image.RGBA{Rect: bounds}
	for _, m := range w.marks {
		m.draw(s.dc, canvas, image.Point{})
	}
	if w.draft != nil {
		w.draft.draw(s.dc, canvas, image.Point{})
	}
	call(gdi32, "GdiFlush")
	n := s.size.X * s.size.Y * 4
	if len(w.markPixels) != n {
		w.markPixels = make([]byte, n)
	}
	pix := w.markPixels
	if windows.ReadProcessMemory(windows.CurrentProcess(), s.address, &pix[0], uintptr(n), nil) != nil {
		return
	}
	empty := byte(0)
	if w.tool != "" {
		empty = 1
	}
	for i := 0; i < n; i += 4 {
		if pix[i] == markKey&255 && pix[i+1] == markKey>>8&255 && pix[i+2] == markKey>>16 {
			pix[i], pix[i+1], pix[i+2], pix[i+3] = 0, 0, 0, empty
		} else {
			pix[i+3] = 255
		}
	}
	if windows.WriteProcessMemory(windows.CurrentProcess(), s.address, &pix[0], uintptr(n), nil) != nil {
		return
	}
	screen := call(user32, "GetDC", 0)
	defer call(user32, "ReleaseDC", 0, screen)
	dest, size, src := point{int32(w.region.Min.X), int32(w.region.Min.Y)}, point{int32(s.size.X), int32(s.size.Y)}, point{}
	blend := [4]byte{0, 0, 255, 1}
	call(user32, "UpdateLayeredWindow", w.overlay, screen, ptr(&dest), ptr(&size), s.dc, ptr(&src), 0, ptr(&blend), 2)
}

func (w *recordWindow) markPoint(lparam uintptr) image.Point {
	p, size := mousePoint(lparam), w.region.Size()
	return image.Pt(max(0, min(p.X, size.X-1)), max(0, min(p.Y, size.Y-1)))
}

func (w *recordWindow) handleMarks(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x0014:
		return 1
	case wmPaint:
		var ps paintStruct
		call(user32, "BeginPaint", w.overlay, ptr(&ps))
		call(user32, "EndPaint", w.overlay, ptr(&ps))
		return 0
	case 0x0021:
		return 3 // MA_NOACTIVATE: the recorded application keeps the keyboard
	case 0x0020:
		if w.tool != "" {
			call(user32, "SetCursor", call(user32, "LoadCursorW", 0, 32515)) // IDC_CROSS
			return 1
		}
	case wmLButtonDown:
		if w.tool == "" || w.state == recordSaving {
			return 0
		}
		p := w.markPoint(lparam)
		ink := annotation{kind: w.tool, color: w.d.theme.Danger, width: 3}
		if w.tool == "number" {
			count := 1
			for _, m := range w.marks {
				if m.kind == "number" {
					count++
				}
			}
			ink.points, ink.text = []image.Point{p}, strconv.Itoa(count)
			w.marks = append(w.marks, ink)
			w.syncMarks()
			return 0
		}
		ink.points = []image.Point{p, p}
		w.draft = &ink
		call(user32, "SetCapture", w.overlay)
		return 0
	case wmMouseMove:
		if w.draft != nil {
			p := w.markPoint(lparam)
			if w.draft.kind != "pen" {
				w.draft.points[1] = p
			} else if p != w.draft.points[len(w.draft.points)-1] {
				w.draft.points = append(w.draft.points, p)
			}
			w.renderMarks()
		}
		return 0
	case wmLButtonUp:
		if w.draft != nil {
			m := *w.draft
			w.draft = nil
			call(user32, "ReleaseCapture")
			if m.points[0] != m.points[len(m.points)-1] {
				w.marks = append(w.marks, m)
			}
			w.syncMarks()
		}
		return 0
	case wmRButtonUp:
		w.selectTool(w.tool)
		return 0
	case 0x0215: // WM_CAPTURECHANGED
		if w.draft != nil {
			w.draft = nil
			w.renderMarks()
		}
		return 0
	}
	return call(user32, "DefWindowProcW", w.overlay, msg, wparam, lparam)
}
