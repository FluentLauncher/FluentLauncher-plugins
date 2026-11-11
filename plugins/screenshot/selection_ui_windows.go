//go:build windows

package main

import (
	"fmt"
	"image"
)

type captureTool struct{ id, label, hint string }

var captureTools = []captureTool{
	{"rect", "矩形", "R"}, {"ellipse", "椭圆", "E"}, {"arrow", "箭头", "A"},
	{"pen", "画笔", "B"}, {"mosaic", "马赛克", "M"}, {"text", "文字", "T"},
	{"undo", "撤销", "Ctrl+Z"}, {"redo", "重做", "Ctrl+Y"},
	{"scroll", "滚动截图", "L"}, {"pin", "原地贴图", "P"}, {"save", "保存", "Ctrl+S"},
	{"close", "取消", "Esc"}, {"check", "完成", "Enter"},
}

func (s *selectionWindow) toolbarButtons() []image.Rectangle {
	bar := s.toolbar()
	scale := uiScale(s.hwnd)
	gap := px(2, scale)
	inner := image.Rect(bar.Min.X+px(4, scale), bar.Min.Y+px(3, scale), bar.Max.X-px(4, scale), bar.Min.Y+px(31, scale))
	buttons := make([]image.Rectangle, len(captureTools))
	if s.longMode {
		buttons = make([]image.Rectangle, 4)
	}
	for i := range buttons {
		buttons[i] = image.Rect(inner.Min.X+i*inner.Dx()/len(buttons), inner.Min.Y, inner.Min.X+(i+1)*inner.Dx()/len(buttons)-gap, inner.Max.Y)
	}
	return buttons
}
func (s *selectionWindow) toolbarHit(p image.Point) int {
	if s.selected.Empty() || s.dragMode != 0 {
		return -1
	}
	for i, r := range s.toolbarButtons() {
		if p.In(r) {
			return i
		}
	}
	return -1
}
func (s *selectionWindow) paintToolbar(dc uintptr) {
	t, scale := s.d.theme, uiScale(s.hwnd)
	bar := s.toolbar()
	s.paintFrozenPanel(dc, bar)
	primary := len(captureTools) - 1
	if s.preferPin {
		primary = 9
	}
	if s.longMode {
		primary = 2
	}
	for i, r := range s.toolbarButtons() {
		color := t.Text
		id := captureTools[i].id
		if s.longMode {
			id = []string{"scroll", "cursor", "start", "close"}[i]
		}
		disabled := !s.longMode && ((id == "undo" && len(s.marks) == 0) || (id == "redo" && len(s.redo) == 0))
		if i == primary || id == s.tool {
			roundPanel(dc, r, px(4, scale), t.Accent, t.Accent)
			color = t.AccentText
		} else if i == s.toolbarHover {
			roundPanel(dc, r, px(4, scale), t.SurfaceHover, t.SurfaceHover)
		}
		if s.longMode && ((i == 0 && s.longAuto) || (i == 1 && !s.longAuto)) {
			roundPanel(dc, r, px(4, scale), t.SurfaceHover, t.Accent)
		}
		if id == "close" && i == s.toolbarHover {
			color = t.Danger
		}
		if disabled {
			color = t.TextMuted
		}
		size := px(18, scale)
		x := (r.Min.X + r.Max.X - size) / 2
		y := (r.Min.Y + r.Max.Y - size) / 2
		if !s.longMode {
			icon(dc, id, image.Rect(x, y, x+size, y+size), color)
		}
		if s.longMode {
			text(dc, []string{"自动", "手动", "开始", "取消"}[i], r, rgbRef(color))
		}
		if i == s.toolbarFocus {
			frame(dc, r.Inset(1), t.Accent, 1)
		}
	}
	hintIndex := s.toolbarHover
	if hintIndex < 0 {
		hintIndex = s.toolbarFocus
	}
	if hintIndex >= 0 {
		hint := captureTools[hintIndex].label + " · " + captureTools[hintIndex].hint
		if s.longMode {
			hint = []string{"自动向下滚动 · A", "手动滚动后采集 · M", "开始长截图 · Enter", "取消 · Esc"}[hintIndex]
		}
		r := image.Rect(bar.Min.X, max(0, bar.Min.Y-px(34, scale)), min(s.image.Rect.Dx(), bar.Min.X+px(205, scale)), max(0, bar.Min.Y-px(34, scale))+px(28, scale))
		roundPanel(dc, r, px(4, scale), t.Inverse, t.Inverse)
		text(dc, hint, r, rgbRef(t.InverseText))
	}
	if s.tool != "" && !s.longMode {
		for i, r := range s.inkOptions() {
			if i < 4 {
				c := s.inkColors()[i]
				roundPanel(dc, r.Inset(px(5, scale)), px(3, scale), c, c)
				if c == s.ink {
					frame(dc, r, t.Accent, 1)
				}
			} else {
				if s.strokeWidth == []int{1, 3, 6}[i-4] {
					roundPanel(dc, r, px(3, scale), t.SurfaceHover, t.Border)
				}
				line(dc, image.Pt(r.Min.X+5, (r.Min.Y+r.Max.Y)/2), image.Pt(r.Max.X-5, (r.Min.Y+r.Max.Y)/2), t.Text, []int{1, 3, 6}[i-4])
			}
		}
		textAligned(dc, "再次点击工具可调整选区", image.Rect(bar.Min.X+px(220, scale), bar.Min.Y+px(34, scale), bar.Max.X, bar.Max.Y), rgbRef(t.TextMuted), false)
	}
}

