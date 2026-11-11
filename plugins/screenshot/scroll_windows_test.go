//go:build windows

package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestLongStitchRenderedParagraphs(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	base := image.NewRGBA(image.Rect(0, 0, 800, 2200))
	b, err := newBitmap(base)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
	fill(dc, base.Bounds(), 0xffffff)
	for i := 0; i < 16; i++ {
		y := i * 130
		textAligned(dc, fmt.Sprintf("第 %02d 段：滚动长截图内容 %d", i+1, 1037+i*71), image.Rect(20, y+8, 760, y+38), 0, false)
		textAligned(dc, "这一行验证普通文本和空白背景的重叠匹配，不使用随机像素定位。", image.Rect(20, y+43, 780, y+73), 0, false)
		textAligned(dc, fmt.Sprintf("段落数据：%08d，保留真实文字、行间距与页面空白。", i*1234567+314159), image.Rect(20, y+78, 780, y+108), 0, false)
	}
	content, err := b.pixels()
	if err != nil {
		t.Fatal(err)
	}
	const height, header, footer = 500, 40, 30
	s := newStitcher(fixtureViewport(content, 0, height, header, footer))
	for _, offset := range []int{160, 320, 480, 640} {
		added, err := s.add(fixtureViewport(content, offset, height, header, footer))
		if err != nil || !added {
			t.Fatalf("text at %d: %v, %v", offset, added, err)
		}
	}
	result := s.result()
	for y := header; y < result.Rect.Dy()-footer; y++ {
		for x := 0; x < 800; x++ {
			if result.RGBAAt(x, y) != content.RGBAAt(x, y-header) {
				t.Fatalf("text seam mismatch at %d,%d", x, y)
			}
		}
	}
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "text-long-result.png"), result, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLongPanelPlacement(t *testing.T) {
	monitor := image.Rect(-1600, 0, 0, 1000)
	region := image.Rect(-1500, 200, -700, 800)
	r := longPanelBounds(region, monitor, image.Pt(600, 150), 10)
	if !r.In(monitor) || r.Overlaps(region) {
		t.Fatal("panel overlaps content despite free space", r)
	}
	r = longPanelBounds(monitor, monitor, image.Pt(600, 150), 10)
	if !r.In(monitor) {
		t.Fatal("full-screen panel not visible", r)
	}
}

func pumpLongMessages() {
	var msg message
	for call(user32, "PeekMessageW", ptr(&msg), 0, 0, 0, 1) != 0 {
		if msg.ID != 0x12 {
			call(user32, "TranslateMessage", ptr(&msg))
			call(user32, "DispatchMessageW", ptr(&msg))
		}
	}
}

