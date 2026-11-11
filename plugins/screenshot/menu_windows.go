//go:build windows

package main

import "image"

type menuItem struct {
	ID                        int
	Label, Hint, Icon         string
	Checked, Disabled, Danger bool
	Children                  []menuItem
}

type popupWindow struct {
	d             *desktop
	hwnd, owner   uintptr
	bounds        image.Rectangle
	items         []menuItem
	rows          []image.Rectangle
	hover         int
	scale         float64
	parent, child *popupWindow
	choose        func(int)
}

func (d *desktop) showMenu(owner uintptr, pos image.Point, items []menuItem, choose func(int)) {
	d.closeMenu()
	p := d.openPopup(owner, pos, items, nil, choose)
	if p == nil {
		return
	}
	d.menuRoot = p
	d.active.Add(1)
	call(user32, "SetForegroundWindow", p.hwnd)
	call(user32, "SetFocus", p.hwnd)
	call(user32, "SetCapture", p.hwnd)
}

func (d *desktop) openPopup(owner uintptr, pos image.Point, items []menuItem, parent *popupWindow, choose func(int)) *popupWindow {
	scale := uiScale(owner)
	width, padding, rowHeight := px(280, scale), px(6, scale), px(28, scale)
	rows := make([]image.Rectangle, len(items))
	y := padding
	for i, it := range items {
		h := rowHeight
		if it.Label == "" {
			h = px(9, scale)
		}
		rows[i] = image.Rect(padding, y, width-padding, y+h)
		y += h
	}
	bounds := image.Rectangle{Min: pos, Max: pos.Add(image.Pt(width, y+padding))}
	monitor := currentMonitor(pos)
	if bounds.Max.X > monitor.Max.X {
		bounds = bounds.Add(image.Pt(monitor.Max.X-bounds.Max.X, 0))
	}
	if bounds.Max.Y > monitor.Max.Y {
		bounds = bounds.Add(image.Pt(0, monitor.Max.Y-bounds.Max.Y))
	}
	if parent != nil && bounds.Overlaps(parent.bounds) {
		bounds = bounds.Add(image.Pt(parent.bounds.Min.X-bounds.Max.X+px(2, scale), 0))
	}
	bounds = bounds.Add(image.Pt(max(0, monitor.Min.X-bounds.Min.X), max(0, monitor.Min.Y-bounds.Min.Y)))
	p := &popupWindow{d: d, owner: owner, bounds: bounds, items: items, rows: rows, hover: -1, scale: scale, parent: parent, choose: choose}
	p.hwnd = d.createWindow("FLScreenshotMenu", "图片操作", wsExTopmost|wsExToolwindow, owner, bounds)
	if p.hwnd == 0 {
		return nil
	}
	d.popups[p.hwnd] = p
	radius := px(int(max(4, d.theme.Radius)), scale)
	region := call(gdi32, "CreateRoundRectRgn", 0, 0, bounds.Dx()+1, bounds.Dy()+1, radius*2, radius*2)
	if call(user32, "SetWindowRgn", p.hwnd, region, 0) == 0 {
		call(gdi32, "DeleteObject", region)
	}
	windowAt(p.hwnd, bounds, false)
	return p
}

func (p *popupWindow) close() {
	if p.child != nil {
		p.child.close()
		p.child = nil
	}
	delete(p.d.popups, p.hwnd)
	call(user32, "DestroyWindow", p.hwnd)
}
func (d *desktop) closeMenu() {
	root := d.menuRoot
	if root == nil {
		return
	}
	d.menuRoot = nil
	d.active.Add(-1)
	restoreFocus := call(user32, "GetForegroundWindow") == root.hwnd
	if call(user32, "GetCapture") == root.hwnd {
		call(user32, "ReleaseCapture")
	}
	root.close()
	if restoreFocus && call(user32, "IsWindowVisible", root.owner) != 0 {
		call(user32, "SetForegroundWindow", root.owner)
		call(user32, "SetFocus", root.owner)
	}
}