func (s *selectionWindow) inkColors() []uint32 {
	t := s.d.theme
	return []uint32{t.Danger, t.Accent, t.Text, t.AccentText}
}
func (s *selectionWindow) inkOptions() []image.Rectangle {
	bar, scale := s.toolbar(), uiScale(s.hwnd)
	var out []image.Rectangle
	for i := 0; i < 7; i++ {
		x := bar.Min.X + px(5+i*30, scale)
		out = append(out, image.Rect(x, bar.Min.Y+px(35, scale), x+px(26, scale), bar.Min.Y+px(59, scale)))
	}
	return out
}

func (s *selectionWindow) activateTool(index int) {
	s.finishText(true)
	if s.longMode {
		if index < 2 {
			s.longAuto = index == 0
			invalidate(s.hwnd)
		} else if index == 3 {
			s.close()
		} else {
			s.output("scroll")
		}
		return
	}
	id := captureTools[index].id
	switch id {
	case "rect", "ellipse", "arrow", "pen", "mosaic", "text":
		if s.tool == id {
			s.tool = ""
		} else {
			s.tool = id
		}
	case "undo", "redo":
		s.undoMark(id == "redo")
	case "scroll":
		s.longAuto = true
		s.output("scroll")
	case "close":
		s.close()
	case "check":
		s.output(s.defaultOutput())
	default:
		s.output(id)
	}
	invalidate(s.hwnd)
}

func (s *selectionWindow) paintSize(dc uintptr) {
	t, scale := s.d.theme, uiScale(s.hwnd)
	h, w := px(26, scale), px(128, scale)
	y := max(0, s.selected.Min.Y-h-px(6, scale))
	x := min(s.selected.Min.X, max(0, s.image.Rect.Dx()-w))
	r := image.Rect(x, y, min(x+w, s.image.Rect.Dx()), y+h)
	roundPanel(dc, r, px(4, scale), t.Background, t.Border)
	text(dc, fmt.Sprintf("%d × %d", s.selected.Dx(), s.selected.Dy()), r, rgbRef(t.Text))
}

// Lens placement is local to the frozen virtual desktop. Flip sides at edges,
// then clamp so the complete HUD stays visible even on small displays.
func lensBounds(cursor, size image.Point, bounds image.Rectangle, gap int) image.Rectangle {
	pos := cursor.Add(image.Pt(gap, gap))
	if pos.X+size.X > bounds.Max.X {
		pos.X = cursor.X - gap - size.X
	}
	if pos.Y+size.Y > bounds.Max.Y {
		pos.Y = cursor.Y - gap - size.Y
	}
	pos.X = max(bounds.Min.X, min(pos.X, bounds.Max.X-size.X))
	pos.Y = max(bounds.Min.Y, min(pos.Y, bounds.Max.Y-size.Y))
	return image.Rectangle{Min: pos, Max: pos.Add(size)}.Intersect(bounds)
}

