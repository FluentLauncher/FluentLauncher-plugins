//go:build windows

package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSurfaceGradientAndForegroundAlpha(t *testing.T) {
	theme := testTheme(false)
	end := uint32(0)
	theme.GradientEnd = &end
	if surfaceColor(theme, 0, 20) != theme.Background || surfaceColor(theme, 19, 20) != 0 {
		t.Fatal("gradient endpoints lost, including black")
	}
	if middle := surfaceColor(theme, 10, 21); middle != 0x808080 {
		t.Fatalf("gradient does not blend vertically: %06x", middle)
	}
	plate := image.Rect(0, 0, 30, 20)
	img := image.NewRGBA(plate)
	for y := 0; y < 20; y++ {
		for x := 0; x < 30; x++ {
			c := surfaceColor(theme, y, 20)
			img.SetRGBA(x, y, color.RGBA{uint8(c >> 16), uint8(c >> 8), uint8(c), 255})
		}
	}
	// A foreground pixel can equal the gradient color at the same point.
	// Filled controls must remain opaque even in that coincidence.
	surfaceAlpha(img, theme, plate, 0, 89, []opaqueSurface{{image.Rect(8, 8, 16, 16), 2}})
	if img.RGBAAt(20, 2).A != 89 || img.RGBAAt(10, 10).A != 255 {
		t.Fatal("background opacity faded the foreground")
	}
	theme.GradientEnd = nil
	if surfaceColor(theme, 19, 20) != theme.Background {
		t.Fatal("old solid theme not supported")
	}
}

