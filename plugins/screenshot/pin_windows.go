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
	rendered             *image.RGBA
	bitmap, shadowBitmap *bitmap
	dragging             bool
	dragCursor           image.Point
	dragBounds           image.Rectangle
	busy                 bool
	nativeTaskbar        bool
	fileDrag             bool
}

func (d *desktop) openPin(img *image.RGBA, bounds image.Rectangle) (*pinWindow, error) {
	return d.openPinAt(img, bounds, bounds)
}

func (d *desktop) openPinAt(img *image.RGBA, original, bounds image.Rectangle) (*pinWindow, error) {
	b, err := newBitmap(img)
	if err != nil {
		return nil, err
	}
	p, err := d.createPin(img, original, bounds, b)
	if err != nil {
		b.close()
	}
	return p, err
}

// createPin takes ownership of the prepared bitmap only on success. Long
// result previews can transfer it without another full-resolution UI copy.
func (d *desktop) createPin(img *image.RGBA, original, bounds image.Rectangle, b *bitmap) (*pinWindow, error) {
	p := &pinWindow{d: d, image: img, rendered: img, bitmap: b, state: pinState{Original: original, Bounds: bounds, Shadow: d.preferences.NewPinShadow, Topmost: true, Opacity: 100}}
	p.hwnd = d.createWindow("FLScreenshotPin", "贴图：方向键微调，右键操作", wsExTopmost|wsExToolwindow|wsExLayered, d.controller, bounds)
	if p.hwnd == 0 {
		return nil, errors.New("无法创建贴图窗口")
	}
	p.shadow = d.createWindow("FLScreenshotShadow", "", wsExTopmost|wsExToolwindow|wsExNoactivate|wsExLayered|wsExTransparent, p.hwnd, bounds.Inset(-16))
	if p.shadow == 0 {
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
	if p.d.menuRoot != nil && p.d.menuRoot.owner == p.hwnd {
		p.d.closeMenu()
	}
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
	if p.nativeTaskbar != p.state.Taskbar {
		call(user32, "ShowWindow", p.hwnd, 0)
		exstyle := uintptr(wsExLayered)
		owner := p.d.controller
		if p.state.Taskbar {
			exstyle |= 0x40000
			owner = 0
		} else {
			exstyle |= wsExToolwindow
		}
		call(user32, "SetWindowLongPtrW", p.hwnd, coord(-20), exstyle)
		call(user32, "SetWindowLongPtrW", p.hwnd, coord(-8), owner)
		p.nativeTaskbar = p.state.Taskbar
	}
	call(user32, "SetLayeredWindowAttributes", p.hwnd, 0, max(10, min(100, p.state.Opacity))*255/100, 2)
	if p.state.Hidden {
		call(user32, "ShowWindow", p.shadow, 0)
		call(user32, "ShowWindow", p.hwnd, 0)
		return
	}
	windowAtLevel(p.hwnd, p.state.Bounds, false, p.state.Topmost)
	p.syncShadow()
	invalidate(p.hwnd)
}

func (p *pinWindow) syncShadow() {
	if !p.state.Shadow || p.state.Hidden {
		call(user32, "ShowWindow", p.shadow, 0)
		return
	}
	const margin = 16
	bounds := p.state.Bounds.Inset(-margin)
	if p.shadowBitmap == nil || p.shadowBitmap.width != bounds.Dx() || p.shadowBitmap.height != bounds.Dy() {
		p.shadowBitmap.close()
		img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		shadowColor := rgbRef(p.d.theme.Border)
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
	blend := [4]byte{0, 0, uint8(max(10, min(100, p.state.Opacity)) * 255 / 100), 1}
	call(user32, "UpdateLayeredWindow", p.shadow, dc, ptr(&dest), ptr(&size), mem, ptr(&src), 0, ptr(&blend), 2)
	call(user32, "ShowWindow", p.shadow, 4) // SW_SHOWNOACTIVATE
	windowAtLevel(p.shadow, bounds, false, p.state.Topmost)
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
		p.bitmap.draw(dc, image.Rect(0, 0, p.state.Bounds.Dx(), p.state.Bounds.Dy()), p.rendered.Bounds())
		call(user32, "EndPaint", p.hwnd, ptr(&ps))
		return 0
	case wmLButtonDown:
		if p.busy {
			return 0
		}
		call(user32, "SetForegroundWindow", p.hwnd)
		call(user32, "SetFocus", p.hwnd)
		p.rememberFront()
		if ctrlDown() {
			p.fileDrag = true
			p.dragCursor = cursorPoint()
			call(user32, "SetCapture", p.hwnd)
			return 0
		}
		if !p.state.Locked {
			p.dragging, p.dragCursor, p.dragBounds = true, cursorPoint(), p.state.Bounds
			call(user32, "SetCapture", p.hwnd)
		}
		return 0
	case wmMouseMove:
		if p.fileDrag && !p.busy && abs(cursorPoint().X-p.dragCursor.X)+abs(cursorPoint().Y-p.dragCursor.Y) > 8 {
			p.fileDrag = false
			call(user32, "ReleaseCapture")
			p.exportFile(true)
			return 0
		}
		if p.dragging && !p.state.Locked {
			p.state.Bounds = p.dragBounds.Add(cursorPoint().Sub(p.dragCursor))
			p.syncWindow()
		}
		return 0
	case wmLButtonUp:
		p.fileDrag = false
		if p.dragging {
			p.dragging = false
			call(user32, "ReleaseCapture")
		}
		return 0
	case 0x0215:
		p.dragging = false
		p.fileDrag = false
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
	p.d.showMenu(p.hwnd, pos, p.menuItems(), func(id int) { p.applyMenu(uintptr(id)) })
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
			p.state.Bounds.Max = p.state.Bounds.Min.Add(p.state.naturalSize())
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
	case 9:
		p.state.Topmost = !p.state.Topmost
	case 10:
		p.state.Taskbar = !p.state.Taskbar
	case 11:
		p.transform(func(next *pinState) {
			next.Angle = 0
			next.MirrorX = false
			next.MirrorY = false
			next.Bounds = next.Original
			next.Opacity = 100
			next.Locked = false
		})
		return
	case 12:
		p.exportFile(false)
		return
	case 13:
		p.help(posForHelp(p))
		return
	case 31, 32, 33:
		if !p.state.Locked {
			p.transform(func(next *pinState) {
				delta := 1
				if chosen == 32 {
					delta = 3
				}
				if chosen == 33 {
					delta = 2
				}
				next.Angle = (next.Angle + delta) % 4
				if delta%2 != 0 {
					size := next.Bounds.Size()
					next.Bounds.Max = next.Bounds.Min.Add(image.Pt(size.Y, size.X))
				}
			})
		}
		return
	case 41, 42:
		if !p.state.Locked {
			p.transform(func(next *pinState) {
				if chosen == 41 {
					next.MirrorX = !next.MirrorX
				} else {
					next.MirrorY = !next.MirrorY
				}
			})
		}
		return
	default:
		if chosen >= 110 && chosen <= 200 {
			p.state.Opacity = int(chosen) - 100
		} else if chosen >= 225 && chosen <= 400 {
			if !p.state.Locked {
				percent := int(chosen) - 200
				size := p.state.naturalSize()
				p.state.Bounds.Max = p.state.Bounds.Min.Add(image.Pt(max(1, size.X*percent/100), max(1, size.Y*percent/100)))
			}
		} else {
			return
		}
	}
	if p.d.pins[p.hwnd] == p {
		p.syncWindow()
	}
}

func (p *pinWindow) copy() {
	p.busy = true
	p.d.outputImage(p.rendered, p.state.Original, false, func(err error) {
		p.busy = false
		if p.d.pins[p.hwnd] == p {
			p.d.report(p.hwnd, err)
		}
	})
}
func (p *pinWindow) save() {
	p.busy = true
	p.d.saveAs(p.hwnd, p.rendered, func(err error, _ bool) {
		p.busy = false
		if p.d.pins[p.hwnd] == p {
			p.d.report(p.hwnd, err)
		}
	})
}
