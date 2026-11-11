//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Read the actual presented back buffer and native window region. Transparent
// gaps in the resulting PNG are the exact holes used for desktop hit testing.
func previewSurface(t *testing.T, s *scrollWindow) *image.RGBA {
	t.Helper()
	invalidate(s.hwnd)
	call(user32, "UpdateWindow", s.hwnd)
	b, err := newBitmap(image.NewRGBA(image.Rectangle{Max: s.bounds.Size()}))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
	if call(gdi32, "BitBlt", dc, 0, 0, b.width, b.height, s.paintBuffer.dc, 0, 0, srccopy) == 0 {
		t.Fatal("preview buffer not presented")
	}
	img, err := b.pixels()
	if err != nil {
		t.Fatal(err)
	}
	rgn := call(gdi32, "CreateRectRgn", 0, 0, 0, 0)
	defer call(gdi32, "DeleteObject", rgn)
	if call(user32, "GetWindowRgn", s.hwnd, rgn) == 0 {
		t.Fatal("preview has no minimal window region")
	}
	length := call(gdi32, "GetRegionData", rgn, 0, 0)
	if length < 32 {
		t.Fatal("invalid native region")
	}
	data := make([]byte, int(length))
	if call(gdi32, "GetRegionData", rgn, length, ptr(&data[0])) == 0 {
		t.Fatal("region data unavailable")
	}
	out := image.NewRGBA(img.Bounds())
	count := int(binary.LittleEndian.Uint32(data[8:12]))
	for i := 0; i < count; i++ {
		p := data[32+i*16 : 32+(i+1)*16]
		r := image.Rect(int(int32(binary.LittleEndian.Uint32(p[0:4]))), int(int32(binary.LittleEndian.Uint32(p[4:8]))), int(int32(binary.LittleEndian.Uint32(p[8:12]))), int(int32(binary.LittleEndian.Uint32(p[12:16]))))
		draw.Draw(out, r, img, r.Min, draw.Src)
	}
	return out
}

