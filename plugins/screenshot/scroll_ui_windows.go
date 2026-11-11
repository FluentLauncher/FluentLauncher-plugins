//go:build windows

package main

import (
	"fmt"
	"image"
)

func (s *scrollWindow) showPreview() {
	s.preview = true
	s.hover = -1
	s.focus = 0
	s.offset = 0
	monitor := currentMonitor(s.region.Min.Add(s.region.Size().Div(2)))
	scale := uiScale(s.hwnd)
	w := min(max(s.region.Dx()+px(24, scale), px(560, scale)), monitor.Dx()*4/5)
	h := min(max(px(360, scale), s.result.Rect.Dy()+px(104, scale)), monitor.Dy()*4/5)
	pos := monitor.Min.Add(image.Pt((monitor.Dx()-w)/2, (monitor.Dy()-h)/2))
	s.bounds = image.Rectangle{Min: pos, Max: pos.Add(image.Pt(w, h))}
	windowAt(s.hwnd, s.bounds, true)
	call(user32, "SetForegroundWindow", s.hwnd)
	call(user32, "SetFocus", s.hwnd)
	invalidate(s.hwnd)
}

func (s *scrollWindow) buttons() []image.Rectangle {
	scale := uiScale(s.hwnd)
	pad, gap, h := px(8, scale), px(5, scale), px(34, scale)
	count := 4
	if s.preview {
		count = 5
	}
	w := (s.bounds.Dx() - pad*2 - gap*(count-1)) / count
	result := make([]image.Rectangle, count)
	y := pad
	if !s.preview {
		y = px(60, scale)
	}
	for i := range result {
		x := pad + i*(w+gap)
		result[i] = image.Rect(x, y, x+w, y+h)
	}
	return result
}

func (s *scrollWindow) view() image.Rectangle {
	scale := uiScale(s.hwnd)
	return image.Rect(px(12, scale), px(52, scale), s.bounds.Dx()-px(22, scale), s.bounds.Dy()-px(44, scale))
}

func (s *scrollWindow) visibleRows() int {
	view := s.view()
	return min(s.result.Rect.Dy(), max(1, int(float64(view.Dy())/s.previewScale())))
}

func (s *scrollWindow) previewScale() float64 {
	return min(1, float64(s.view().Dx())/float64(s.result.Rect.Dx()))
}

func (s *scrollWindow) scrollbar() (image.Rectangle, image.Rectangle) {
	v := s.view()
	scale := uiScale(s.hwnd)
	track := image.Rect(v.Max.X+px(6, scale), v.Min.Y, v.Max.X+px(12, scale), v.Max.Y)
	rows := s.visibleRows()
	remaining := s.result.Rect.Dy() - rows
	h := max(px(24, scale), track.Dy()*rows/s.result.Rect.Dy())
	h = min(h, track.Dy())
	y := track.Min.Y
	if remaining > 0 {
		y += (track.Dy() - h) * s.offset / remaining
	}
	return track, image.Rect(track.Min.X, y, track.Max.X, y+h)
}

func (s *scrollWindow) scrollBy(delta int) {
	s.offset = max(0, min(s.offset+delta, s.result.Rect.Dy()-s.visibleRows()))
	invalidate(s.hwnd)
}

func (s *scrollWindow) dragScrollbar(point image.Point) {
	track, thumb := s.scrollbar()
	length := track.Dy() - thumb.Dy()
	if length <= 0 {
		return
	}
	y := max(0, min(point.Y-track.Min.Y-thumb.Dy()/2, length))
	s.offset = (s.result.Rect.Dy() - s.visibleRows()) * y / length
	invalidate(s.hwnd)
}

func (s *scrollWindow) paint() {
	var ps paintStruct
	dc := call(user32, "BeginPaint", s.hwnd, ptr(&ps))
	defer call(user32, "EndPaint", s.hwnd, ptr(&ps))
	s.draw(dc)
}

