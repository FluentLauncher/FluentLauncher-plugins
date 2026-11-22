//go:build windows

package main

import (
	"errors"
	"fmt"
	"image"
)

const delayTimer = 1

// delaySeconds maps the delayed capture commands to their countdown.
var delaySeconds = map[string]int{"delay-3": 3, "delay-5": 5}

// delayWindow counts down before a region capture, so that menus, tooltips
// and hover states can be opened first. It never takes focus: activating it
// would close the very thing the user is waiting to capture.
type delayWindow struct {
	d    *desktop
	hwnd uintptr
	left int
}

// toggleDelay starts the countdown; running the command again cancels it.
func (d *desktop) toggleDelay(seconds int) {
	if d.delaying != nil {
		d.delaying.cancel()
		return
	}
	if !d.capturing.CompareAndSwap(false, true) {
		d.report(d.controller, errors.New("已有截图正在进行，请先完成或取消"))
		return
	}
	d.closeMenu()
	monitor := currentMonitor(cursorPoint())
	w := &delayWindow{d: d, left: seconds}
	// Created on the target monitor first: its DPI decides the size.
	w.hwnd = d.createWindow("FLScreenshotDelay", "延时截图", wsExTopmost|wsExToolwindow|wsExNoactivate|wsExLayered, d.controller, image.Rectangle{Min: monitor.Min, Max: monitor.Min.Add(image.Pt(1, 1))})
	if w.hwnd == 0 {
		d.capturing.Store(false)
		d.report(d.controller, errors.New("无法创建延时截图倒计时"))
		return
	}
	scale := uiScale(w.hwnd)
	bounds := delayBounds(monitor, image.Pt(px(220, scale), px(36, scale)), px(24, scale))
	radius := px(int(max(0, d.theme.Radius)), scale)
	rgn := call(gdi32, "CreateRoundRectRgn", 0, 0, bounds.Dx()+1, bounds.Dy()+1, radius*2, radius*2)
	if call(user32, "SetWindowRgn", w.hwnd, rgn, 0) == 0 {
		call(gdi32, "DeleteObject", rgn)
	}
	call(user32, "SetLayeredWindowAttributes", w.hwnd, 0, 255, 2)
	d.delaying = w
	windowAt(w.hwnd, bounds, false)
	call(user32, "SetTimer", w.hwnd, delayTimer, 1000, 0)
}

// delayBounds puts the countdown at the top centre of the monitor.
func delayBounds(monitor image.Rectangle, size image.Point, gap int) image.Rectangle {
	size.X = min(size.X, monitor.Dx())
	pos := image.Pt(monitor.Min.X+(monitor.Dx()-size.X)/2, monitor.Min.Y+gap)
	return image.Rectangle{Min: pos, Max: pos.Add(size)}
}

func (w *delayWindow) remove() {
	call(user32, "KillTimer", w.hwnd, delayTimer)
	call(user32, "DestroyWindow", w.hwnd)
	w.d.delaying = nil
}

func (w *delayWindow) cancel() {
	if w.d.delaying != w {
		return
	}
	w.remove()
	w.d.capturing.Store(false)
}

// tick is one second of the countdown. At zero the countdown disappears and
// the ordinary region capture takes over, still holding the capturing flag.
func (w *delayWindow) tick() {
	if w.d.delaying != w {
		return
	}
	if w.left--; w.left > 0 {
		invalidate(w.hwnd)
		return
	}
	w.remove()
	if w.d.captureAfterDelay != nil {
		w.d.captureAfterDelay()
		return
	}
	w.d.startCapture("region")
}

func (w *delayWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmPaint:
		var ps paintStruct
		dc := call(user32, "BeginPaint", w.hwnd, ptr(&ps))
		var client rect
		call(user32, "GetClientRect", w.hwnd, ptr(&client))
		t := w.d.theme
		fill(dc, client.image(), rgbRef(t.Inverse))
		text(dc, fmt.Sprintf("%d 秒后截图 · 点击取消", w.left), client.image(), rgbRef(t.InverseText))
		call(user32, "EndPaint", w.hwnd, ptr(&ps))
		return 0
	case 0x0014:
		return 1
	case 0x0021:
		return 3 // MA_NOACTIVATE
	case wmTimer:
		if wparam == delayTimer {
			w.tick()
		}
		return 0
	case wmLButtonUp:
		w.cancel()
		return 0
	}
	return call(user32, "DefWindowProcW", w.hwnd, msg, wparam, lparam)
}
