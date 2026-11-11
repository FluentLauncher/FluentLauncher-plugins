//go:build windows

package main

import (
	"errors"
	"fmt"
	"image"
)

type selectionWindow struct {
	d                  *desktop
	hwnd               uintptr
	originalFocus      uintptr
	image              *image.RGBA
	background, dimmed *bitmap
	desktopBounds      image.Rectangle
	selected           image.Rectangle
	anchor             image.Point
	dragStart          image.Rectangle
	dragMode           int // 0 idle, 1 new, 2 move, 4/8/16/32 resize edges
	preferPin          bool
	busy               bool
}

func (d *desktop) openSelection(img *image.RGBA, bounds image.Rectangle, preferPin bool, focus uintptr) error {
	b, err := newBitmap(img)
	if err != nil {
		return err
	}
	dim := image.NewRGBA(img.Bounds())
	for i := 0; i < len(img.Pix); i += 4 {
		dim.Pix[i], dim.Pix[i+1], dim.Pix[i+2], dim.Pix[i+3] = uint8(uint16(img.Pix[i])*3/5), uint8(uint16(img.Pix[i+1])*3/5), uint8(uint16(img.Pix[i+2])*3/5), 255
	}
	dimBitmap, err := newBitmap(dim)
	if err != nil {
		b.close()
		return err
	}
	s := &selectionWindow{d: d, image: img, background: b, dimmed: dimBitmap, desktopBounds: bounds, preferPin: preferPin, originalFocus: focus}
	s.hwnd = d.createWindow("FLScreenshotSelection", "截图：拖选区域，Enter 确认，P 贴图，Esc 取消", wsExTopmost|wsExToolwindow, d.controller, bounds)
	if s.hwnd == 0 {
		b.close()
		dimBitmap.close()
		return errors.New("无法创建截图选区")
	}
	d.selection = s
	windowAt(s.hwnd, bounds, true)
	call(user32, "SetForegroundWindow", s.hwnd)
	call(user32, "SetFocus", s.hwnd)
	return nil
}

func (s *selectionWindow) close() {
	if s.d.selection != s {
		return
	}
	s.d.selection = nil
	call(user32, "ReleaseCapture")
	call(user32, "DestroyWindow", s.hwnd)
	s.background.close()
	s.dimmed.close()
	s.d.capturing.Store(false)
	if s.originalFocus != 0 && call(user32, "IsWindow", s.originalFocus) != 0 {
		call(user32, "SetForegroundWindow", s.originalFocus)
	}
}

func (s *selectionWindow) toolbar() image.Rectangle {
	scale := max(1.0, float64(call(user32, "GetDpiForWindow", s.hwnd))/96)
	w, h := int(380*scale), int(36*scale)
	w = min(w, s.image.Rect.Dx())
	x := max(0, min(s.selected.Max.X-w, s.image.Rect.Dx()-w))
	y := s.selected.Max.Y + 8
	if y+h > s.image.Rect.Dy() {
		y = max(0, s.selected.Min.Y-h-8)
	}
	return image.Rect(x, y, x+w, y+h)
}

