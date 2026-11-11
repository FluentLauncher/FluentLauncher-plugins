//go:build windows

package main

import (
	"errors"
	"image"
	"image/color"
	"math"
	"slices"
)

type pinWindow struct {
	d                    *desktop
	hwnd, shadow         uintptr
	state                pinState
	image                *image.RGBA
	bitmap, shadowBitmap *bitmap
	dragging             bool
	dragCursor           image.Point
	dragBounds           image.Rectangle
	busy                 bool
}

func (d *desktop) openPin(img *image.RGBA, bounds image.Rectangle) (*pinWindow, error) {
	b, err := newBitmap(img)
	if err != nil {
		return nil, err
	}
	p := &pinWindow{d: d, image: img, bitmap: b, state: pinState{Original: bounds, Bounds: bounds, Shadow: d.preferences.NewPinShadow}}
	p.hwnd = d.createWindow("FLScreenshotPin", "贴图：方向键微调，右键操作", wsExTopmost|wsExToolwindow, d.controller, bounds)
	if p.hwnd == 0 {
		b.close()
		return nil, errors.New("无法创建贴图窗口")
	}
	p.shadow = d.createWindow("FLScreenshotShadow", "", wsExTopmost|wsExToolwindow|wsExNoactivate|wsExLayered|wsExTransparent, p.hwnd, bounds.Inset(-16))
	if p.shadow == 0 {
		b.close()
		call(user32, "DestroyWindow", p.hwnd)
		return nil, errors.New("无法创建贴图阴影")
	}
	d.pins[p.hwnd] = p
	d.pinOrder = append(d.pinOrder, p.hwnd)
	d.active.Add(1)
	p.syncWindow()
	return p, nil
}

func (p *pinWindow) close() {
	if p.d.pins[p.hwnd] != p {
		return
	}
	delete(p.d.pins, p.hwnd)
	p.d.pinOrder = slices.DeleteFunc(p.d.pinOrder, func(hwnd uintptr) bool { return hwnd == p.hwnd })
	if p.dragging {
		call(user32, "ReleaseCapture")
	}
	call(user32, "DestroyWindow", p.shadow)
	call(user32, "DestroyWindow", p.hwnd)
	p.bitmap.close()
	p.shadowBitmap.close()
	p.d.active.Add(-1)
}

func (p *pinWindow) syncWindow() {
	if p.state.Hidden {
		call(user32, "ShowWindow", p.shadow, 0)
		call(user32, "ShowWindow", p.hwnd, 0)
		return
	}
	windowAt(p.hwnd, p.state.Bounds, false)
	p.syncShadow()
	invalidate(p.hwnd)
}

func (p *pinWindow) syncShadow() {
	if !p.state.Shadow {
		call(user32, "ShowWindow", p.shadow, 0)
		return
	}
	const margin = 16
	bounds := p.state.Bounds.Inset(-margin)
	if p.shadowBitmap == nil || p.shadowBitmap.width != bounds.Dx() || p.shadowBitmap.height != bounds.Dy() {
		p.shadowBitmap.close()
		img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		shadowColor := systemColor(16) // COLOR_BTNSHADOW follows system/high-contrast palette.
		for y := 0; y < img.Rect.Dy(); y++ {
			for x := 0; x < img.Rect.Dx(); x++ {
				// Transparent interior leaves the source pixels untouched. A small
				// downward offset and Gaussian falloff distinguish the image edge.
				if x >= margin && x < bounds.Dx()-margin && y >= margin && y < bounds.Dy()-margin {
					continue
				}
				dx := max(float64(margin-x), 0, float64(x-(bounds.Dx()-margin-1)))
				dy := max(float64(margin+3-y), 0, float64(y-(bounds.Dy()-margin+2)))
				a := uint8(110 * math.Exp(-(dx*dx+dy*dy)/45))
				img.SetRGBA(x, y, color.RGBA{uint8(uint16(shadowColor&255) * uint16(a) / 255), uint8(uint16((shadowColor>>8)&255) * uint16(a) / 255), uint8(uint16((shadowColor>>16)&255) * uint16(a) / 255), a})
			}
		}
		var err error
		p.shadowBitmap, err = newBitmap(img)
		if err != nil {
			logShadowError(p, err)
			return
		}
	}
	dc := call(user32, "GetDC", 0)
	defer call(user32, "ReleaseDC", 0, dc)
	mem := call(gdi32, "CreateCompatibleDC", dc)
	old := call(gdi32, "SelectObject", mem, p.shadowBitmap.handle)
	defer func() { call(gdi32, "SelectObject", mem, old); call(gdi32, "DeleteDC", mem) }()
	dest, size, src := point{int32(bounds.Min.X), int32(bounds.Min.Y)}, point{int32(bounds.Dx()), int32(bounds.Dy())}, point{}
	blend := [4]byte{0, 0, 255, 1}
	call(user32, "UpdateLayeredWindow", p.shadow, dc, ptr(&dest), ptr(&size), mem, ptr(&src), 0, ptr(&blend), 2)
	call(user32, "ShowWindow", p.shadow, 4) // SW_SHOWNOACTIVATE
}

func logShadowError(p *pinWindow, err error) {
	p.state.Shadow = false
	// Keep the captured image usable even if Windows cannot allocate its shadow.
	p.d.report(p.hwnd, err)
}

