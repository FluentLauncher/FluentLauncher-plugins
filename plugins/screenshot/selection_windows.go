//go:build windows

package main

import (
	"errors"
	"image"
)

type selectionWindow struct {
	d                         *desktop
	hwnd                      uintptr
	originalFocus             uintptr
	image                     *image.RGBA
	background, dimmed        *bitmap
	desktopBounds             image.Rectangle
	selected                  image.Rectangle
	anchor                    image.Point
	dragStart                 image.Rectangle
	dragMode                  int // 0 idle, 1 new, 2 move, 4/8/16/32 resize edges
	preferPin                 bool
	busy                      bool
	cursor                    image.Point
	toolbarHover              int
	toolbarFocus              int
	longMode, longAuto        bool
	tool                      string
	ink                       uint32
	strokeWidth               int
	marks, redo               []annotation
	draft                     *annotation
	edit, editFont, editBrush uintptr
	textPoint                 image.Point
	panelCache                *bitmap
	panelBounds               image.Rectangle
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
	s := &selectionWindow{d: d, image: img, background: b, dimmed: dimBitmap, desktopBounds: bounds, preferPin: preferPin, originalFocus: focus, cursor: cursorPoint().Sub(bounds.Min), toolbarHover: -1, toolbarFocus: -1}
	s.hwnd = d.createWindow("FLScreenshotSelection", "截图：拖选区域，Enter 确认，P 贴图，Esc 取消", wsExTopmost|wsExToolwindow, d.controller, bounds)
	if s.hwnd == 0 {
		b.close()
		dimBitmap.close()
		return errors.New("无法创建截图选区")
	}
	d.selection = s
	s.ink, s.strokeWidth = d.theme.Danger, 3
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
	s.finishText(false)
	call(user32, "ReleaseCapture")
	call(user32, "DestroyWindow", s.hwnd)
	s.background.close()
	s.dimmed.close()
	s.panelCache.close()
	s.d.capturing.Store(false)
	if s.originalFocus != 0 && call(user32, "IsWindow", s.originalFocus) != 0 {
		call(user32, "SetForegroundWindow", s.originalFocus)
	}
}

