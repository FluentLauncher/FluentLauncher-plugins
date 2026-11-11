//go:build windows

package main

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestAnnotationOutputAndHistory(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	source := scrollFixture(180, 140)
	for _, kind := range []string{"rect", "ellipse", "arrow", "pen", "mosaic", "text"} {
		m := annotation{kind: kind, points: []image.Point{image.Pt(20, 20), image.Pt(110, 100)}, color: 0xe23643, width: 3, text: "中文标注"}
		out, err := renderAnnotations(source, []annotation{m})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(out.Pix, source.Pix) {
			t.Fatalf("%s changed no pixels", kind)
		}
		if out.Bounds() != source.Bounds() {
			t.Fatal("annotation resized output")
		}
		if out.RGBAAt(0, 0) != source.RGBAAt(0, 0) {
			t.Fatal("annotation damaged untouched pixel")
		}
	}
	d := &desktop{theme: testTheme(false)}
	s := &selectionWindow{d: d, marks: []annotation{{kind: "rect", points: []image.Point{image.Pt(30, 40), image.Pt(100, 100)}}}, selected: image.Rect(10, 20, 150, 130)}
	s.undoMark(false)
	if len(s.marks) != 0 || len(s.redo) != 1 {
		t.Fatal("undo lost history")
	}
	s.undoMark(true)
	if len(s.marks) != 1 || len(s.redo) != 0 {
		t.Fatal("redo lost mark")
	}
	if s.annotationMarks()[0].points[0] != image.Pt(20, 20) || s.marks[0].points[0] != image.Pt(30, 40) {
		t.Fatal("crop coordinates modified the source mark")
	}
	for _, tool := range captureTools {
		if tool.id == "scroll" {
			return
		}
	}
	t.Fatal("ordinary toolbar has no long capture action")
}