func (p *pinWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmClose:
		if !p.busy {
			p.close()
		}
		return 0
	case 0x0014:
		return 1
	case wmPaint:
		var ps paintStruct
		dc := call(user32, "BeginPaint", p.hwnd, ptr(&ps))
		p.bitmap.draw(dc, image.Rect(0, 0, p.state.Bounds.Dx(), p.state.Bounds.Dy()), p.image.Bounds())
		call(user32, "EndPaint", p.hwnd, ptr(&ps))
		return 0
	case wmLButtonDown:
		if p.busy {
			return 0
		}
		call(user32, "SetForegroundWindow", p.hwnd)
		call(user32, "SetFocus", p.hwnd)
		p.rememberFront()
		if !p.state.Locked {
			p.dragging, p.dragCursor, p.dragBounds = true, cursorPoint(), p.state.Bounds
			call(user32, "SetCapture", p.hwnd)
		}
		return 0
	case wmMouseMove:
		if p.dragging && !p.state.Locked {
			p.state.Bounds = p.dragBounds.Add(cursorPoint().Sub(p.dragCursor))
			p.syncWindow()
		}
		return 0
	case wmLButtonUp:
		if p.dragging {
			p.dragging = false
			call(user32, "ReleaseCapture")
		}
		return 0
	case 0x0215:
		p.dragging = false
		return 0 // WM_CAPTURECHANGED
	case wmMouseWheel:
		if !p.busy {
			if int16(wparam>>16) > 0 {
				p.state.zoom(1.1)
			} else {
				p.state.zoom(1 / 1.1)
			}
			p.syncWindow()
		}
		return 0
	case wmContextMenu:
		if !p.busy {
			pos := mousePoint(lparam)
			if lparam == ^uintptr(0) {
				pos = p.state.Bounds.Min.Add(image.Pt(12, 12))
			}
			p.menu(pos)
		}
		return 0
	case wmKeyDown:
		if p.busy {
			return 0
		}
		if p.state.nudge(uint32(wparam), shiftDown()) {
			p.syncWindow()
			return 0
		}
		switch wparam {
		case 0x1B:
			p.close()
		case 'C':
			if ctrlDown() {
				p.copy()
			}
		case 'S':
			if ctrlDown() {
				p.save()
			}
		case 0x5D:
			p.menu(p.state.Bounds.Min.Add(image.Pt(12, 12)))
		case 0x79:
			if shiftDown() {
				p.menu(p.state.Bounds.Min.Add(image.Pt(12, 12)))
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", p.hwnd, msg, wparam, lparam)
}

func (p *pinWindow) menu(pos image.Point) {
	p.rememberFront()
	menu := call(user32, "CreatePopupMenu")
	defer call(user32, "DestroyMenu", menu)
	appendMenu(menu, 1, "复制原图\tCtrl+C", false, true)
	appendMenu(menu, 2, "另存为…\tCtrl+S", false, true)
	call(user32, "AppendMenuW", menu, 0x800, 0, 0)
	appendMenu(menu, 3, "显示阴影", p.state.Shadow, true)
	appendMenu(menu, 4, "锁定位置", p.state.Locked, true)
	appendMenu(menu, 5, "恢复原始大小", false, !p.state.Locked)
	appendMenu(menu, 6, "回到原始位置", false, !p.state.Locked)
	call(user32, "AppendMenuW", menu, 0x800, 0, 0)
	appendMenu(menu, 7, "隐藏当前贴图", false, true)
	appendMenu(menu, 8, "关闭当前贴图\tEsc", false, true)
	call(user32, "SetForegroundWindow", p.hwnd)
	chosen := call(user32, "TrackPopupMenu", menu, 0x100|0x02, coord(pos.X), coord(pos.Y), 0, p.hwnd, 0)
	call(user32, "PostMessageW", p.hwnd, 0, 0, 0)
	p.applyMenu(chosen)
}

func (p *pinWindow) rememberFront() {
	p.d.pinOrder = slices.DeleteFunc(p.d.pinOrder, func(hwnd uintptr) bool { return hwnd == p.hwnd })
	p.d.pinOrder = append(p.d.pinOrder, p.hwnd)
}

func (p *pinWindow) applyMenu(chosen uintptr) {
	switch chosen {
	case 1:
		p.copy()
	case 2:
		p.save()
	case 3:
		p.state.Shadow = !p.state.Shadow
	case 4:
		p.state.Locked = !p.state.Locked
	case 5:
		if !p.state.Locked {
			p.state.Bounds.Max = p.state.Bounds.Min.Add(p.state.Original.Size())
		}
	case 6:
		if !p.state.Locked {
			p.state.Bounds = p.state.Bounds.Sub(p.state.Bounds.Min).Add(p.state.Original.Min)
		}
	case 7:
		p.state.Hidden = true
	case 8:
		p.close()
		return
	default:
		return
	}
	if p.d.pins[p.hwnd] == p {
		p.syncWindow()
	}
}

func (p *pinWindow) copy() {
	p.busy = true
	p.d.outputImage(p.image, p.state.Original, false, func(err error) {
		p.busy = false
		if p.d.pins[p.hwnd] == p {
			p.d.report(p.hwnd, err)
		}
	})
}
func (p *pinWindow) save() {
	p.busy = true
	p.d.saveAs(p.hwnd, p.image, func(err error, _ bool) {
		p.busy = false
		if p.d.pins[p.hwnd] == p {
			p.d.report(p.hwnd, err)
		}
	})
}
