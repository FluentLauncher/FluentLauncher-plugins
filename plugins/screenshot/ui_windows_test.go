//go:build windows

package main

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func testTheme(dark bool) pluginsdk.Theme {
	if dark {
		return pluginsdk.Theme{Dark: true, Background: 0x18181b, Surface: 0x27272a, SurfaceHover: 0x323236, Border: 0x3f3f46, Text: 0xf4f4f5, TextMuted: 0xa1a1aa, Accent: 0x3b82f6, AccentText: 0xffffff, Danger: 0xef4444, Inverse: 0xf4f4f5, InverseText: 0x18181b, Radius: 8, Spacing: 4}
	}
	return pluginsdk.Theme{Background: 0xffffff, Surface: 0xf4f4f5, SurfaceHover: 0xe9e9ec, Border: 0xd9d9de, Text: 0x18181b, TextMuted: 0x71717a, Accent: 0x2563eb, AccentText: 0xffffff, Danger: 0xdc2626, Inverse: 0x18181b, InverseText: 0xffffff, Radius: 8, Spacing: 4}
}

func TestLensPlacementAndMenuActions(t *testing.T) {
	bounds := image.Rect(0, 0, 800, 600)
	for _, cursor := range []image.Point{image.Pt(0, 0), image.Pt(799, 599), image.Pt(790, 4), image.Pt(5, 590)} {
		r := lensBounds(cursor, image.Pt(240, 220), bounds, 20)
		if !r.In(bounds) || r.Dx() != 240 || r.Dy() != 220 || cursor.In(r) {
			t.Fatalf("lens placement at %v: %v", cursor, r)
		}
	}
	negative := image.Rect(-1800, -200, 0, 800)
	if r := lensBounds(image.Pt(-1799, -199), image.Pt(240, 220), negative, 20); !r.In(negative) {
		t.Fatal("negative monitor", r)
	}
	p := &pinWindow{state: pinState{Original: image.Rect(0, 0, 100, 60), Bounds: image.Rect(0, 0, 100, 60), Opacity: 100}}
	var hasOpacity, hasRotate, hasMirror bool
	for _, it := range p.menuItems() {
		switch it.Label {
		case "不透明度":
			hasOpacity = len(it.Children) == 6
		case "旋转":
			hasRotate = len(it.Children) == 3
		case "镜像":
			hasMirror = len(it.Children) == 2
		}
	}
	if !hasOpacity || !hasRotate || !hasMirror {
		t.Fatal("reference actions missing")
	}
}