func TestNativeLongMinimalLayoutAndSelectionFrame(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native layout QA")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("layout native error: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	monitor := currentMonitor(cursorPoint())
	origin := monitor.Min.Add(image.Pt(20, 20))
	for _, dark := range []bool{false, true} {
		theme := testTheme(dark)
		d.applyTheme(theme)
		baseSize := image.Pt(min(1200, monitor.Dx()-40), min(760, monitor.Dy()-40))
		regionLocal := image.Rect(24, 32, 24+min(620, baseSize.X-px(280, uiScale(d.controller))-60), min(452, baseSize.Y-40))
		if regionLocal.Dx() < 200 {
			t.Skip("display too small for side-by-side visual fixture")
		}
		b, err := newBitmap(image.NewRGBA(image.Rectangle{Max: baseSize}))
		if err != nil {
			t.Fatal(err)
		}
		dc := call(gdi32, "CreateCompatibleDC", 0)
		old := call(gdi32, "SelectObject", dc, b.handle)
		fill(dc, image.Rectangle{Max: baseSize}, rgbRef(theme.Surface))
		fill(dc, regionLocal, rgbRef(theme.Background))
		textAligned(dc, "截图内容示例 · 滚动阅读", image.Rect(44, 48, 600, 82), rgbRef(theme.Text), false)
		for i := 0; i < 8; i++ {
			y := 100 + i*40
			textAligned(dc, fmt.Sprintf("%02d  原选区保留边界，预览只保留图片与细线。", i+1), image.Rect(44, y, regionLocal.Max.X-12, y+30), rgbRef(theme.Text), false)
		}
		baseImage, err := b.pixels()
		call(gdi32, "SelectObject", dc, old)
		call(gdi32, "DeleteDC", dc)
		b.close()
		if err != nil {
			t.Fatal(err)
		}
		base, err := d.openPin(baseImage, image.Rectangle{Min: origin, Max: origin.Add(baseSize)})
		if err != nil {
			t.Fatal(err)
		}
		base.state.Shadow = false
		base.syncWindow()
		first := crop(baseImage, regionLocal)
		d.captureLong = func(image.Rectangle) (*image.RGBA, error) { return first, nil }
		d.startLongCapture(regionLocal.Add(origin), uiScale(base.hwnd), longSeed{first: first})
		s := d.scrolling
		if s == nil {
			t.Fatal("no preview")
		}
		await := func(done func() bool) {
			t.Helper()
			deadline := time.Now().Add(3 * time.Second)
			for !done() {
				pumpLongMessages()
				if time.Now().After(deadline) {
					t.Fatal("layout fixture timed out")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		await(func() bool { return s.thumbnail != nil })
		initial := s.bounds
		outline := s.outline
		if outline == 0 || call(user32, "IsWindowVisible", outline) == 0 {
			t.Fatal("source crop has no visible outline")
		}
		style := call(user32, "GetWindowLongPtrW", outline, coord(-20))
		if style&(wsExTransparent|wsExNoactivate) != (wsExTransparent | wsExNoactivate) {
			t.Fatal("outline intercepts input")
		}
		rgn := call(gdi32, "CreateRectRgn", 0, 0, 0, 0)
		call(user32, "GetWindowRgn", outline, rgn)
		if call(gdi32, "PtInRegion", rgn, s.region.Dx()/2, s.region.Dy()/2) != 0 || call(gdi32, "PtInRegion", rgn, 0, 20) == 0 {
			t.Fatal("outline is not a hollow ring")
		}
		call(gdi32, "DeleteObject", rgn)
		for _, button := range s.buttons() {
			if button.Min.Y < s.toolbar().Min.Y || button.Overlaps(s.imageFrame()) {
				t.Fatal("buttons overlap the image or remain at top")
			}
		}
		if s.buttonActions()[len(s.buttonActions())-1] != 1 {
			t.Fatal("finish is not at far right")
		}
		name := "light"
		if dark {
			name = "dark"
		}
		save := func(state string) {
			surface := previewSurface(t, s)
			if surface.RGBAAt(0, 0).A != 0 {
				t.Fatal("preview still has a full background shell")
			}
			if surface.RGBAAt(s.imageDestination().Min.X+2, s.imageDestination().Min.Y+2).A == 0 {
				t.Fatal("image was clipped from minimal frame")
			}
			if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				// Compose only synthetic fixture pixels with the actual native
				// preview buffer, never personal desktop content.
				canvas, err := newBitmap(baseImage)
				if err != nil {
					t.Fatal(err)
				}
				dc := call(gdi32, "CreateCompatibleDC", 0)
				old := call(gdi32, "SelectObject", dc, canvas.handle)
				s.drawSourceFrame(dc, regionLocal.Inset(-sourceFrameWidth))
				shot, err := canvas.pixels()
				call(gdi32, "SelectObject", dc, old)
				call(gdi32, "DeleteDC", dc)
				canvas.close()
				if err != nil {
					t.Fatal(err)
				}
				pos := s.bounds.Min.Sub(origin)
				draw.Draw(shot, image.Rectangle{Min: pos, Max: pos.Add(surface.Rect.Size())}, surface, image.Point{}, draw.Over)
				if err := savePNG(filepath.Join(dir, "layout-"+name+"-"+state+".png"), shot, false); err != nil {
					t.Fatal(err)
				}
			}
		}
		save("live")
		s.activate(0)
		save("paused")
		s.activate(0)
		s.activate(1)
		await(func() bool { return s.preview })
		if s.bounds != initial || call(user32, "IsWindowVisible", outline) == 0 {
			t.Fatal("completion moved UI or lost crop reference")
		}
		save("complete")
		s.handle(wmKeyDown, 0x09, 0)
		if s.focus != 1 {
			t.Fatal("keyboard focus order no longer follows the bottom toolbar")
		}
		main := s.hwnd
		s.handle(wmKeyDown, 0x1b, 0)
		if call(user32, "IsWindow", outline) != 0 || call(user32, "IsWindow", main) != 0 {
			t.Fatal("closing left orphan capture UI")
		}
		base.close()
	}
}
