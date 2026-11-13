//go:build windows

package main

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestToolbarHintAlignmentAndDPI(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, scale := range []float64{1, 1.5, 2} {
		t.Run(fmt.Sprint(scale), func(t *testing.T) {
			bounds := image.Rect(0, 0, px(800, scale), px(180, scale))
			b, err := newBitmap(image.NewRGBA(bounds))
			if err != nil {
				t.Fatal(err)
			}
			defer b.close()
			dc := call(gdi32, "CreateCompatibleDC", 0)
			old := call(gdi32, "SelectObject", dc, b.handle)
			defer func() { call(gdi32, "SelectObject", dc, old); call(gdi32, "DeleteDC", dc) }()
			for i, tool := range captureTools {
				button := image.Rect(px(80+i*45, scale), px(100, scale), px(108+i*45, scale), px(128, scale))
				r := paintToolbarHint(dc, tool.label+" · "+tool.hint, button, bounds, scale, testTheme(false))
				if abs(r.Min.X+r.Max.X-button.Min.X-button.Max.X) > 1 || r.Max.Y != button.Min.Y-px(6, scale) {
					t.Fatalf("%s hint is not centered above its icon: %v, button %v", tool.id, r, button)
				}
				if !r.In(bounds) || r.Dx() >= px(205, scale) || r.Dy() >= px(28, scale) || r.Dy() < px(20, scale) {
					t.Fatalf("%s hint is not compact or in bounds at scale %v: %v", tool.id, scale, r)
				}
			}
		})
	}
	monitor := image.Rect(-1280, 0, 0, 720)
	for _, button := range []image.Rectangle{image.Rect(-1280, 500, -1252, 528), image.Rect(-28, 500, 0, 528), image.Rect(-600, 0, -572, 28)} {
		r := toolbarHintBounds(button, image.Pt(90, 12), monitor, 1)
		if !r.In(monitor) || r.Overlaps(button) {
			t.Fatalf("monitor edge clipped or overlapped the hint: %v, button %v", r, button)
		}
	}
}

// Render the production toolbar into a memory bitmap; never open a window,
// capture the desktop, move the pointer or read the user's configuration.
func TestCaptureToolbarHintRender(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, dark := range []bool{false, true} {
		theme := testTheme(dark)
		img := image.NewRGBA(image.Rect(0, 0, 800, 260))
		background, err := newBitmap(img)
		if err != nil {
			t.Fatal(err)
		}
		defer background.close()
		s := &selectionWindow{d: &desktop{theme: theme}, image: img, background: background, selected: image.Rect(30, 20, 760, 145), toolbarFocus: -1}
		defer func() { s.panelCache.close() }()
		var buffer paintBuffer
		defer buffer.close()
		for _, index := range []int{0, 9, 12} {
			s.toolbarHover = index
			if err := buffer.render(0, img.Bounds().Size(), func(dc uintptr) {
				fill(dc, img.Bounds(), rgbRef(theme.Surface))
				s.paintToolbar(dc)
			}); err != nil {
				t.Fatal(err)
			}
			if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
				shot, err := buffer.pixels()
				if err != nil {
					t.Fatal(err)
				}
				if err := savePNG(filepath.Join(dir, fmt.Sprintf("toolbar-hint-dark-%t-%s.png", dark, captureTools[index].id)), shot, false); err != nil {
					t.Fatal(err)
				}
			}
		}
		// Keyboard focus shares the same hint placement when the pointer leaves.
		s.toolbarHover, s.toolbarFocus = -1, 9
		if err := buffer.render(0, img.Bounds().Size(), s.paintToolbar); err != nil {
			t.Fatal(err)
		}
	}
}