func (s *scrollWindow) draw(dc uintptr) {
	t, scale := s.d.theme, uiScale(s.hwnd)
	r := image.Rect(0, 0, s.bounds.Dx(), s.bounds.Dy())
	fill(dc, r, rgbRef(t.Background))
	roundPanel(dc, r, px(int(t.Radius), scale), t.Background, t.Border)
	labels := []string{"切换手动", "采集", "完成", "取消"}
	if !s.auto {
		labels[0] = "继续自动"
	}
	if s.preview {
		labels = []string{"复制", "保存", "贴图", "继续采集", "关闭"}
	}
	for i, r := range s.buttons() {
		bg, fg := t.Surface, t.Text
		disabled := s.busy || (!s.preview && i == 1 && s.auto)
		if !s.preview && s.finishRequested.Load() && i != 3 {
			disabled = true
		}
		if i == s.hover && !disabled {
			bg = t.SurfaceHover
		}
		primary := 2
		if s.preview {
			primary = 0
		}
		if i == primary {
			bg, fg = t.Accent, t.AccentText
		}
		if disabled {
			fg = t.TextMuted
			bg = t.Surface
		}
		roundPanel(dc, r, px(4, scale), bg, bg)
		text(dc, labels[i], r, rgbRef(fg))
		if call(user32, "GetFocus") == s.hwnd && i == s.focus {
			frame(dc, r.Inset(px(2, scale)), t.Accent, 1)
		}
	}
	if !s.preview {
		status := s.status
		if s.finishRequested.Load() {
			status = "正在生成完整预览，仍可取消"
		}
		mode := "手动"
		if s.auto {
			mode = "自动"
		}
		textAligned(dc, "长截图 · "+mode+" · "+s.progressText(), image.Rect(px(12, scale), px(4, scale), s.bounds.Dx()-px(8, scale), px(30, scale)), rgbRef(t.Text), false)
		textAligned(dc, status, image.Rect(px(12, scale), px(30, scale), s.bounds.Dx()-px(8, scale), px(54, scale)), rgbRef(t.TextMuted), false)
		return
	}
	view := s.view()
	rows := s.visibleRows()
	dest := view
	width := int(float64(s.result.Rect.Dx()) * s.previewScale())
	dest.Min.X += (view.Dx() - width) / 2
	dest.Max.X = dest.Min.X + width
	dest.Max.Y = dest.Min.Y + int(float64(rows)*s.previewScale())
	s.bitmap.draw(dc, dest, image.Rect(0, s.offset, s.result.Rect.Dx(), s.offset+rows))
	track, thumb := s.scrollbar()
	roundPanel(dc, track, px(3, scale), t.Surface, t.Surface)
	if rows < s.result.Rect.Dy() {
		roundPanel(dc, thumb, px(3, scale), t.Accent, t.Accent)
	}
	y := s.bounds.Dy() - px(38, scale)
	info := fmt.Sprintf("%d × %d · %d 帧 · 滚轮 / PgUp / PgDn 查看", s.result.Rect.Dx(), s.result.Rect.Dy(), s.frames)
	textAligned(dc, info, image.Rect(px(12, scale), y, s.bounds.Dx()-px(8, scale), y+px(20, scale)), rgbRef(t.Text), false)
	status := s.status
	if s.busy {
		status = "正在输出完整分辨率图片…"
	}
	textAligned(dc, status, image.Rect(px(12, scale), y+px(19, scale), s.bounds.Dx()-px(8, scale), s.bounds.Dy()-px(2, scale)), rgbRef(t.TextMuted), false)
}