// Called from the existing locked-thread native test: all imagery is synthetic.
func exerciseThemedUI(t *testing.T, d *desktop, p *pinWindow, s *selectionWindow) {
	t.Helper()
	original := d.theme
	defer d.applyTheme(original)
	selected := s.selected
	defer func() { s.selected = selected }()
	s.selected = image.Rect(30, 40, 430, 230)
	dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR")
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	save := func(name string, b *bitmap) {
		if dir == "" {
			return
		}
		img, err := b.pixels()
		if err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, name), img, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, dark := range []bool{false, true} {
		palette := testTheme(dark)
		d.applyTheme(palette)
		name := "light"
		if dark {
			name = "dark"
		}
		s.cursor = image.Pt(220, 120)
		s.dragMode = 0
		call(user32, "UpdateWindow", s.hwnd)
		call(dwmapi, "DwmFlush")
		if dir != "" {
			shot, err := captureScreen(s.desktopBounds)
			if err != nil {
				t.Fatal(err)
			}
			if err := savePNG(filepath.Join(dir, "selection-"+name+".png"), shot, false); err != nil {
				t.Fatal(err)
			}
		}
		s.cursor = image.Pt(300, 210)
		s.dragMode = 1
		invalidate(s.hwnd)
		call(user32, "UpdateWindow", s.hwnd)
		call(dwmapi, "DwmFlush")
		if dir != "" {
			shot, err := captureScreen(s.desktopBounds)
			if err != nil {
				t.Fatal(err)
			}
			if err := savePNG(filepath.Join(dir, "magnifier-"+name+".png"), shot, false); err != nil {
				t.Fatal(err)
			}
		}
		s.dragMode = 0
		p.menu(p.state.Bounds.Min)
		root := d.menuRoot
		if root == nil {
			t.Fatal("themed menu missing")
		}
		// Render the exact same menu draw function offscreen; avoid capturing
		// unrelated desktop areas when the menu exceeds the synthetic fixture.
		blank := image.NewRGBA(image.Rect(0, 0, root.bounds.Dx(), root.bounds.Dy()))
		b, err := newBitmap(blank)
		if err != nil {
			t.Fatal(err)
		}
		dc := call(gdi32, "CreateCompatibleDC", 0)
		old := call(gdi32, "SelectObject", dc, b.handle)
		root.draw(dc)
		save("menu-"+name+".png", b)
		call(gdi32, "SelectObject", dc, old)
		call(gdi32, "DeleteDC", dc)
		b.close()
		root.selectRow(8) // opacity submenu
		if root.child == nil {
			t.Fatal("submenu did not open")
		}
		root.child.activate(3) // 50 percent
		if p.state.Opacity != 50 || d.menuRoot != nil {
			t.Fatal("opacity action did not apply")
		}
		p.state.Opacity = 100
		p.syncWindow()
	}
	// A custom launcher palette must replace the light/dark defaults too.
	custom := testTheme(true)
	custom.Background = 0x213a48
	custom.Accent = 0x00bba0
	d.applyTheme(custom)
	p.menu(p.state.Bounds.Min)
	if d.theme != custom || d.menuRoot == nil {
		t.Fatal("custom theme not applied")
	}
	d.closeMenu()
	p.menu(p.state.Bounds.Min)
	root := d.menuRoot
	root.handle(wmKeyDown, 0x28, 0)
	root.handle(wmKeyDown, 0x27, 0)
	if root.child == nil || root.child.hover != 0 {
		t.Fatal("keyboard did not enter submenu")
	}
	root.handle(wmKeyDown, 0x25, 0)
	if root.child != nil {
		t.Fatal("keyboard did not return from submenu")
	}
	root.selectRow(2)
	topmost := p.state.Topmost
	root.handle(wmKeyDown, 0x27, 0)
	if d.menuRoot == nil || p.state.Topmost != topmost {
		t.Fatal("Right activated an item without children")
	}
	root.handle(wmKeyDown, 0x1b, 0)
	if d.menuRoot != nil {
		t.Fatal("Escape did not dismiss menu")
	}
	dataPath := filepath.Join(t.TempDir(), "drag.png")
	if err := savePNG(dataPath, p.rendered, true); err != nil {
		t.Fatal(err)
	}
	object, err := shellDataObject(dataPath)
	if err != nil {
		t.Fatal("native drag data object", err)
	}
	comRelease(object)
	waitTransform := func() {
		deadline := time.Now().Add(3 * time.Second)
		for p.busy {
			var msg message
			for call(user32, "PeekMessageW", ptr(&msg), 0, 0, 0, 1) != 0 {
				call(user32, "TranslateMessage", ptr(&msg))
				call(user32, "DispatchMessageW", ptr(&msg))
			}
			if time.Now().After(deadline) {
				t.Fatal("image operation did not complete")
			}
			time.Sleep(time.Millisecond)
		}
	}
	p.applyMenu(31)
	waitTransform()
	if p.state.Angle != 1 || p.rendered.Rect.Dx() != p.image.Rect.Dy() {
		t.Fatal("native rotation failed")
	}
	p.applyMenu(42)
	waitTransform()
	if !p.state.MirrorY {
		t.Fatal("native mirror failed")
	}
	p.applyMenu(11)
	waitTransform()
	if p.state.Angle != 0 || p.state.Opacity != 100 || p.state.Bounds != p.state.Original || !bytes.Equal(p.rendered.Pix, p.image.Pix) {
		t.Fatal("reset did not restore source")
	}
	p.applyMenu(10)
	exstyle := call(user32, "GetWindowLongPtrW", p.hwnd, coord(-20))
	if exstyle&0x40000 == 0 || exstyle&wsExToolwindow != 0 {
		t.Fatal("taskbar window styles not applied")
	}
	p.applyMenu(10)
	p.applyMenu(9)
	if call(user32, "GetWindowLongPtrW", p.hwnd, coord(-20))&wsExTopmost != 0 {
		t.Fatal("topmost could not be disabled")
	}
	p.applyMenu(9)
}
