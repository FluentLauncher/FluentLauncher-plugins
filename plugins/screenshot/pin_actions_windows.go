//go:build windows

package main

import (
	"fmt"
	"image"
)

func (p *pinWindow) menuItems() []menuItem {
	var opacity, sizes []menuItem
	for _, percent := range []int{100, 90, 75, 50, 25, 10} {
		opacity = append(opacity, menuItem{ID: 100 + percent, Label: fmt.Sprintf("%d%%", percent), Checked: p.state.Opacity == percent})
	}
	for _, percent := range []int{25, 50, 75, 100, 125, 150, 200} {
		sizes = append(sizes, menuItem{ID: 200 + percent, Label: fmt.Sprintf("%d%%", percent), Checked: p.state.Bounds.Dx()*100/p.state.naturalSize().X == percent, Disabled: p.state.Locked})
	}
	return []menuItem{
		{Label: "图片操作", Icon: "file", Children: []menuItem{{ID: 1, Label: "复制图片", Icon: "copy", Hint: "Ctrl+C"}, {ID: 2, Label: "另存为…", Icon: "save", Hint: "Ctrl+S"}, {ID: 12, Label: "作为文件打开", Icon: "file"}}},
		{},
		{ID: 9, Label: "置顶", Icon: "pin", Checked: p.state.Topmost},
		{ID: 10, Label: "在任务栏显示", Icon: "taskbar", Checked: p.state.Taskbar},
		{ID: 3, Label: "显示阴影", Icon: "shadow", Checked: p.state.Shadow},
		{ID: 4, Label: "锁定位置", Icon: "pin", Checked: p.state.Locked},
		{},
		{ID: 11, Label: "重置", Icon: "reset"},
		{Label: "不透明度", Icon: "opacity", Children: opacity},
		{Label: "大小", Icon: "size", Children: append(sizes, menuItem{}, menuItem{ID: 5, Label: "恢复原始大小", Disabled: p.state.Locked}, menuItem{ID: 6, Label: "回到原始位置", Disabled: p.state.Locked})},
		{},
		{Label: "旋转", Icon: "rotate", Disabled: p.state.Locked, Children: []menuItem{{ID: 31, Label: "顺时针 90°"}, {ID: 32, Label: "逆时针 90°"}, {ID: 33, Label: "旋转 180°"}}},
		{Label: "镜像", Icon: "mirror", Disabled: p.state.Locked, Children: []menuItem{{ID: 41, Label: "水平镜像", Checked: p.state.MirrorX}, {ID: 42, Label: "垂直镜像", Checked: p.state.MirrorY}}},
		{},
		{ID: 1, Label: "复制图片", Icon: "copy", Hint: "Ctrl+C"},
		{ID: 2, Label: "另存为…", Icon: "save", Hint: "Ctrl+S"},
		{ID: 12, Label: "作为文件打开", Icon: "file"},
		{},
		{ID: 13, Label: "操作提示", Icon: "info"},
		{ID: 7, Label: "隐藏当前贴图", Icon: "hide"},
		{ID: 8, Label: "关闭", Icon: "close", Hint: "Esc", Danger: true},
	}
}

func (p *pinWindow) transform(update func(*pinState)) {
	if p.busy {
		return
	}
	next := p.state
	update(&next)
	p.busy = true
	p.d.active.Add(1)
	source := p.image
	go func() {
		img := transformed(source, next)
		bmp, err := newBitmap(img)
		postErr := p.d.post(func() {
			defer p.d.active.Add(-1)
			p.busy = false
			if p.d.pins[p.hwnd] != p {
				bmp.close()
				return
			}
			if err != nil {
				p.d.report(p.hwnd, err)
				return
			}
			p.bitmap.close()
			p.bitmap = bmp
			p.rendered = img
			p.state = next
			p.syncWindow()
		})
		if postErr != nil {
			bmp.close()
		}
	}()
}

func posForHelp(p *pinWindow) image.Point { return p.state.Bounds.Min.Add(image.Pt(20, 20)) }
func (p *pinWindow) help(pos image.Point) {
	p.d.showMenu(p.hwnd, pos, []menuItem{
		{Label: "方向键：移动 1 像素", Disabled: true},
		{Label: "Shift＋方向键：移动 10 像素", Disabled: true},
		{Label: "拖动移动，滚轮缩放", Disabled: true},
		{Label: "Ctrl 拖动：拖出 PNG 文件", Disabled: true},
		{Label: "Ctrl+C 复制，Ctrl+S 另存为", Disabled: true},
		{Label: "Esc 关闭当前贴图", Disabled: true},
		{}, {ID: 1, Label: "知道了", Icon: "check"},
	}, func(int) {})
}
