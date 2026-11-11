//go:build windows

package main

import (
	"image"
	"math"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type annotation struct {
	kind       string
	points     []image.Point
	color      uint32
	width      int
	text       string
	fontHeight int // Explicit size only for the scaled live preview.
}

func (m annotation) draw(dc uintptr, source *image.RGBA, offset image.Point) {
	if len(m.points) == 0 {
		return
	}
	a := m.points[0].Sub(offset)
	b := m.points[len(m.points)-1].Sub(offset)
	r := orderedRect(a, b)
	switch m.kind {
	case "rect":
		frame(dc, r, m.color, m.width)
	case "ellipse":
		pen := call(gdi32, "CreatePen", 0, m.width, rgbRef(m.color))
		oldPen := call(gdi32, "SelectObject", dc, pen)
		oldBrush := call(gdi32, "SelectObject", dc, call(gdi32, "GetStockObject", 5))
		call(gdi32, "Ellipse", dc, coord(r.Min.X), coord(r.Min.Y), coord(r.Max.X), coord(r.Max.Y))
		call(gdi32, "SelectObject", dc, oldBrush)
		call(gdi32, "SelectObject", dc, oldPen)
		call(gdi32, "DeleteObject", pen)
	case "arrow":
		line(dc, a, b, m.color, m.width)
		angle := math.Atan2(float64(b.Y-a.Y), float64(b.X-a.X))
		length := float64(max(10, m.width*4))
		for _, turn := range []float64{-0.5, 0.5} {
			end := b.Sub(image.Pt(int(length*math.Cos(angle+turn)), int(length*math.Sin(angle+turn))))
			line(dc, b, end, m.color, m.width)
		}
	case "pen":
		for i := 1; i < len(m.points); i++ {
			line(dc, m.points[i-1].Sub(offset), m.points[i].Sub(offset), m.color, m.width)
		}
	case "mosaic":
		area := orderedRect(m.points[0], m.points[len(m.points)-1]).Intersect(source.Bounds())
		block := max(8, m.width*4)
		for y := area.Min.Y; y < area.Max.Y; y += block {
			for x := area.Min.X; x < area.Max.X; x += block {
				cell := image.Rect(x, y, min(x+block, area.Max.X), min(y+block, area.Max.Y))
				var red, green, blue, n uint32
				for yy := cell.Min.Y; yy < cell.Max.Y; yy++ {
					for xx := cell.Min.X; xx < cell.Max.X; xx++ {
						c := source.RGBAAt(xx, yy)
						red += uint32(c.R)
						green += uint32(c.G)
						blue += uint32(c.B)
						n++
					}
				}
				fill(dc, cell.Sub(offset), rgbRef((red/n)<<16|(green/n)<<8|blue/n))
			}
		}
	case "text":
		height := m.fontHeight
		if height == 0 {
			height = max(18, m.width*6)
		}
		font := logFont{Height: -int32(height), Weight: 400, Quality: 5}
		face, _ := windows.UTF16FromString("Segoe UI")
		copy(font.Face[:], face)
		h := call(gdi32, "CreateFontIndirectW", ptr(&font))
		old := call(gdi32, "SelectObject", dc, h)
		call(gdi32, "SetBkMode", dc, 1)
		call(gdi32, "SetTextColor", dc, rgbRef(m.color))
		wr := winRect(image.Rect(a.X, a.Y, source.Rect.Max.X-offset.X, source.Rect.Max.Y-offset.Y))
		call(user32, "DrawTextW", dc, ptr(u16(m.text)), ^uintptr(0), ptr(&wr), 0x800|0x10)
		call(gdi32, "SelectObject", dc, old)
		call(gdi32, "DeleteObject", h)
	}
}

func renderAnnotations(source *image.RGBA, marks []annotation) (*image.RGBA, error) {
	if len(marks) == 0 {
		return source, nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b, err := newBitmap(source)
	if err != nil {
		return nil, err
	}
	defer b.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
	for _, m := range marks {
		m.draw(dc, source, source.Rect.Min)
	}
	return b.pixels()
}

func (s *selectionWindow) annotationMarks() []annotation {
	marks := append([]annotation(nil), s.marks...)
	for i := range marks {
		marks[i].points = append([]image.Point(nil), marks[i].points...)
		for j := range marks[i].points {
			marks[i].points[j] = marks[i].points[j].Sub(s.selected.Min)
		}
	}
	return marks
}

func (s *selectionWindow) undoMark(redo bool) {
	s.finishText(true)
	if redo && len(s.redo) > 0 {
		s.marks = append(s.marks, s.redo[len(s.redo)-1])
		s.redo = s.redo[:len(s.redo)-1]
	} else if !redo && len(s.marks) > 0 {
		s.redo = append(s.redo, s.marks[len(s.marks)-1])
		s.marks = s.marks[:len(s.marks)-1]
	}
	if s.hwnd != 0 {
		invalidate(s.hwnd)
	}
}

var editSubclass = windows.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam, id, data uintptr) uintptr {
	if gui != nil && gui.selection != nil && gui.selection.edit == hwnd {
		s := gui.selection
		if msg == wmKeyDown && (wparam == 0x0d || wparam == 0x1b) {
			s.finishText(wparam == 0x0d)
			return 0
		}
		if msg == 0x0102 && (wparam == 0x0d || wparam == 0x1b) {
			return 0
		}
	}
	return call(comctl32, "DefSubclassProc", hwnd, msg, wparam, lparam)
})

func (s *selectionWindow) startText(p image.Point) {
	s.finishText(true)
	w, h := min(px(260, uiScale(s.hwnd)), s.selected.Max.X-p.X), min(px(32, uiScale(s.hwnd)), s.selected.Max.Y-p.Y)
	if w < 20 || h < 16 {
		return
	}
	s.textPoint = p
	s.edit = call(user32, "CreateWindowExW", 0, ptr(u16("EDIT")), ptr(u16("")), 0x50000000|0x00800000|0x80, coord(p.X), coord(p.Y), w, h, s.hwnd, 0, call(kernel32, "GetModuleHandleW", 0), 0)
	if s.edit == 0 {
		return
	}
	font := logFont{Height: -int32(max(18, s.strokeWidth*6)), Weight: 400, Quality: 5}
	s.editFont = call(gdi32, "CreateFontIndirectW", ptr(&font))
	s.editBrush = call(gdi32, "CreateSolidBrush", rgbRef(s.d.theme.Background))
	call(user32, "SendMessageW", s.edit, 0x30, s.editFont, 1)
	call(user32, "SendMessageW", s.edit, 0xc5, 2000, 0)
	call(comctl32, "SetWindowSubclass", s.edit, editSubclass, 1, 0)
	call(user32, "SetFocus", s.edit)
}

func (s *selectionWindow) finishText(commit bool) {
	if s.edit == 0 {
		return
	}
	hwnd := s.edit
	s.edit = 0
	if commit {
		buffer := make([]uint16, 2001)
		call(user32, "GetWindowTextW", hwnd, unsafe.Pointer(&buffer[0]), len(buffer))
		value := strings.TrimSpace(windows.UTF16ToString(buffer))
		if value != "" {
			s.marks = append(s.marks, annotation{kind: "text", points: []image.Point{s.textPoint}, text: value, color: s.ink, width: s.strokeWidth})
			s.redo = nil
		}
	}
	call(user32, "DestroyWindow", hwnd)
	call(gdi32, "DeleteObject", s.editFont)
	call(gdi32, "DeleteObject", s.editBrush)
	s.editFont, s.editBrush = 0, 0
	call(user32, "SetFocus", s.hwnd)
	invalidate(s.hwnd)
}