func TestNativeToolbarGradientAndOpacity(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native surface QA")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("native surface: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	monitor := currentMonitor(cursorPoint())
	origin := monitor.Min.Add(image.Pt(20, 20))
	size := image.Pt(min(960, monitor.Dx()-40), min(760, monitor.Dy()-40))
	img := image.NewRGBA(image.Rectangle{Max: size})
	for y := 0; y < size.Y; y++ {
		for x := 0; x < size.X; x++ {
			c := color.RGBA{160, 185, 210, 255}
			if (x/20+y/20)%2 == 0 {
				c = color.RGBA{235, 220, 190, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	theme := testTheme(false)
	theme.Background = 0xf1dedd
	end := uint32(0x99c5ee)
	opacity := float32(0.35)
	theme.GradientEnd = &end
	theme.Opacity = &opacity
	d.applyTheme(theme)
	if err := d.openSelection(img, img.Bounds().Add(origin), false, previous); err != nil {
		t.Fatal(err)
	}
	selection := d.selection
	selection.selected = image.Rect(24, 32, 344, 240)
	invalidate(selection.hwnd)
	call(user32, "UpdateWindow", selection.hwnd)
	bar := selection.toolbar()
	checkFrozen := func(a float64) {
		t.Helper()
		call(user32, "UpdateWindow", selection.hwnd)
		panel, err := selection.panelCache.pixels()
		if err != nil {
			t.Fatal(err)
		}
		for _, y := range []int{0, panel.Rect.Dy() - 1} {
			x := panel.Rect.Dx() / 2
			c := img.RGBAAt(bar.Min.X+x, bar.Min.Y+y)
			tint := surfaceColor(theme, y, panel.Rect.Dy())
			blend := func(v uint8, tint uint32) uint8 { return uint8(float64(v)*0.6*(1-a) + float64(tint)*a + 0.5) }
			want := color.RGBA{blend(c.R, (tint>>16)&255), blend(c.G, (tint>>8)&255), blend(c.B, tint&255), 255}
			if got := panel.RGBAAt(x, y); got != want {
				t.Fatalf("frozen toolbar opacity/endpoint mismatch: %v != %v", got, want)
			}
		}
	}
	checkFrozen(float64(opacity))
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		shot, err := selection.paintBuffer.pixels()
		if err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "region-toolbar-gradient.png"), crop(shot, bar), false); err != nil {
			t.Fatal(err)
		}
	}
	zero := float32(0)
	theme.Opacity = &zero
	d.applyTheme(theme)
	checkFrozen(0)
	full := float32(1)
	theme.Opacity = &full
	d.applyTheme(theme)
	checkFrozen(1)
	selection.close()
	base, err := d.openPin(img, img.Bounds().Add(origin))
	if err != nil {
		t.Fatal(err)
	}
	base.state.Shadow = false
	base.syncWindow()
	theme.Opacity = &opacity
	d.applyTheme(theme)
	region := image.Rect(24, 32, 344, 240)
	first := crop(img, region)
	d.captureLong = func(image.Rectangle) (*image.RGBA, error) { return first, nil }
	d.startLongCapture(region.Add(origin), uiScale(base.hwnd), longSeed{first: first})
	s := d.scrolling
	if s == nil {
		t.Fatal("no long preview")
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.thumbnail == nil {
		pumpLongMessages()
		if time.Now().After(deadline) {
			t.Fatal("surface fixture timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.activate(0)
	call(user32, "UpdateWindow", s.hwnd)
	layer, err := s.layeredImage()
	if err != nil {
		t.Fatal(err)
	}
	longBar := s.toolbar()
	a := materialAlpha(theme, "none")
	for _, y := range []int{longBar.Min.Y + 2, longBar.Max.Y - 3} {
		c := layer.RGBAAt(longBar.Dx()/2, y)
		tint := surfaceColor(theme, y-longBar.Min.Y, longBar.Dy())
		want := color.RGBA{uint8(((tint >> 16) & 255) * uint32(a) / 255), uint8(((tint >> 8) & 255) * uint32(a) / 255), uint8((tint & 255) * uint32(a) / 255), a}
		if c != want {
			t.Fatalf("long toolbar lost gradient or opacity: %v != %v", c, want)
		}
	}
	imagePoint := s.imageDestination().Min.Add(image.Pt(3, 3))
	if layer.RGBAAt(imagePoint.X, imagePoint.Y).A != 255 {
		t.Fatal("toolbar opacity faded the screenshot")
	}
	primary := s.buttons()[2].Min.Add(image.Pt(px(8, uiScale(s.hwnd)), px(8, uiScale(s.hwnd))))
	if layer.RGBAAt(primary.X, primary.Y).A != 255 {
		t.Fatal("primary control faded with background")
	}
	absBar := longBar.Add(s.bounds.Min)
	if absBar.In(base.state.Bounds) {
		// Temporarily allow only this synthetic fixture into desktop capture to
		// verify the OS compositor, rather than only our alpha buffer.
		call(user32, "SetWindowDisplayAffinity", s.hwnd, 0)
		deadline := time.Now().Add(time.Second)
		for {
			pumpLongMessages()
			call(dwmapi, "DwmFlush")
			shot, err := captureScreen(absBar)
			if err != nil {
				t.Fatal(err)
			}
			matched := true
			for _, y := range []int{2, longBar.Dy() - 3} {
				x := longBar.Dx() / 2
				fg := layer.RGBAAt(x, longBar.Min.Y+y)
				bg := img.RGBAAt(absBar.Min.X-origin.X+x, absBar.Min.Y-origin.Y+y)
				blend := func(v, b uint8) uint8 { return uint8(int(v) + (int(b)*(255-int(fg.A))+127)/255) }
				want := color.RGBA{blend(fg.R, bg.R), blend(fg.G, bg.G), blend(fg.B, bg.B), 255}
				got := shot.RGBAAt(x, y)
				if abs(int(got.R)-int(want.R)) > 3 || abs(int(got.G)-int(want.G)) > 3 || abs(int(got.B)-int(want.B)) > 3 {
					matched = false
				}
			}
			if matched {
				if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
					if err := savePNG(filepath.Join(dir, "long-toolbar-gradient.png"), shot, false); err != nil {
						t.Fatal(err)
					}
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("desktop did not composite the translucent gradient toolbar over its source")
			}
			time.Sleep(15 * time.Millisecond)
		}
		call(user32, "SetWindowDisplayAffinity", s.hwnd, 0x11)
	}
	changed := uint32(0x778899)
	theme.GradientEnd = &changed
	newOpacity := float32(0.7)
	theme.Opacity = &newOpacity
	d.applyTheme(theme)
	call(user32, "UpdateWindow", s.hwnd)
	updated, err := s.layeredImage()
	if err != nil {
		t.Fatal(err)
	}
	if updated.RGBAAt(longBar.Dx()/2, longBar.Min.Y+2).A != materialAlpha(theme, "none") {
		t.Fatal("live opacity update was ignored")
	}
	y := longBar.Max.Y - 3
	tint := surfaceColor(theme, y-longBar.Min.Y, longBar.Dy())
	alpha := uint32(materialAlpha(theme, "none"))
	want := color.RGBA{uint8(((tint >> 16) & 255) * alpha / 255), uint8(((tint >> 8) & 255) * alpha / 255), uint8((tint & 255) * alpha / 255), uint8(alpha)}
	if updated.RGBAAt(longBar.Dx()/2, y) != want {
		t.Fatal("live gradient endpoint update was ignored")
	}
	theme.GradientEnd = nil
	theme.Backdrop = "acrylic"
	d.applyTheme(theme)
	call(user32, "UpdateWindow", s.hwnd)
	acrylic, err := s.layeredImage()
	if err != nil {
		t.Fatal(err)
	}
	if acrylic.RGBAAt(longBar.Dx()/2, longBar.Min.Y+2).A != materialAlpha(theme, s.material) || acrylic.RGBAAt(imagePoint.X, imagePoint.Y).A != 255 {
		t.Fatal("acrylic update lost separate background/image opacity")
	}
	s.close(true)
	base.close()
}