func (s *selectionWindow) toolbar() image.Rectangle {
	scale := max(1.0, float64(call(user32, "GetDpiForWindow", s.hwnd))/96)
	w, h := px(398, scale), px(34, scale)
	if s.longMode {
		w = px(248, scale)
	} else if s.tool != "" {
		h = px(64, scale)
	}
	w = min(w, s.image.Rect.Dx())
	monitor := currentMonitor(s.selected.Min.Add(s.selected.Size().Div(2)).Add(s.desktopBounds.Min)).Sub(s.desktopBounds.Min).Intersect(s.image.Bounds())
	if monitor.Empty() {
		monitor = s.image.Bounds()
	}
	w = min(w, monitor.Dx())
	x := max(monitor.Min.X, min(s.selected.Max.X-w, monitor.Max.X-w))
	y := s.selected.Max.Y + px(6, scale)
	if y+h > monitor.Max.Y {
		y = max(monitor.Min.Y, s.selected.Min.Y-h-px(6, scale))
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
		cursor := 32515
		if s.toolbarHover >= 0 {
			cursor = 32512
		}
		call(user32, "SetCursor", call(user32, "LoadCursorW", 0, cursor))
		return 1 // IDC_CROSS
	case wmPaint:
		s.paint()
		return 0
	case wmRButtonUp:
		if !s.busy {
			if s.tool != "" {
				s.finishText(false)
				s.tool = ""
				s.draft = nil
				invalidate(s.hwnd)
			} else {
				s.close()
			}
		}
		return 0
	case 0x0215:
		s.dragMode = 0
		s.draft = nil
		return 0
	case 0x0133: // WM_CTLCOLOREDIT: native edit keeps IME support.
		call(gdi32, "SetTextColor", wparam, rgbRef(s.ink))
		call(gdi32, "SetBkColor", wparam, rgbRef(s.d.theme.Background))
		return s.editBrush
	case wmLButtonDbl:
		if !s.busy && s.tool == "" && !s.selected.Empty() && mousePoint(lparam).In(s.selected) && !mousePoint(lparam).In(s.toolbar()) {
			s.output(s.defaultOutput())
		}
		return 0
	case wmLButtonDown:
		if s.busy {
			return 0
		}
		p := mousePoint(lparam)
		s.toolbarFocus = -1
		s.finishText(true)
		if index := s.toolbarHit(p); index >= 0 {
			s.activateTool(index)
			return 0
		}
		if s.tool != "" && !s.longMode {
			for i, r := range s.inkOptions() {
				if p.In(r) {
					if i < 4 {
						s.ink = s.inkColors()[i]
					} else {
						s.strokeWidth = []int{1, 3, 6}[i-4]
					}
					invalidate(s.hwnd)
					return 0
				}
			}
		}
		if !s.selected.Empty() && p.In(s.toolbar()) {
			return 0
		}
		if s.tool != "" && p.In(s.selected) {
			if s.tool == "text" {
				s.startText(p)
			} else {
				s.draft = &annotation{kind: s.tool, points: []image.Point{p, p}, color: s.ink, width: s.strokeWidth}
				call(user32, "SetCapture", s.hwnd)
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
		s.cursor = mousePoint(lparam)
		if s.draft != nil {
			p := image.Pt(max(s.selected.Min.X, min(s.cursor.X, s.selected.Max.X-1)), max(s.selected.Min.Y, min(s.cursor.Y, s.selected.Max.Y-1)))
			if s.draft.kind == "pen" {
				if p != s.draft.points[len(s.draft.points)-1] {
					s.draft.points = append(s.draft.points, p)
				}
			} else {
				s.draft.points[1] = p
			}
		}
		s.toolbarHover = s.toolbarHit(s.cursor)
		if s.dragMode != 0 && !s.busy {
			s.drag(s.cursor)
		}
		invalidate(s.hwnd)
		return 0
	case wmLButtonUp:
		s.cursor = mousePoint(lparam)
		if s.draft != nil {
			m := *s.draft
			s.draft = nil
			if m.points[0] != m.points[len(m.points)-1] {
				s.marks = append(s.marks, m)
				s.redo = nil
			}
			call(user32, "ReleaseCapture")
			invalidate(s.hwnd)
			return 0
		}
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
			if s.tool != "" {
				s.tool = ""
				invalidate(s.hwnd)
			} else {
				s.close()
			}
		case 0x0D:
			if s.toolbarFocus >= 0 {
				s.activateTool(s.toolbarFocus)
			} else {
				s.output(s.defaultOutput())
			}
		case 0x09:
			if !s.selected.Empty() {
				n := len(s.toolbarButtons())
				delta := 1
				if shiftDown() {
					delta = -1
				}
				s.toolbarFocus = (s.toolbarFocus + delta + n) % n
				invalidate(s.hwnd)
			}
		case 'P':
			if !s.longMode {
				s.output("pin")
			}
		case 'A':
			if s.longMode {
				s.longAuto = true
				invalidate(s.hwnd)
			}
			if !s.longMode && !ctrlDown() {
				s.tool = "arrow"
				invalidate(s.hwnd)
			}
		case 'M':
			if s.longMode {
				s.longAuto = false
				invalidate(s.hwnd)
			}
			if !s.longMode {
				s.tool = "mosaic"
				invalidate(s.hwnd)
			}
		case 'R', 'E', 'B', 'T':
			if !s.longMode {
				s.tool = map[uintptr]string{'R': "rect", 'E': "ellipse", 'B': "pen", 'T': "text"}[wparam]
				invalidate(s.hwnd)
			}
		case 'L':
			s.longAuto = true
			s.output("scroll")
		case 'Z', 'Y':
			if ctrlDown() {
				s.undoMark(wparam == 'Y' || shiftDown())
			}
		case 'C':
			if ctrlDown() && !s.longMode {
				s.output("copy")
			}
		case 'S':
			if ctrlDown() && !s.longMode {
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
	if s.longMode {
		return "scroll"
	}
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
	s.panelCache.close()
	s.panelCache = nil
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
		brush := call(gdi32, "CreateSolidBrush", rgbRef(s.d.theme.Accent))
		call(user32, "FrameRect", dc, ptr(&wr), brush)
		call(gdi32, "DeleteObject", brush)
		s.paintSize(dc)
		call(gdi32, "SaveDC", dc)
		call(gdi32, "IntersectClipRect", dc, coord(s.selected.Min.X), coord(s.selected.Min.Y), coord(s.selected.Max.X), coord(s.selected.Max.Y))
		for _, m := range s.marks {
			m.draw(dc, s.image, image.Point{})
		}
		if s.draft != nil {
			s.draft.draw(dc, s.image, image.Point{})
		}
		call(gdi32, "RestoreDC", dc, coord(-1))
	} else {
		r := image.Rect(16, 16, min(s.image.Rect.Dx()-16, 650), 52)
		roundPanel(dc, r, px(int(s.d.theme.Radius), uiScale(s.hwnd)), s.d.theme.Background, s.d.theme.Border)
		text(dc, "拖选截图区域，拖动边缘调整大小 · Esc / 右键取消", r, rgbRef(s.d.theme.Text))
	}
	if !s.busy && s.tool == "" && s.toolbarHover < 0 && (s.dragMode != 0 || s.selected.Empty() || s.hitTest(s.cursor) > 2) {
		s.paintMagnifier(dc)
	}
	if !s.selected.Empty() && s.dragMode == 0 {
		s.paintToolbar(dc)
	}
	if s.busy {
		r := image.Rect(16, 60, min(s.image.Rect.Dx(), 400), 96)
		roundPanel(dc, r, px(int(s.d.theme.Radius), uiScale(s.hwnd)), s.d.theme.Background, s.d.theme.Border)
		text(dc, "正在处理图片…", r, rgbRef(s.d.theme.Text))
	}
}

func (s *selectionWindow) output(kind string) {
	if s.busy || s.selected.Empty() {
		return
	}
	s.finishText(true)
	if kind == "scroll" {
		region, scale, auto := s.selected.Add(s.desktopBounds.Min), uiScale(s.hwnd), s.longAuto
		s.d.active.Add(1)
		defer s.d.active.Add(-1)
		seed := longSeed{crop(s.image, s.selected), s.annotationMarks()}
		s.close()
		s.d.startLongCapture(region, auto, scale, seed)
		if s.d.scrolling == nil {
			if err := s.d.openSelection(s.image, s.desktopBounds, s.preferPin, s.originalFocus); err != nil {
				s.d.report(s.d.controller, err)
			} else {
				restored := s.d.selection
				restored.selected, restored.marks, restored.redo, restored.tool = s.selected, s.marks, s.redo, s.tool
				restored.ink, restored.strokeWidth = s.ink, s.strokeWidth
				s.d.capturing.Store(true)
			}
		}
		return
	}
	img := crop(s.image, s.selected)
	var err error
	img, err = renderAnnotations(img, s.annotationMarks())
	if err != nil {
		s.d.report(s.hwnd, err)
		return
	}
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
