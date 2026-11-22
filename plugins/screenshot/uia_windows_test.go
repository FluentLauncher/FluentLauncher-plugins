//go:build windows

package main

import (
	"image"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestSnapElementsPreferSmallestAndKeepWindowOrder(t *testing.T) {
	// Elements overlap freely: a full-size container listed first must not
	// hide the button listed after it, and a deeper small part wins.
	window := snapTarget{bounds: image.Rect(0, 0, 400, 300), loose: true, children: []snapTarget{
		{bounds: image.Rect(0, 0, 400, 300), loose: true},
		{bounds: image.Rect(20, 20, 380, 200), loose: true, children: []snapTarget{
			{bounds: image.Rect(30, 30, 130, 60), loose: true},
			{bounds: image.Rect(30, 30, 370, 190), loose: true},
		}},
		{bounds: image.Rect(300, 250, 380, 280), loose: true},
	}}
	behind := snapTarget{bounds: image.Rect(-100, -100, 900, 700), loose: true, children: []snapTarget{{bounds: image.Rect(40, 40, 50, 50), loose: true}}}
	targets := []snapTarget{window, behind}
	chain := snapChain(targets, image.Pt(40, 40))
	want := []image.Rectangle{window.bounds, image.Rect(20, 20, 380, 200), image.Rect(30, 30, 130, 60)}
	if len(chain) != len(want) {
		t.Fatalf("chain %v, want %v", chain, want)
	}
	for i := range want {
		if chain[i] != want[i] {
			t.Fatalf("chain %v, want %v", chain, want)
		}
	}
	if got := snapAt(targets, image.Pt(310, 260)); got != image.Rect(300, 250, 380, 280) {
		t.Fatal("later sibling lost to a container listed first:", got)
	}
	// Equal rectangles appear once, so one wheel step always changes the highlight.
	if chain := snapChain(targets, image.Pt(5, 290)); len(chain) != 1 || chain[0] != window.bounds {
		t.Fatal("duplicate level:", chain)
	}
	// Parts of a window behind never show through the window in front.
	if got := snapAt(targets, image.Pt(45, 45)); got == image.Rect(40, 40, 50, 50) {
		t.Fatal("element of a covered window was chosen")
	}
}

func TestSnapOrderStartsUnderPointerAndSkipsHiddenWindows(t *testing.T) {
	targets := []snapTarget{
		{bounds: image.Rect(1800, 0, 1920, 40)},    // 0 small topmost
		{bounds: image.Rect(0, 0, 1920, 1080)},     // 1 maximized
		{bounds: image.Rect(100, 100, 800, 600)},   // 2 hidden behind 1
		{bounds: image.Rect(1920, 0, 3840, 1080)},  // 3 on the second monitor
		{bounds: image.Rect(1900, 500, 2100, 700)}, // 4 partly visible beside 1 and behind 3
	}
	order := snapOrder(targets, image.Pt(500, 500), 10)
	if len(order) != 4 || order[0] != 1 || order[1] != 0 || order[2] != 3 || order[3] != 4 {
		t.Fatal("order:", order)
	}
	if order := snapOrder(targets, image.Pt(-5, -5), 2); len(order) != 2 || order[0] != 0 || order[1] != 1 {
		t.Fatal("limit or pointer outside every window:", order)
	}
}

func TestSnapWheelMovesBetweenPartAndWindow(t *testing.T) {
	s := snapFixture()
	origin := s.desktopBounds.Min
	s.d.preferences.SnapElements = true
	s.snaps[0].loose = true
	s.snaps[0].children = []snapTarget{{bounds: image.Rect(120, 120, 300, 200).Add(origin), loose: true, children: []snapTarget{{bounds: image.Rect(130, 130, 180, 160).Add(origin), loose: true}}}}
	s.cursor = image.Pt(140, 140)
	button, panel, window := image.Rect(130, 130, 180, 160), image.Rect(120, 120, 300, 200), image.Rect(100, 100, 400, 300)
	if got := s.snapCandidate(); got != button {
		t.Fatal("smallest part first:", got)
	}
	// The same arithmetic as snapWheel, which also repaints a real window.
	step := func(outward bool) image.Rectangle {
		if outward {
			s.snapLevel++
		} else {
			s.snapLevel--
		}
		return s.snapCandidate()
	}
	if step(true) != panel || step(true) != window || step(true) != window {
		t.Fatal("wheel outward does not stop at the window:", s.snapLevel)
	}
	if step(false) != panel || step(false) != button || step(false) != button {
		t.Fatal("wheel inward does not stop at the smallest part:", s.snapLevel)
	}
	// Pointing at something else starts from its smallest part again.
	step(true)
	s.cursor = image.Pt(250, 180)
	s.snapCandidate() // the panel contains the button: followed once the pointer rests
	s.snapLeft = time.Now().Add(-snapStickDelay)
	if got := s.snapCandidate(); got != panel || s.snapLevel != 0 {
		t.Fatal("level carried over to another part:", got, s.snapLevel)
	}
	// The press decides what a click selects, at the level chosen before it.
	s.cursor = image.Pt(140, 140)
	s.snapCandidate()
	step(true)
	s.anchor, s.dragMode, s.cursor = image.Pt(140, 140), 1, image.Pt(141, 141)
	s.drag(s.cursor)
	if got := s.snapCandidate(); got != panel {
		t.Fatal("click ignores the chosen level:", got)
	}
	s.dragMode = 0
	// Elements off: only the window, whatever the tree holds.
	s.d.preferences.SnapElements = false
	if got := s.snapCandidate(); got != window {
		t.Fatal("element snapping ignores its setting:", got)
	}
}

// Reads a real window of this process through UI Automation: two standard
// buttons at known positions. The GUI thread keeps pumping messages because
// the buttons answer on it.
func TestNativeUIAReadsWindowElements(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native UI Automation check")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	origin := currentMonitor(cursorPoint()).Min.Add(image.Pt(120, 120))
	bounds := image.Rect(0, 0, 420, 260).Add(origin)
	parent := d.createWindow("FLScreenshotController", "界面元素测试", wsExToolwindow|wsExNoactivate, 0, bounds)
	if parent == 0 {
		t.Fatal("no test window")
	}
	defer call(user32, "DestroyWindow", parent)
	instance := call(kernel32, "GetModuleHandleW", 0)
	button := func(title string, r image.Rectangle) {
		hwnd := call(user32, "CreateWindowExW", 0, ptr(u16("BUTTON")), ptr(u16(title)), uint32(0x50000000),
			coord(r.Min.X), coord(r.Min.Y), r.Dx(), r.Dy(), parent, 0, instance, 0)
		if hwnd == 0 {
			t.Fatal("no button")
		}
	}
	first, second := image.Rect(30, 40, 170, 90), image.Rect(220, 150, 380, 220)
	button("确定", first)
	button("取消", second)
	windowAtLevel(parent, bounds, false, true)
	pumpLongMessages()

	type outcome struct {
		parts   []snapTarget
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if hr := int32(call(ole32, "CoInitializeEx", 0, 0)); hr >= 0 {
			defer call(ole32, "CoUninitialize")
		}
		reader, err := newUIAReader()
		if err != nil {
			done <- outcome{err: err}
			return
		}
		defer reader.close()
		start := time.Now()
		parts := reader.elements(parent, bounds)
		done <- outcome{parts: parts, elapsed: time.Since(start)}
	}()
	var got outcome
	deadline := time.Now().Add(15 * time.Second)
wait:
	for {
		select {
		case got = <-done:
			break wait
		default:
			pumpLongMessages()
			if time.Now().After(deadline) {
				t.Fatal("UI Automation did not answer")
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Logf("read %d top-level parts in %v", len(got.parts), got.elapsed)
	window := []snapTarget{{bounds: bounds, children: got.parts, loose: true}}
	for name, r := range map[string]image.Rectangle{"first": first, "second": second} {
		center := r.Min.Add(r.Size().Div(2)).Add(origin)
		if found := snapAt(window, center); found != r.Add(origin) {
			t.Errorf("%s button: snapped to %v, want %v (parts %v)", name, found, r.Add(origin), got.parts)
		}
	}
	if found := snapAt(window, origin.Add(image.Pt(200, 120))); found != bounds {
		t.Errorf("empty area did not fall back to the window: %v", found)
	}
}

// The background path of a real capture: a selection opens with window-level
// snapping, the taskbar (a window of another process that every desktop has)
// is read while it is open, and the wheel then moves between its parts.
// Only rectangles are read; no names or text.
func TestNativeSnapRefinesWhileSelectionIsOpen(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native UI Automation check")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	taskbar := call(user32, "FindWindowW", ptr(u16("Shell_TrayWnd")), 0)
	bounds, ok := snapTopLevel(taskbar)
	if taskbar == 0 || !ok {
		t.Skip("no visible taskbar to read")
	}
	previous := call(user32, "GetForegroundWindow")
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("refine native error: %v", err) }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	// A small synthetic frame placed over the taskbar's own coordinates.
	area := image.Rectangle{Min: bounds.Min, Max: bounds.Min.Add(image.Pt(min(600, bounds.Dx()), bounds.Dy()))}
	if err := d.openSelection(scrollFixture(area.Dx(), area.Dy()), area, false, previous); err != nil {
		t.Fatal(err)
	}
	s := d.selection
	targets := []snapTarget{{bounds: bounds, hwnd: taskbar}}
	s.snaps = targets
	d.active.Add(1)
	go d.refineSnaps(s, append([]snapTarget(nil), targets...), bounds.Min)
	deadline := time.Now().Add(15 * time.Second)
	for !s.snaps[0].loose {
		pumpLongMessages()
		if time.Now().After(deadline) {
			t.Fatal("taskbar elements never arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for d.active.Load() != 0 {
		pumpLongMessages()
		time.Sleep(5 * time.Millisecond)
	}
	// Find a point over some part and walk outward with real wheel messages.
	var part image.Rectangle
	var find func([]snapTarget)
	find = func(parts []snapTarget) {
		for _, p := range parts {
			if part.Empty() && p.bounds.In(area) && p.bounds != bounds {
				part = p.bounds
			}
			find(p.children)
		}
	}
	find(s.snaps[0].children)
	if part.Empty() {
		t.Skip("the taskbar exposes no parts inside the test area")
	}
	local := part.Min.Add(part.Size().Div(2)).Sub(area.Min)
	pos := uintptr(uint32(uint16(local.X)) | uint32(uint16(local.Y))<<16)
	s.handle(wmMouseMove, 0, pos)
	smallest := s.snapCandidate()
	if smallest.Empty() || smallest == bounds.Sub(area.Min).Intersect(s.image.Bounds()) {
		t.Fatal("hover still snaps to the whole window:", smallest)
	}
	wheel := func(delta int16) { s.handle(wmMouseWheel, uintptr(uint32(uint16(delta))<<16), pos) }
	for i := 0; i < 80; i++ {
		wheel(120)
	}
	if got := s.snapCandidate(); got != bounds.Sub(area.Min).Intersect(s.image.Bounds()) {
		t.Fatal("wheel outward did not reach the window:", got)
	}
	for i := 0; i < 80; i++ {
		wheel(-120)
	}
	if got := s.snapCandidate(); got != smallest {
		t.Fatal("wheel inward did not return to the smallest part:", got, smallest)
	}
	// A click selects the level chosen with the wheel.
	wheel(120)
	chosen := s.snapCandidate()
	s.handle(wmLButtonDown, 0, pos)
	s.handle(wmLButtonUp, 0, pos)
	if s.selected != chosen {
		t.Fatal("click selected", s.selected, "instead of", chosen)
	}
	// Closing stops background work from touching the selection again.
	s.close()
	if !s.closed.Load() {
		t.Fatal("closed selection is not marked for background work")
	}
}

// Menu items with wide gaps inside a large bar: travelling from one item to
// the next must never light up the whole bar in between.
func TestSnapHoldsPartWhileCrossingItsContainer(t *testing.T) {
	s := snapFixture()
	origin := s.desktopBounds.Min
	s.d.preferences.SnapElements = true
	s.snaps[0].loose = true
	item, next := image.Rect(150, 200, 190, 220), image.Rect(250, 200, 300, 220)
	s.snaps[0].children = []snapTarget{{bounds: item.Add(origin), loose: true}, {bounds: next.Add(origin), loose: true}}
	window := image.Rect(100, 100, 400, 300)
	at := func(x, y int) image.Rectangle { s.cursor = image.Pt(x, y); return s.snapCandidate() }
	rested := func() { s.snapLeft = time.Now().Add(-snapStickDelay) }

	if at(120, 120) != window || at(160, 210) != item {
		t.Fatal("entering a smaller part is not immediate")
	}
	// Across the 60 pixel gap, far beyond any edge margin, then onto the next item.
	for x := 191; x < 250; x += 7 {
		if got := at(x, 210); got != item {
			t.Fatalf("gap at x=%d switched to %v", x, got)
		}
	}
	if at(255, 210) != next {
		t.Fatal("the next item is not followed at once")
	}
	// Each move restarts the wait: an old timestamp does not count for a new position.
	at(220, 210)
	rested()
	if at(221, 210) != next {
		t.Fatal("the wait was not restarted by movement")
	}
	// Resting grows to the container; coming back shrinks at once.
	rested()
	if at(221, 210) != window || !s.snapLeft.IsZero() {
		t.Fatal("resting over the container never selected it")
	}
	if at(255, 210) != next {
		t.Fatal("returning to the part")
	}
	// Back on the part cancels a pending wait.
	at(310, 210)
	if at(260, 210) != next || !s.snapLeft.IsZero() {
		t.Fatal("returning did not cancel the wait")
	}
	// A press beside the part selects it; a press far away selects what is under it.
	at(304, 210)
	s.anchor, s.dragMode = image.Pt(304, 210), 1
	if got := s.snapCandidate(); got != next {
		t.Fatal("press beside the highlighted part:", got)
	}
	s.dragMode = 0
	at(255, 210)
	at(350, 280)
	s.anchor, s.dragMode = image.Pt(350, 280), 1
	if got := s.snapCandidate(); got != window {
		t.Fatal("press far from the highlighted part:", got)
	}
	s.dragMode = 0
	// The wheel level survives the crossing as well.
	at(160, 210)
	s.snapLevel = 1
	if at(160, 210) != window || at(220, 210) != window || s.snapLevel != 1 {
		t.Fatal("crossing reset the level chosen with the wheel")
	}
	if !snapRetreat(item, window, image.Pt(220, 210)) || snapRetreat(item, next, image.Pt(255, 210)) || snapRetreat(window, item, image.Pt(160, 210)) || snapRetreat(item, window, image.Pt(160, 210)) {
		t.Fatal("snapRetreat")
	}
}