func (s *selectionWindow) paintMagnifier(dc uintptr) {
	t, scale := s.d.theme, uiScale(s.hwnd)
	const cols, rows = 17, 11
	cell, pad := px(8, scale), px(9, scale)
	size := image.Pt(cols*cell+pad*2, rows*cell+pad*2+px(52, scale))
	cursor := s.cursor
	cursor.X = max(0, min(cursor.X, s.image.Rect.Dx()-1))
	cursor.Y = max(0, min(cursor.Y, s.image.Rect.Dy()-1))
	// Keep the HUD on the monitor containing the pointer, not in virtual gaps.
	monitor := currentMonitor(cursor.Add(s.desktopBounds.Min)).Sub(s.desktopBounds.Min).Intersect(s.image.Bounds())
	if monitor.Empty() {
		monitor = s.image.Bounds()
	}
	r := lensBounds(cursor, size, monitor, px(22, scale))
	if s.dragMode == 0 && !s.selected.Empty() && r.Overlaps(s.toolbar()) {
		for _, delta := range []image.Point{image.Pt(-size.X-px(22, scale), px(22, scale)), image.Pt(px(22, scale), -size.Y-px(22, scale)), image.Pt(-size.X-px(22, scale), -size.Y-px(22, scale))} {
			candidate := image.Rectangle{Min: cursor.Add(delta), Max: cursor.Add(delta).Add(size)}
			if candidate.In(monitor) && !candidate.Overlaps(s.toolbar()) {
				r = candidate
				break
			}
		}
	}
	roundPanel(dc, r, px(int(t.Radius), scale), t.Background, t.Border)
	origin := r.Min.Add(image.Pt(pad, pad))
	grid := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(cols*cell, rows*cell))}
	fill(dc, grid, rgbRef(t.Surface))
	source := image.Rect(cursor.X-cols/2, cursor.Y-rows/2, cursor.X-cols/2+cols, cursor.Y-rows/2+rows)
	clipped := source.Intersect(s.image.Bounds())
	start := origin.Add(clipped.Min.Sub(source.Min).Mul(cell))
	destination := image.Rectangle{Min: start, Max: start.Add(clipped.Size().Mul(cell))}
	s.background.drawMode(dc, destination, clipped, 3) // COLORONCOLOR: exact nearest-neighbor pixels.
	for x := 0; x <= cols; x++ {
		line(dc, origin.Add(image.Pt(x*cell, 0)), origin.Add(image.Pt(x*cell, rows*cell)), t.Border, 1)
	}
	for y := 0; y <= rows; y++ {
		line(dc, origin.Add(image.Pt(0, y*cell)), origin.Add(image.Pt(cols*cell, y*cell)), t.Border, 1)
	}
	center := image.Rect(origin.X+cols/2*cell, origin.Y+rows/2*cell, origin.X+(cols/2+1)*cell, origin.Y+(rows/2+1)*cell)
	frame(dc, center.Inset(-1), t.Accent, 2)
	y := origin.Y + rows*cell + px(5, scale)
	position := cursor.Add(s.desktopBounds.Min)
	textAligned(dc, fmt.Sprintf("X %d   Y %d", position.X, position.Y), image.Rect(origin.X, y, r.Max.X-pad, y+px(23, scale)), rgbRef(t.Text), false)
	c := s.image.RGBAAt(cursor.X, cursor.Y)
	swatch := image.Rect(origin.X, y+px(28, scale), origin.X+px(12, scale), y+px(40, scale))
	fill(dc, swatch, rgbRef(uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B)))
	frame(dc, swatch, t.Border, 1)
	textAligned(dc, fmt.Sprintf("RGB %d, %d, %d", c.R, c.G, c.B), image.Rect(swatch.Max.X+px(5, scale), y+px(23, scale), r.Max.X-pad, y+px(46, scale)), rgbRef(t.TextMuted), false)
}

func frame(dc uintptr, r image.Rectangle, c uint32, width int) {
	for i := 0; i < width; i++ {
		wr := winRect(r.Inset(i))
		brush := call(gdi32, "CreateSolidBrush", rgbRef(c))
		call(user32, "FrameRect", dc, ptr(&wr), brush)
		call(gdi32, "DeleteObject", brush)
	}
}