func (s *selectionWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmClose:
		if !s.busy {
			s.close()
		}
		return 0
	case 0x0014:
		return 1 // WM_ERASEBKGND
	case 0x0020:
		call(user32, "SetCursor", call(user32, "LoadCursorW", 0, 32515))
		return 1 // IDC_CROSS
	case wmPaint:
		s.paint()
		return 0
	case wmRButtonUp:
		if !s.busy {
			s.close()
		}
		return 0
	case 0x0215:
		s.dragMode = 0
		return 0
	case wmLButtonDbl:
		if !s.busy && !s.selected.Empty() {
			s.output(s.defaultOutput())
		}
		return 0
	case wmLButtonDown:
		if s.busy {
			return 0
		}
		p := mousePoint(lparam)
		if !s.selected.Empty() && p.In(s.toolbar()) {
			bar := s.toolbar()
			index := min(3, (p.X-bar.Min.X)*4/bar.Dx())
			if index == 3 {
				s.close()
			} else {
				s.output([]string{"copy", "save", "pin"}[index])
			}
			return 0
		}
		s.anchor, s.dragStart = p, s.selected
		s.dragMode = s.hitTest(p)
		if s.dragMode == 1 {
			s.selected = image.Rectangle{}
		}
		call(user32, "SetCapture", s.hwnd)
		return 0
	case wmMouseMove:
		if s.dragMode != 0 && !s.busy {
			s.drag(mousePoint(lparam))
			invalidate(s.hwnd)
		}
		return 0
	case wmLButtonUp:
		if s.dragMode != 0 {
			s.drag(mousePoint(lparam))
			s.dragMode = 0
			call(user32, "ReleaseCapture")
			invalidate(s.hwnd)
		}
		return 0
	case wmKeyDown:
		if s.busy {
			return 0
		}
		switch wparam {
		case 0x1B:
			s.close()
		case 0x0D:
			s.output(s.defaultOutput())
		case 'P':
			s.output("pin")
		case 'C':
			if ctrlDown() {
				s.output("copy")
			}
		case 'S':
			if ctrlDown() {
				s.output("save")
			}
		case 0x25, 0x26, 0x27, 0x28:
			if !s.selected.Empty() {
				p := pinState{Bounds: s.selected}
				p.nudge(uint32(wparam), shiftDown())
				if p.Bounds.In(s.image.Bounds()) {
					s.selected = p.Bounds
					invalidate(s.hwnd)
				}
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", s.hwnd, msg, wparam, lparam)
}

func (s *selectionWindow) defaultOutput() string {
	if s.preferPin {
		return "pin"
	}
	return "copy"
}

func (s *selectionWindow) hitTest(p image.Point) int {
	r := s.selected
	if r.Empty() || !p.In(r.Inset(-6)) {
		return 1
	}
	mode := 0
	if abs(p.X-r.Min.X) <= 6 {
		mode |= 4
	} else if abs(p.X-r.Max.X) <= 6 {
		mode |= 8
	}
	if abs(p.Y-r.Min.Y) <= 6 {
		mode |= 16
	} else if abs(p.Y-r.Max.Y) <= 6 {
		mode |= 32
	}
	if mode != 0 {
		return mode
	}
	if p.In(r) {
		return 2
	}
	return 1
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (s *selectionWindow) drag(p image.Point) {
	p.X, p.Y = max(0, min(p.X, s.image.Rect.Dx())), max(0, min(p.Y, s.image.Rect.Dy()))
	r := s.dragStart
	switch s.dragMode {
	case 1:
		r = orderedRect(s.anchor, p)
	case 2:
		delta := p.Sub(s.anchor)
		delta.X = max(-r.Min.X, min(delta.X, s.image.Rect.Dx()-r.Max.X))
		delta.Y = max(-r.Min.Y, min(delta.Y, s.image.Rect.Dy()-r.Max.Y))
		r = r.Add(delta)
	default:
		if s.dragMode&4 != 0 {
			r.Min.X = p.X
		}
		if s.dragMode&8 != 0 {
			r.Max.X = p.X
		}
		if s.dragMode&16 != 0 {
			r.Min.Y = p.Y
		}
		if s.dragMode&32 != 0 {
			r.Max.Y = p.Y
		}
		r = orderedRect(r.Min, r.Max)
	}
	s.selected = r.Intersect(s.image.Bounds())
}

func (s *selectionWindow) paint() {
	var ps paintStruct
	dc := call(user32, "BeginPaint", s.hwnd, ptr(&ps))
	defer call(user32, "EndPaint", s.hwnd, ptr(&ps))
	s.dimmed.draw(dc, s.image.Bounds(), s.image.Bounds())
	if !s.selected.Empty() {
		s.background.draw(dc, s.selected, s.selected)
		wr := winRect(s.selected.Inset(-1))
		brush := call(gdi32, "CreateSolidBrush", systemColor(13))
		call(user32, "FrameRect", dc, ptr(&wr), brush)
		call(gdi32, "DeleteObject", brush)
		bar := s.toolbar()
		fill(dc, bar, systemColor(15))
		labels := []string{"复制 Enter", "保存 Ctrl+S", "贴图 P", "取消 Esc"}
		if s.preferPin {
			labels[0], labels[2] = "复制 Ctrl+C", "贴图 Enter"
		}
		for i, label := range labels {
			button := image.Rect(bar.Min.X+i*bar.Dx()/4, bar.Min.Y, bar.Min.X+(i+1)*bar.Dx()/4, bar.Max.Y)
			text(dc, label, button, systemColor(18))
		}
		label := fmt.Sprintf("%d × %d", s.selected.Dx(), s.selected.Dy())
		r := image.Rect(s.selected.Min.X, max(0, s.selected.Min.Y-28), min(s.image.Rect.Dx(), s.selected.Min.X+150), max(0, s.selected.Min.Y-28)+24)
		fill(dc, r, systemColor(15))
		text(dc, label, r, systemColor(18))
	} else {
		r := image.Rect(16, 16, min(s.image.Rect.Dx(), 650), 52)
		fill(dc, r, systemColor(15))
		text(dc, "拖选截图区域，拖动边缘调整大小 · Esc / 右键取消", r, systemColor(18))
	}
	if s.busy {
		r := image.Rect(16, 60, min(s.image.Rect.Dx(), 400), 96)
		fill(dc, r, systemColor(15))
		text(dc, "正在处理图片…", r, systemColor(18))
	}
}

func (s *selectionWindow) output(kind string) {
	if s.busy || s.selected.Empty() {
		return
	}
	img := crop(s.image, s.selected)
	bounds := s.selected.Add(s.desktopBounds.Min)
	if kind == "pin" {
		if _, err := s.d.openPin(img, bounds); err != nil {
			s.d.report(s.hwnd, err)
			return
		}
		s.close()
		return
	}
	s.busy = true
	invalidate(s.hwnd)
	if kind == "save" {
		s.d.saveAs(s.hwnd, img, func(err error, saved bool) {
			if s.d.selection != s {
				return
			}
			s.busy = false
			if saved {
				s.close()
			} else {
				s.d.report(s.hwnd, err)
				invalidate(s.hwnd)
			}
		})
		return
	}
	s.d.outputImage(img, bounds, false, func(err error) {
		if s.d.selection != s {
			return
		}
		s.busy = false
		if err == nil {
			s.close()
		} else {
			s.d.report(s.hwnd, err)
			invalidate(s.hwnd)
		}
	})
}
