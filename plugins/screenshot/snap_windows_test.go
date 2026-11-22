//go:build windows

package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSnapPicksFrontWindowAndDeepestChild(t *testing.T) {
	back := snapTarget{bounds: image.Rect(0, 0, 500, 400)}
	front := snapTarget{bounds: image.Rect(100, 100, 400, 300), children: []snapTarget{
		{bounds: image.Rect(110, 140, 390, 290), children: []snapTarget{{bounds: image.Rect(120, 150, 200, 200)}}},
		{bounds: image.Rect(110, 140, 390, 290)}, // covered sibling never wins
	}}
	targets := []snapTarget{front, back}
	for _, c := range []struct {
		p    image.Point
		want image.Rectangle
	}{
		{image.Pt(130, 160), image.Rect(120, 150, 200, 200)}, // deepest child
		{image.Pt(300, 250), image.Rect(110, 140, 390, 290)}, // child
		{image.Pt(105, 105), front.bounds},                   // title bar: the whole window
		{image.Pt(50, 50), back.bounds},                      // window behind, where it is not covered
		{image.Pt(400, 300), back.bounds},                    // Max is exclusive
		{image.Pt(600, 50), image.Rectangle{}},               // bare desktop
	} {
		if got := snapAt(targets, c.p); got != c.want {
			t.Fatalf("snapAt(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func snapFixture() *selectionWindow {
	origin := image.Pt(-300, 40) // a monitor left of the primary one
	d := &desktop{preferences: preferences{SnapWindows: true}, theme: testTheme(false)}
	return &selectionWindow{d: d, image: image.NewRGBA(image.Rect(0, 0, 800, 600)), desktopBounds: image.Rect(0, 0, 800, 600).Add(origin),
		toolbarHover: -1, toolbarFocus: -1,
		snaps: []snapTarget{{bounds: image.Rect(100, 100, 400, 300).Add(origin)}, {bounds: image.Rect(-50, 200, 900, 700).Add(origin)}}}
}

func TestSnapClickSelectsWindowAndDragOverrides(t *testing.T) {
	s := snapFixture()
	s.cursor = image.Pt(150, 150)
	if got := s.snapCandidate(); got != image.Rect(100, 100, 400, 300) {
		t.Fatal("hover candidate is not in image coordinates:", got)
	}
	s.cursor = image.Pt(20, 400)
	if got := s.snapCandidate(); got != image.Rect(0, 200, 800, 600) {
		t.Fatal("candidate was not clipped to the frozen image:", got)
	}
	// A press that barely moves stays a click on the window under the press.
	s.anchor, s.dragMode, s.cursor = image.Pt(150, 150), 1, image.Pt(152, 151)
	s.drag(s.cursor)
	if !s.selected.Empty() || s.snapCandidate() != image.Rect(100, 100, 400, 300) {
		t.Fatal("small movement cancelled the snap:", s.selected)
	}
	// Moving on makes it a manual selection for the rest of the gesture,
	// even when the pointer returns next to where it was pressed.
	s.drag(image.Pt(700, 500))
	if s.selected != image.Rect(150, 150, 700, 500) || !s.snapOff {
		t.Fatal("drag did not take over:", s.selected)
	}
	s.drag(image.Pt(151, 151))
	s.selected = image.Rectangle{} // a 1 px drag may collapse; the snap must still stay off
	if !s.snapCandidate().Empty() {
		t.Fatal("snap came back during a drag")
	}
	s.dragMode, s.snapOff = 0, false
	s.selected = image.Rect(10, 10, 50, 50)
	if !s.snapCandidate().Empty() {
		t.Fatal("an existing selection must not show another candidate")
	}
	s.selected = image.Rectangle{}
	s.d.preferences.SnapWindows = false
	if !s.snapCandidate().Empty() {
		t.Fatal("snap ignores its setting")
	}
	s.d.preferences.SnapWindows, s.snaps = true, nil
	if !s.snapCandidate().Empty() {
		t.Fatal("without recorded windows a click must not select a monitor")
	}
}

func TestSnapPreferenceDefaultsOn(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if !loadPreferences().SnapWindows {
		t.Fatal("snap is not on by default")
	}
	if err := os.MkdirAll(filepath.Dir(preferencesPath()), 0755); err != nil {
		t.Fatal(err)
	}
	// A file written before the option existed keeps it on; an explicit off stays off.
	if err := os.WriteFile(preferencesPath(), []byte(`{"includePins":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if !loadPreferences().SnapWindows {
		t.Fatal("old settings turned snap off")
	}
	p := loadPreferences()
	p.SnapWindows = false
	if err := savePreferences(p); err != nil {
		t.Fatal(err)
	}
	if loadPreferences().SnapWindows {
		t.Fatal("snap setting was not saved")
	}
}

// Drives the real selection window with its own mouse messages over a
// synthetic image; a pin serves as the live window that enumeration must find.
func TestNativeSelectionSnap(t *testing.T) {
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
	d.onError = func(err error) { t.Errorf("snap native error: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	origin := currentMonitor(cursorPoint()).Min.Add(image.Pt(80, 80))
	pinBounds := image.Rect(40, 60, 360, 300).Add(origin)
	pin, err := d.openPin(scrollFixture(pinBounds.Dx(), pinBounds.Dy()), pinBounds)
	if err != nil {
		t.Fatal(err)
	}
	pumpLongMessages()
	found := false
	var walk func([]snapTarget)
	walk = func(targets []snapTarget) {
		for _, target := range targets {
			found = found || target.bounds == pinBounds
			if target.bounds.Empty() {
				t.Fatal("enumeration returned an empty window")
			}
			walk(target.children)
		}
	}
	live := snapTargets()
	walk(live)
	if !found {
		t.Fatalf("live enumeration missed a visible pin among %d windows", len(live))
	}
	pin.close()

	img := image.NewRGBA(image.Rect(0, 0, 760, 440))
	window, panel := image.Rect(60, 70, 520, 380), image.Rect(80, 130, 500, 360)
	for y := 0; y < 440; y++ {
		for x := 0; x < 760; x++ {
			c := color.RGBA{R: 96, G: 125, B: 150, A: 255}
			if p := image.Pt(x, y); p.In(panel) {
				c = color.RGBA{R: 250, G: 250, B: 250, A: 255}
			} else if p.In(window) {
				c = color.RGBA{R: 225, G: 230, B: 238, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	for _, dark := range []bool{false, true} {
		d.applyTheme(testTheme(dark))
		if err := d.openSelection(img, img.Bounds().Add(origin), false, previous); err != nil {
			t.Fatal(err)
		}
		s := d.selection
		s.snaps = []snapTarget{{bounds: window.Add(origin), children: []snapTarget{{bounds: panel.Add(origin)}}}}
		pos := func(p image.Point) uintptr { return uintptr(uint32(uint16(p.X)) | uint32(uint16(p.Y))<<16) }
		s.handle(wmMouseMove, 0, pos(image.Pt(200, 200)))
		if s.snapCandidate() != panel {
			t.Fatal("hover did not highlight the child under the pointer")
		}
		call(user32, "UpdateWindow", s.hwnd)
		shot, err := s.paintBuffer.pixels()
		if err != nil {
			t.Fatal(err)
		}
		accent := testTheme(dark).Accent
		if c := shot.RGBAAt(panel.Min.X, panel.Min.Y+40); uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B) != accent {
			t.Fatal("highlight frame missing:", c)
		}
		if shot.RGBAAt(300, 250) != img.RGBAAt(300, 250) || shot.RGBAAt(700, 420) == img.RGBAAt(700, 420) {
			t.Fatal("highlight is not the only undimmed area")
		}
		if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
			name := "snap-hover-light.png"
			if dark {
				name = "snap-hover-dark.png"
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := savePNG(filepath.Join(dir, name), shot, false); err != nil {
				t.Fatal(err)
			}
		}
		// Click the title area: the whole window. Then click outside: the monitor.
		s.handle(wmLButtonDown, 0, pos(image.Pt(70, 90)))
		s.handle(wmMouseMove, 0, pos(image.Pt(71, 91)))
		s.handle(wmLButtonUp, 0, pos(image.Pt(71, 91)))
		if s.selected != window || s.dragMode != 0 {
			t.Fatal("click did not select the window:", s.selected)
		}
		s.handle(wmLButtonDown, 0, pos(image.Pt(700, 20)))
		s.handle(wmLButtonUp, 0, pos(image.Pt(700, 20)))
		if s.selected != img.Bounds() {
			t.Fatal("click on the bare desktop did not select the visible monitor area:", s.selected)
		}
		// Dragging from inside a window still makes a free selection.
		s.handle(wmLButtonDown, 0, pos(image.Pt(5, 5)))
		s.selected = image.Rectangle{}
		s.handle(wmLButtonUp, 0, pos(image.Pt(5, 5)))
		s.selected = image.Rectangle{}
		s.handle(wmLButtonDown, 0, pos(image.Pt(200, 200)))
		s.handle(wmMouseMove, 0, pos(image.Pt(320, 300)))
		s.handle(wmLButtonUp, 0, pos(image.Pt(320, 300)))
		if s.selected != image.Rect(200, 200, 320, 300) {
			t.Fatal("drag was replaced by a snap:", s.selected)
		}
		s.close()
	}
}