func TestNativeCaptureToolbarAndMaterial(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native UI check")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("native UI error: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	img := image.NewRGBA(image.Rect(0, 0, 760, 440))
	for y := 0; y < 440; y++ {
		for x := 0; x < 760; x++ {
			c := color.RGBA{R: 180, G: 210, B: 230, A: 255}
			if (x/12+y/12)%2 == 0 {
				c = color.RGBA{R: 245, G: 220, B: 185, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	origin := currentMonitor(cursorPoint()).Min.Add(image.Pt(80, 80))
	if err := d.openSelection(img, img.Bounds().Add(origin), false, previous); err != nil {
		t.Fatal(err)
	}
	s := d.selection
	s.selected = image.Rect(20, 40, 720, 330)
	pos := func(p image.Point) uintptr { return uintptr(uint32(uint16(p.X)) | uint32(uint16(p.Y))<<16) }
	// Exercise the same mouse handlers as the shipped toolbar/drawing path.
	s.activateTool(0)
	s.handle(wmLButtonDown, 0, pos(image.Pt(80, 85)))
	s.handle(wmMouseMove, 0, pos(image.Pt(240, 180)))
	s.handle(wmLButtonUp, 0, pos(image.Pt(240, 180)))
	if len(s.marks) != 1 {
		t.Fatal("drawing did not create one mark")
	}
	s.undoMark(false)
	s.undoMark(true)
	s.tool = "text"
	s.startText(image.Pt(280, 90))
	if s.edit == 0 {
		t.Fatal("native IME text editor missing")
	}
	call(user32, "SetWindowTextW", s.edit, ptr(u16("中文文字标注")))
	call(user32, "SendMessageW", s.edit, wmKeyDown, 0x0d, 0)
	if s.edit != 0 || len(s.marks) != 2 || s.marks[1].text != "中文文字标注" {
		t.Fatal("text commit failed")
	}
	s.startText(image.Pt(280, 150))
	call(user32, "SetWindowTextW", s.edit, ptr(u16("放弃")))
	call(user32, "SendMessageW", s.edit, wmKeyDown, 0x1b, 0)
	if len(s.marks) != 2 {
		t.Fatal("cancelled text was retained")
	}
	if h := s.toolbar().Dy(); h > px(64, uiScale(s.hwnd)) {
		t.Fatal("toolbar too tall", h)
	}
	// A cancelled drag does not keep a partial mark.
	s.tool = "pen"
	s.handle(wmLButtonDown, 0, pos(image.Pt(90, 220)))
	call(user32, "ReleaseCapture")
	if s.draft != nil {
		t.Fatal("capture loss retained draft")
	}
	s.tool = ""
	s.toolbarHover = -1
	invalidate(s.hwnd)
	call(user32, "UpdateWindow", s.hwnd)
	selectionBuffer := s.paintBuffer.bitmap
	if selectionBuffer == 0 {
		t.Fatal("selection has no back buffer")
	}
	for _, r := range s.toolbarButtons() {
		s.handle(wmMouseMove, 0, pos(r.Min.Add(image.Pt(2, 2))))
		call(user32, "UpdateWindow", s.hwnd)
	}
	if s.paintBuffer.bitmap != selectionBuffer {
		t.Fatal("toolbar hover recreated the full selection buffer")
	}
	s.toolbarHover = -1
	s.handle(wmKeyDown, 0x09, 0)
	if s.toolbarFocus < 0 {
		t.Fatal("Tab did not focus the toolbar")
	}
	s.handle(wmKeyDown, 0x0d, 0)
	if s.tool != "rect" {
		t.Fatal("keyboard did not activate the focused drawing tool")
	}
	s.handle(wmKeyDown, 0x1b, 0)
	s.toolbarFocus = -1
	for _, dark := range []bool{false, true} {
		palette := testTheme(dark)
		opacity := float32(0.55)
		palette.Opacity = &opacity
		palette.Backdrop = "acrylic"
		d.applyTheme(palette)
		call(user32, "UpdateWindow", s.hwnd)
		call(dwmapi, "DwmFlush")
		d.showMenu(s.hwnd, origin.Add(image.Pt(460, 80)), []menuItem{{ID: 1, Label: "复制", Icon: "copy", Hint: "Ctrl+C"}, {ID: 2, Label: "另存为", Icon: "save"}, {ID: 3, Label: "显示阴影", Icon: "shadow", Checked: true}, {ID: 4, Label: "关闭", Icon: "close", Danger: true}}, func(int) {})
		p := d.menuRoot
		if p == nil {
			t.Fatal("material menu missing")
		}
		b, err := p.layeredSurface()
		if err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, b.width*b.height*4)
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), b.address, &raw[0], uintptr(len(raw)), nil); err != nil {
			t.Fatal(err)
		}
		b.close()
		translucent, opaque := 0, 0
		for i := 3; i < len(raw); i += 4 {
			if raw[i] > 0 && raw[i] < 255 {
				translucent++
			}
			if raw[i] == 255 {
				opaque++
			}
		}
		if p.material != "opaque" && (translucent == 0 || opaque == 0) {
			t.Fatalf("background/foreground alpha not separated: %d/%d", translucent, opaque)
		}
		for end := time.Now().Add(120 * time.Millisecond); time.Now().Before(end); {
			pumpLongMessages()
			time.Sleep(5 * time.Millisecond)
		}
		call(user32, "UpdateWindow", p.hwnd)
		call(dwmapi, "DwmFlush")
		if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			shot, err := captureScreen(s.desktopBounds)
			if err != nil {
				t.Fatal(err)
			}
			name := "light"
			if dark {
				name = "dark"
			}
			if err := savePNG(filepath.Join(dir, "toolbar-material-"+name+".png"), shot, false); err != nil {
				t.Fatal(err)
			}
		}
		d.closeMenu()
		// Appearance updates also clear material state, without replacing the menu.
		d.showMenu(s.hwnd, origin.Add(image.Pt(460, 80)), []menuItem{{ID: 1, Label: "测试", Icon: "copy"}}, func(int) {})
		palette.Backdrop = "none"
		d.applyTheme(palette)
		if d.menuRoot.material != "none" {
			t.Fatal("old blur not cleared")
		}
		d.closeMenu()
	}
	mica := testTheme(true)
	mica.Backdrop = "mica"
	d.applyTheme(mica)
	d.showMenu(s.hwnd, origin.Add(image.Pt(460, 80)), []menuItem{{ID: 1, Label: "云母主题菜单", Icon: "copy"}, {ID: 2, Label: "原生系统材质", Icon: "info"}}, func(int) {})
	for end := time.Now().Add(150 * time.Millisecond); time.Now().Before(end); {
		pumpLongMessages()
		time.Sleep(5 * time.Millisecond)
	}
	call(dwmapi, "DwmFlush")
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		shot, err := captureScreen(s.desktopBounds)
		if err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "toolbar-material-mica.png"), shot, false); err != nil {
			t.Fatal(err)
		}
	}
	d.closeMenu()
	// An invalid long region preserves the frozen selection and its annotations.
	s.selected = image.Rect(0, 0, 30, 30)
	d.onError = func(error) {}
	s.output("scroll")
	if d.selection == nil || len(d.selection.marks) != 2 {
		t.Fatal("failed handoff discarded existing capture")
	}
}