// This controlled target responds to real WM_MOUSEWHEEL messages. It does not
// move the desktop mouse, send global input, or capture unrelated applications.
func TestNativeLongScrollCaptureAndPreview(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native long capture")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Logf("native long capture error: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	content := scrollFixture(320, 900)
	const height, header, footer = 240, 24, 18
	offset, wheelCount := 0, 0
	var frame *bitmap
	var fixture uintptr
	callback := windows.NewCallback(func(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
		switch msg {
		case 0x020a:
			wheelCount++
			offset = min(content.Rect.Dy()-(height-header-footer), max(0, offset-int(int16(wparam>>16))*80/120))
			frame.close()
			frame, _ = newBitmap(fixtureViewport(content, offset, height, header, footer))
			invalidate(hwnd)
			return 0
		case wmPaint:
			var ps paintStruct
			dc := call(user32, "BeginPaint", hwnd, ptr(&ps))
			if frame != nil {
				frame.draw(dc, image.Rect(0, 0, 320, height), image.Rect(0, 0, 320, height))
			}
			call(user32, "EndPaint", hwnd, ptr(&ps))
			return 0
		case 0x0317, 0x0318:
			if frame != nil {
				frame.draw(wparam, image.Rect(0, 0, 320, height), image.Rect(0, 0, 320, height))
				call(gdi32, "GdiFlush")
			}
			return 0
		case 0x0014:
			return 1
		}
		return call(user32, "DefWindowProcW", hwnd, msg, wparam, lparam)
	})
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: callback, Instance: call(kernel32, "GetModuleHandleW", 0), Name: u16("FLLongTestTarget")}
	if call(user32, "RegisterClassExW", ptr(&wc)) == 0 {
		t.Fatal("register fixture")
	}
	monitor := currentMonitor(cursorPoint())
	origin := monitor.Min.Add(image.Pt(80, 70))
	region := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(320, height))}
	frame, err = newBitmap(fixtureViewport(content, 0, height, header, footer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		frame.close()
		if fixture != 0 {
			call(user32, "DestroyWindow", fixture)
		}
	}()
	fixture = d.createWindow("FLLongTestTarget", "合成长截图测试窗口", wsExTopmost|wsExToolwindow, 0, region)
	if fixture == 0 {
		t.Fatal("failed to create native scroll target")
	}
	windowAt(fixture, region, false)
	call(user32, "UpdateWindow", fixture)
	call(dwmapi, "DwmFlush")
	// Print only our fixture's client area: desktop overlays cannot inject
	// personal pixels or make this message/worker/preview regression flaky.
	d.captureLong = func(image.Rectangle) (*image.RGBA, error) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		b, err := newBitmap(image.NewRGBA(image.Rect(0, 0, 320, height)))
		if err != nil {
			return nil, err
		}
		defer b.close()
		dc := call(gdi32, "CreateCompatibleDC", 0)
		old := call(gdi32, "SelectObject", dc, b.handle)
		defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
		if call(user32, "PrintWindow", fixture, dc, 1) == 0 {
			return nil, fmt.Errorf("controlled target rendering failed")
		}
		return b.pixels()
	}
	d.applyTheme(testTheme(true))
	frozenFirst := fixtureViewport(content, 0, height, header, footer)
	frozenFirst.SetRGBA(0, 0, color.RGBA{R: 237, G: 25, B: 71, A: 255})
	marks := []annotation{{kind: "rect", points: []image.Point{image.Pt(4, 4), image.Pt(18, 18)}, color: 0xee2255, width: 2}}
	expectedFirst, err := renderAnnotations(frozenFirst, marks)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real toolbar handoff. The source is another HWND in this
	// process; manual screen stitching must not require a particular root HWND.
	if err := d.openSelection(frozenFirst, region, false, previous); err != nil {
		t.Fatal(err)
	}
	d.selection.selected = frozenFirst.Bounds()
	d.selection.marks = marks
	d.selection.activateTool(8)
	if d.scrolling == nil {
		t.Fatal("long capture not opened")
	}
	s := d.scrolling
	await := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for !condition() {
			pumpLongMessages()
			if d.scrolling != s {
				t.Fatal("session closed unexpectedly")
			}
			if time.Now().After(deadline) {
				t.Fatalf("manual capture timed out: frames=%d height=%d status=%s", s.frames, s.height, s.status)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	await(func() bool { return s.thumbnail != nil && s.frames == 1 })
	assertLongPreviewPixels(t, s)
	originalBounds := s.bounds
	// Leaving the page still never sends wheel messages or completes capture.
	for end := time.Now().Add(400 * time.Millisecond); time.Now().Before(end); {
		pumpLongMessages()
		time.Sleep(5 * time.Millisecond)
	}
	if wheelCount != 0 || s.preview {
		t.Fatal("plugin scrolled or completed without user input")
	}
	s.activate(0) // pause
	call(user32, "SendMessageW", fixture, wmMouseWheel, uintptr(uint32(uint16(65416))<<16), 0)
	for end := time.Now().Add(350 * time.Millisecond); time.Now().Before(end); {
		pumpLongMessages()
		time.Sleep(5 * time.Millisecond)
	}
	if s.frames != 1 {
		t.Fatal("paused capture accepted a frame")
	}
	s.activate(0)
	await(func() bool { return s.height == height+offset })
	userWheels := 1
	for offset < content.Rect.Dy()-(height-header-footer) {
		call(user32, "SendMessageW", fixture, wmMouseWheel, uintptr(uint32(uint16(65416))<<16), 0)
		userWheels++
		if offset < content.Rect.Dy()-(height-header-footer) {
			await(func() bool { return s.height == height+offset })
		}
	}
	if wheelCount != userWheels || s.preview {
		t.Fatal("collection emitted input or finished without confirmation")
	}
	assertLongPreviewPixels(t, s)
	s.activate(1) // finish, keeping the small window in place
	await(func() bool { return s.preview })
	if s.bounds != originalBounds {
		t.Fatal("completion replaced the small preview with a large window")
	}
	assertLongPreviewPixels(t, s)
	if s.result.Rect.Dy() != content.Rect.Dy()+header+footer {
		t.Fatal("wrong native stitch height", s.result.Rect)
	}
	first, last := expectedFirst, fixtureViewport(content, offset, height, header, footer)
	for y := 0; y < s.result.Rect.Dy(); y++ {
		for x := 0; x < 320; x++ {
			want := first.RGBAAt(x, min(y, height-1))
			if y >= header && y < s.result.Rect.Dy()-footer {
				want = content.RGBAAt(x, y-header)
			}
			if y >= s.result.Rect.Dy()-footer {
				want = last.RGBAAt(x, y-offset)
			}
			if s.result.RGBAAt(x, y) != want {
				t.Fatalf("native seam mismatch at %d,%d", x, y)
			}
		}
	}
	// Force a shorter viewport on large monitors so the scrolling path is
	// exercised even when this small synthetic long image would fit at 1:1.
	s.bounds.Max.Y = min(s.bounds.Max.Y, s.bounds.Min.Y+px(420, uiScale(s.hwnd)))
	windowAt(s.hwnd, s.bounds, false)
	s.scrollBy(200)
	if s.offset == 0 {
		t.Fatal("preview did not scroll")
	}
	s.handle(wmKeyDown, 0x24, 0)
	if s.offset != 0 {
		t.Fatal("preview Home key did not reset")
	}
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "long-result.png"), s.result, false); err != nil {
			t.Fatal(err)
		}
		for _, dark := range []bool{true, false} {
			d.applyTheme(testTheme(dark))
			pumpLongMessages()
			call(user32, "UpdateWindow", s.hwnd)
			call(dwmapi, "DwmFlush")
			previewBitmap, err := newBitmap(image.NewRGBA(image.Rectangle{Max: s.bounds.Size()}))
			if err != nil {
				t.Fatal(err)
			}
			dc := call(gdi32, "CreateCompatibleDC", 0)
			old := call(gdi32, "SelectObject", dc, previewBitmap.handle)
			s.draw(dc)
			preview, err := previewBitmap.pixels()
			call(gdi32, "SelectObject", dc, old)
			call(gdi32, "DeleteDC", dc)
			previewBitmap.close()
			if err != nil {
				t.Fatal(err)
			}
			name := "long-preview-light.png"
			if dark {
				name = "long-preview-dark.png"
			}
			if err := savePNG(filepath.Join(dir, name), preview, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := append([]byte(nil), s.result.Pix...)
	s.resume()
	s.activate(1)
	deadline := time.Now().Add(3 * time.Second)
	for !s.preview {
		pumpLongMessages()
		if time.Now().After(deadline) {
			t.Fatal("resume did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if !bytes.Equal(before, s.result.Pix) {
		t.Fatal("resuming without a new frame duplicated content")
	}
	prepared := s.bitmap
	s.activate(2)
	if d.scrolling != nil || len(d.pins) != 1 {
		t.Fatal("long result did not become a pin")
	}
	for _, pin := range d.pins {
		if pin.bitmap != prepared || s.bitmap != nil {
			t.Fatal("preview bitmap ownership was not transferred")
		}
		if pin.image.Rect.Dy() != height+offset || !pin.state.Bounds.In(monitor) {
			t.Fatal("long pin lost original pixels or did not fit monitor")
		}
		pin.close()
	}
	// Cancel in the middle of temporary pin hiding, not only before the
	// worker starts: cancellation must restore the user's existing images.
	mask, err := d.openPin(crop(content, image.Rect(0, 0, 40, 40)), image.Rect(region.Min.X+8, region.Min.Y+8, region.Min.X+48, region.Min.Y+48))
	if err != nil {
		t.Fatal(err)
	}
	d.preferences.IncludePins = false
	d.openLongCapture(region, uiScale(fixture), longSeed{first: fixtureViewport(content, offset, height, header, footer)})
	s = d.scrolling
	deadline = time.Now().Add(2 * time.Second)
	for !s.capturePinsHidden {
		pumpLongMessages()
		if time.Now().After(deadline) {
			t.Fatal("capture never reached temporary hiding")
		}
		time.Sleep(time.Millisecond)
	}
	s.close(true)
	if call(user32, "IsWindowVisible", mask.hwnd) == 0 {
		t.Fatal("cancellation left an existing pin hidden")
	}
	mask.close()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	wheelsBefore := wheelCount
	if _, err := s.capture(cancelled); err != context.Canceled {
		t.Fatal("cancelled capture accepted", err)
	}
	pumpLongMessages()
	if wheelCount != wheelsBefore {
		t.Fatal("wheel sent after cancellation")
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) && d.active.Load() != 0 {
		pumpLongMessages()
		time.Sleep(time.Millisecond)
	}
	pumpLongMessages()
	if d.scrolling != nil {
		t.Fatal("cancelled capture reopened")
	}
	if active, _ := (handler{d}).HasActivity(context.Background()); active {
		t.Fatal("cancelled session kept activity")
	}
}

func assertLongPreviewPixels(t *testing.T, s *scrollWindow) {
	t.Helper()
	invalidate(s.hwnd)
	call(user32, "UpdateWindow", s.hwnd)
	buffer := s.paintBuffer
	if buffer.dc == 0 || buffer.bitmap == 0 {
		t.Fatal("preview did not paint a back buffer")
	}
	b, err := newBitmap(image.NewRGBA(image.Rectangle{Max: s.bounds.Size()}))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
	if call(gdi32, "BitBlt", dc, 0, 0, b.width, b.height, buffer.dc, 0, 0, srccopy) == 0 {
		t.Fatal("preview buffer copy failed")
	}
	img, err := b.pixels()
	if err != nil {
		t.Fatal(err)
	}
	colors := map[color.RGBA]bool{}
	v := s.view().Intersect(img.Bounds())
	for y := v.Min.Y; y < v.Max.Y; y += 3 {
		for x := v.Min.X; x < v.Max.X; x += 3 {
			colors[img.RGBAAt(x, y)] = true
		}
	}
	if len(colors) < 32 {
		t.Fatalf("preview has only background/control colors: %d", len(colors))
	}
	// Hover updates reuse the same back buffer and never resize the window.
	for _, r := range s.buttons() {
		p := r.Min.Add(image.Pt(2, 2))
		s.handle(wmMouseMove, 0, uintptr(uint32(uint16(p.X))|uint32(uint16(p.Y))<<16))
		call(user32, "UpdateWindow", s.hwnd)
	}
	if buffer.bitmap != s.paintBuffer.bitmap {
		t.Fatal("hover recreated the preview buffer")
	}
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		name := "manual-live.png"
		if s.preview {
			name = "manual-complete.png"
		}
		if err := savePNG(filepath.Join(dir, name), img, false); err != nil {
			t.Fatal(err)
		}
	}
}

// Verify production BitBlt, with the preview deliberately overlapping a fully
// synthetic source. No source-render hook can mask self-capture regressions.
func TestNativeLongPreviewCaptureExclusion(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in desktop composition")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("native capture: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	monitor := currentMonitor(cursorPoint())
	origin := monitor.Min.Add(image.Pt(20, 20))
	img := scrollFixture(min(960, monitor.Dx()-40), min(760, monitor.Dy()-40))
	bounds := img.Bounds().Add(origin)
	base, err := d.openPin(img, bounds)
	if err != nil {
		t.Fatal(err)
	}
	base.state.Shadow = false
	base.syncWindow()
	d.preferences.IncludePins = true
	d.startLongCapture(bounds, uiScale(base.hwnd), longSeed{first: img})
	s := d.scrolling
	if s == nil {
		t.Fatal("screen crop rejected because it was not a foreign HWND")
	}
	if !s.excludedFromCapture {
		t.Skip("OS does not support capture exclusion")
	}
	s.bounds = s.bounds.Add(bounds.Min.Add(image.Pt(15, 15)).Sub(s.bounds.Min))
	windowAt(s.hwnd, s.bounds, false)
	deadline := time.Now().Add(2 * time.Second)
	for {
		pumpLongMessages()
		call(user32, "UpdateWindow", base.hwnd)
		call(user32, "UpdateWindow", s.hwnd)
		call(dwmapi, "DwmFlush")
		shot, err := s.capture(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(shot.Pix, img.Pix) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture included the preview or lost source pixels under its overlapping bounds")
		}
		time.Sleep(15 * time.Millisecond)
	}
	if call(user32, "IsWindowVisible", s.hwnd) == 0 {
		t.Fatal("capture hid the live preview")
	}
	s.close(true)
}
