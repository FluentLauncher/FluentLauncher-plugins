//go:build windows

package main

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func TestDelayCommandsAndPlacement(t *testing.T) {
	h := handler{&desktop{}}
	rows, err := h.Query(context.Background(), pluginsdk.Query{Text: "延时", MaxResults: 20})
	if err != nil || len(rows) != 2 || delaySeconds[rows[0].ID] != 3 || delaySeconds[rows[1].ID] != 5 {
		t.Fatalf("delay menu: %v %v", rows, err)
	}
	monitor := image.Rect(-1920, 0, 0, 1080)
	if got := delayBounds(monitor, image.Pt(220, 36), 24); got != image.Rect(-1070, 24, -850, 60) {
		t.Fatal("not at the top centre:", got)
	}
	if got := delayBounds(image.Rect(0, 0, 100, 100), image.Pt(220, 36), 24); !got.In(image.Rect(0, 0, 100, 100)) {
		t.Fatal("wider than a tiny monitor:", got)
	}
}

// The countdown must appear without taking focus, count down on its own
// messages, hand over to the capture exactly once, and cancel cleanly.
func TestNativeDelayCountdown(t *testing.T) {
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
	var reported []error
	d.onError = func(err error) { reported = append(reported, err) }
	captured := 0
	d.captureAfterDelay = func() { captured++ }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	for _, dark := range []bool{false, true} {
		d.applyTheme(testTheme(dark))
		before := call(user32, "GetForegroundWindow")
		d.toggleDelay(3)
		w := d.delaying
		if w == nil || !d.capturing.Load() || call(user32, "IsWindowVisible", w.hwnd) == 0 {
			t.Fatal("countdown did not start")
		}
		pumpLongMessages()
		if call(user32, "GetForegroundWindow") != before {
			t.Fatal("countdown took the foreground and would close the menu being captured")
		}
		if style := call(user32, "GetWindowLongPtrW", w.hwnd, coord(-20)); style&wsExNoactivate == 0 {
			t.Fatal("countdown can be activated")
		}
		if w.handle(0x0021, 0, 0) != 3 {
			t.Fatal("a click would activate the countdown")
		}
		var r rect
		call(user32, "GetWindowRect", w.hwnd, ptr(&r))
		if !r.image().In(currentMonitor(cursorPoint())) || r.image().Dx() < 100 {
			t.Fatal("countdown placement:", r.image())
		}
		if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
			size := r.image().Size()
			b, err := newBitmap(image.NewRGBA(image.Rectangle{Max: size}))
			if err != nil {
				t.Fatal(err)
			}
			dc := call(gdi32, "CreateCompatibleDC", 0)
			old := call(gdi32, "SelectObject", dc, b.handle)
			fill(dc, image.Rectangle{Max: size}, rgbRef(d.theme.Inverse))
			text(dc, "3 秒后截图 · 点击取消", image.Rectangle{Max: size}, rgbRef(d.theme.InverseText))
			shot, err := b.pixels()
			call(gdi32, "SelectObject", dc, old)
			call(gdi32, "DeleteDC", dc)
			b.close()
			if err != nil {
				t.Fatal(err)
			}
			name := "delay-light.png"
			if dark {
				name = "delay-dark.png"
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := savePNG(filepath.Join(dir, name), shot, false); err != nil {
				t.Fatal(err)
			}
		}
		// A second capture is refused while counting; the countdown goes on.
		if d.capturing.CompareAndSwap(false, true) {
			t.Fatal("countdown does not hold the capture")
		}
		hwnd := w.hwnd
		w.handle(wmTimer, delayTimer, 0)
		w.handle(wmTimer, delayTimer, 0)
		if w.left != 1 || captured != 0 || d.delaying != w {
			t.Fatal("countdown ended early:", w.left)
		}
		w.handle(wmTimer, delayTimer, 0)
		if captured != 1 || d.delaying != nil || call(user32, "IsWindow", hwnd) != 0 {
			t.Fatal("countdown did not hand over to the capture")
		}
		w.tick() // a late timer message must not capture again
		if captured != 1 {
			t.Fatal("captured twice")
		}
		d.capturing.Store(false) // the replaced capture would release it
		captured = 0

		// Running the command again, or clicking the countdown, cancels.
		d.toggleDelay(5)
		d.toggleDelay(5)
		if d.delaying != nil || d.capturing.Load() {
			t.Fatal("second run did not cancel")
		}
		d.toggleDelay(5)
		d.delaying.handle(wmLButtonUp, 0, 0)
		if d.delaying != nil || d.capturing.Load() || captured != 0 {
			t.Fatal("click did not cancel")
		}
	}
	// Another capture in progress: report, do not start counting.
	d.capturing.Store(true)
	d.toggleDelay(3)
	if d.delaying != nil || len(reported) != 1 {
		t.Fatal("countdown started over another capture:", reported)
	}
	d.capturing.Store(false)
}