func (p *popupWindow) pointAt(pos image.Point) (*popupWindow, int) {
	if p.child != nil {
		if target, index := p.child.pointAt(pos); target != nil {
			return target, index
		}
	}
	if !pos.In(p.bounds) {
		return nil, -1
	}
	pos = pos.Sub(p.bounds.Min)
	for i, r := range p.rows {
		if pos.In(r) {
			return p, i
		}
	}
	return p, -1
}
func (p *popupWindow) selectRow(index int) {
	if index == p.hover && (index < 0 || len(p.items[index].Children) == 0 || p.child != nil) {
		return
	}
	p.hover = index
	if p.child != nil {
		p.child.close()
		p.child = nil
	}
	if index >= 0 && !p.items[index].Disabled && len(p.items[index].Children) > 0 {
		pos := p.bounds.Min.Add(image.Pt(p.bounds.Dx()-px(2, p.scale), p.rows[index].Min.Y-px(6, p.scale)))
		p.child = p.d.openPopup(p.owner, pos, p.items[index].Children, p, p.choose)
	}
	invalidate(p.hwnd)
}
func (p *popupWindow) activate(index int) {
	if index < 0 || index >= len(p.items) {
		return
	}
	it := p.items[index]
	if it.Label == "" || it.Disabled {
		return
	}
	if len(it.Children) > 0 {
		p.selectRow(index)
		if p.child != nil {
			p.child.step(1)
		}
		return
	}
	choose := p.choose
	p.d.closeMenu()
	choose(it.ID)
}
func (p *popupWindow) step(delta int) {
	n := len(p.items)
	start := p.hover
	if start < 0 && delta < 0 {
		start = 0
	}
	for i := 1; i <= n; i++ {
		index := (start + delta*i + n*2) % n
		it := p.items[index]
		if it.Label != "" && !it.Disabled {
			p.selectRow(index)
			return
		}
	}
}
func (p *popupWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmPaint:
		p.paint()
		return 0
	case 0x0014:
		return 1
	case wmMouseMove, wmLButtonUp, wmRButtonUp, wmLButtonDown:
		pos := mousePoint(lparam).Add(p.bounds.Min)
		root := p.d.menuRoot
		if root == nil {
			return 0
		}
		target, index := root.pointAt(pos)
		if target == nil {
			if msg == wmLButtonDown || msg == wmRButtonUp {
				p.d.closeMenu()
			}
			return 0
		}
		if msg == wmMouseMove {
			target.selectRow(index)
		}
		if msg == wmLButtonUp {
			target.activate(index)
		}
		return 0
	case wmKeyDown:
		leaf := p.d.menuRoot
		if leaf == nil {
			return 0
		}
		for leaf.child != nil && leaf.child.hover >= 0 {
			leaf = leaf.child
		}
		switch wparam {
		case 0x1B:
			p.d.closeMenu()
		case 0x26:
			leaf.step(-1)
		case 0x28:
			leaf.step(1)
		case 0x27:
			if leaf.hover >= 0 && len(leaf.items[leaf.hover].Children) > 0 {
				leaf.activate(leaf.hover)
			}
		case 0x0D, 0x20:
			leaf.activate(leaf.hover)
		case 0x25:
			if leaf.parent != nil {
				parent := leaf.parent
				leaf.close()
				parent.child = nil
				invalidate(parent.hwnd)
			}
		}
		return 0
	case 0x0006: // WM_ACTIVATE: close when another application takes focus.
		if wparam&0xffff == 0 && p.d.popups[lparam] == nil {
			p.d.closeMenu()
		}
		return 0
	case wmClose:
		p.d.closeMenu()
		return 0
	}
	return call(user32, "DefWindowProcW", p.hwnd, msg, wparam, lparam)
}

func (p *popupWindow) paint() {
	var ps paintStruct
	dc := call(user32, "BeginPaint", p.hwnd, ptr(&ps))
	defer call(user32, "EndPaint", p.hwnd, ptr(&ps))
	p.draw(dc)
}

func (p *popupWindow) draw(dc uintptr) {
	t := p.d.theme
	r := image.Rect(0, 0, p.bounds.Dx(), p.bounds.Dy())
	fill(dc, r, rgbRef(t.Background))
	roundPanel(dc, r, px(int(max(4, t.Radius)), p.scale), t.Background, t.Border)
	for i, it := range p.items {
		r := p.rows[i]
		if it.Label == "" {
			y := (r.Min.Y + r.Max.Y) / 2
			line(dc, image.Pt(r.Min.X+px(6, p.scale), y), image.Pt(r.Max.X-px(6, p.scale), y), t.Border, 1)
			continue
		}
		color := t.Text
		if it.Disabled {
			color = t.TextMuted
		} else if it.Danger {
			color = t.Danger
		}
		if i == p.hover && !it.Disabled {
			roundPanel(dc, r, px(4, p.scale), t.SurfaceHover, t.SurfaceHover)
		}
		size := px(17, p.scale)
		cy := (r.Min.Y + r.Max.Y - size) / 2
		ir := image.Rect(r.Min.X+px(7, p.scale), cy, r.Min.X+px(7, p.scale)+size, cy+size)
		if it.Checked {
			roundPanel(dc, ir, px(3, p.scale), t.Accent, t.Accent)
			icon(dc, "check", ir.Inset(px(2, p.scale)), t.AccentText)
		} else {
			icon(dc, it.Icon, ir, color)
		}
		label := image.Rect(r.Min.X+px(34, p.scale), r.Min.Y, r.Max.X-px(22, p.scale), r.Max.Y)
		textAligned(dc, it.Label, label, rgbRef(color), false)
		if it.Hint != "" {
			textAligned(dc, it.Hint, label, rgbRef(t.TextMuted), true)
		}
		if len(it.Children) > 0 {
			icon(dc, "right", image.Rect(r.Max.X-px(19, p.scale), cy, r.Max.X-px(3, p.scale), cy+size), color)
		}
	}
}