func (s *scrollWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x0014:
		return 1
	case wmPaint:
		s.paint()
		return 0
	case wmClose:
		if !s.busy {
			s.close(true)
		}
		return 0
	case wmMouseMove:
		p := mousePoint(lparam)
		s.hover = -1
		for i, r := range s.buttons() {
			if p.In(r) {
				s.hover = i
			}
		}
		if s.dragScroll && s.preview {
			s.dragScrollbar(p)
		} else {
			invalidate(s.hwnd)
		}
		return 0
	case wmLButtonDown:
		call(user32, "SetFocus", s.hwnd)
		if s.preview && !s.busy {
			track, _ := s.scrollbar()
			if mousePoint(lparam).In(track.Inset(-px(5, uiScale(s.hwnd)))) {
				s.dragScroll = true
				call(user32, "SetCapture", s.hwnd)
				s.dragScrollbar(mousePoint(lparam))
			}
		}
		return 0
	case wmLButtonUp:
		if s.dragScroll {
			s.dragScroll = false
			call(user32, "ReleaseCapture")
			return 0
		}
		if s.busy {
			return 0
		}
		for i, r := range s.buttons() {
			if mousePoint(lparam).In(r) {
				s.focus = i
				s.activate(i)
				break
			}
		}
		return 0
	case 0x0215:
		s.dragScroll = false
		return 0
	case wmMouseWheel:
		if s.preview {
			s.scrollBy(int(-float64(int16(wparam>>16)) / s.previewScale()))
		}
		return 0
	case wmKeyDown:
		if s.busy {
			return 0
		}
		switch wparam {
		case 0x1b:
			s.close(true)
		case 0x09:
			delta := 1
			if shiftDown() {
				delta = -1
			}
			s.focus = (s.focus + delta + len(s.buttons())) % len(s.buttons())
			invalidate(s.hwnd)
		case 0x0d, 0x20:
			s.activate(s.focus)
		case 'C':
			if s.preview && ctrlDown() {
				s.activate(0)
			}
		case 'S':
			if s.preview && ctrlDown() {
				s.activate(1)
			}
		case 0x21:
			if s.preview {
				s.scrollBy(-s.visibleRows() * 4 / 5)
			}
		case 0x22:
			if s.preview {
				s.scrollBy(s.visibleRows() * 4 / 5)
			}
		case 0x24:
			if s.preview {
				s.offset = 0
				invalidate(s.hwnd)
			}
		case 0x23:
			if s.preview {
				s.offset = s.result.Rect.Dy() - s.visibleRows()
				invalidate(s.hwnd)
			}
		case 0x26:
			if s.preview {
				s.scrollBy(-40)
			}
		case 0x28:
			if s.preview {
				s.scrollBy(40)
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", s.hwnd, msg, wparam, lparam)
}

func (s *scrollWindow) activate(index int) {
	if s.busy {
		return
	}
	if !s.preview {
		if s.finishRequested.Load() && index != 3 {
			return
		}
		switch index {
		case 0:
			s.auto = !s.auto
			s.send(scrollAction{kind: "mode", auto: s.auto})
		case 1:
			if !s.auto {
				s.send(scrollAction{kind: "capture"})
			}
		case 2:
			s.finishRequested.Store(true)
			s.cancel()
		case 3:
			s.close(true)
		}
		invalidate(s.hwnd)
		return
	}
	switch index {
	case 0:
		s.busy = true
		s.d.outputImage(s.result, s.region, false, func(err error) {
			s.busy = false
			if s.d.scrolling != s {
				return
			}
			if err == nil {
				s.close(false)
			} else {
				s.d.report(s.hwnd, err)
				invalidate(s.hwnd)
			}
		})
	case 1:
		s.busy = true
		s.d.saveAs(s.hwnd, s.result, func(err error, saved bool) {
			s.busy = false
			if s.d.scrolling != s {
				return
			}
			if saved {
				s.close(false)
			} else {
				s.d.report(s.hwnd, err)
				invalidate(s.hwnd)
			}
		})
	case 2:
		bounds := image.Rectangle{Min: s.region.Min, Max: s.region.Min.Add(s.result.Rect.Size())}
		monitor := currentMonitor(s.region.Min)
		factor := min(1.0, float64(monitor.Dy()*4/5)/float64(bounds.Dy()), float64(monitor.Dx()*4/5)/float64(bounds.Dx()))
		size := image.Pt(max(1, int(float64(bounds.Dx())*factor)), max(1, int(float64(bounds.Dy())*factor)))
		pos := image.Pt(max(monitor.Min.X, min(bounds.Min.X, monitor.Max.X-size.X)), max(monitor.Min.Y, min(bounds.Min.Y, monitor.Max.Y-size.Y)))
		// Size the window before its first shadow allocation; a tall result
		// must not create a screen-sized shadow for tens of thousands of rows.
		if _, err := s.d.createPin(s.result, bounds, image.Rectangle{Min: pos, Max: pos.Add(size)}, s.bitmap); err != nil {
			s.d.report(s.hwnd, err)
			return
		}
		s.bitmap = nil
		s.close(false)
	case 3:
		s.resume()
	case 4:
		s.close(true)
	}
}
