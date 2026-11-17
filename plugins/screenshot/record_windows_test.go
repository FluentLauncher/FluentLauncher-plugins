//go:build windows

package main

import (
	"context"
	"errors"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

// A bar sweeping over a white frame: every grab differs from the last.
type sweepSource struct {
	out    image.Point
	grabs  atomic.Int32
	fail   atomic.Bool
	closed atomic.Bool
}

func (s *sweepSource) size() image.Point { return s.out }
func (s *sweepSource) close()            { s.closed.Store(true) }
func (s *sweepSource) grab(dst []byte) error {
	if s.fail.Load() {
		return errors.New("desktop unavailable")
	}
	n := int(s.grabs.Add(1))
	copy(dst, bgraFrame(s.out.X, s.out.Y, color.RGBA{255, 255, 255, 255}))
	x := n * 4 % (s.out.X - 8)
	paintBGRA(dst, s.out.X, image.Rect(x, 0, x+8, s.out.Y), color.RGBA{255, 0, 0, 255})
	return nil
}

func recordedFiles(t *testing.T, dir string) (finished, partial []string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".partial") {
			partial = append(partial, entry.Name())
		} else {
			finished = append(finished, entry.Name())
		}
	}
	return
}

func waitRecorder(t *testing.T, r *recorder) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("recorder did not stop")
	}
}

func TestRecorderPauseStopAndDiscard(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "FluentLauncher")
	source := &sweepSource{out: image.Pt(96, 64)}
	r := newRecorder(func() (frameSource, error) { return source, nil }, dir, 20, time.Minute)
	go r.run()
	time.Sleep(300 * time.Millisecond)
	if _, partial := recordedFiles(t, dir); len(partial) != 1 {
		t.Fatalf("a running recording must only exist as .partial: %v", partial)
	}
	r.paused.Store(true)
	time.Sleep(120 * time.Millisecond) // let an in-flight tick settle
	grabs, elapsed := source.grabs.Load(), r.elapsed.Load()
	time.Sleep(400 * time.Millisecond)
	if source.grabs.Load() != grabs || r.elapsed.Load() != elapsed {
		t.Fatal("paused recorder kept capturing or counting time")
	}
	r.paused.Store(false)
	source.fail.Store(true) // e.g. the secure desktop: frames are skipped, not fatal
	time.Sleep(150 * time.Millisecond)
	source.fail.Store(false)
	time.Sleep(250 * time.Millisecond)
	r.finish(false)
	waitRecorder(t, r)
	if r.err != nil || r.path == "" || !source.closed.Load() {
		t.Fatalf("finish: %v %q closed=%v", r.err, r.path, source.closed.Load())
	}
	finished, partial := recordedFiles(t, dir)
	if len(finished) != 1 || len(partial) != 0 {
		t.Fatalf("files %v %v", finished, partial)
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, frames := composeGIF(t, data)
	if decoded.Config.Width != 96 || decoded.Config.Height != 64 || len(frames) < 6 {
		t.Fatalf("%d×%d, %d frames", decoded.Config.Width, decoded.Config.Height, len(frames))
	}
	total := 0
	for _, delay := range decoded.Delay {
		total += delay
	}
	// The 400 ms pause is excluded: the animation matches the recorded time.
	recorded := int(time.Duration(r.elapsed.Load()) / (10 * time.Millisecond))
	if total < recorded || total > recorded+12 || recorded > 110 {
		t.Fatalf("animation %d cs, recorded %d cs", total, recorded)
	}
	if int64(len(data)) != r.bytes.Load() {
		t.Fatalf("size counter %d, file %d", r.bytes.Load(), len(data))
	}

	discarded := newRecorder(func() (frameSource, error) { return &sweepSource{out: image.Pt(32, 32)}, nil }, dir, 20, time.Minute)
	go discarded.run()
	time.Sleep(150 * time.Millisecond)
	discarded.finish(true)
	waitRecorder(t, discarded)
	finished, partial = recordedFiles(t, dir)
	if discarded.err != nil || discarded.path != "" || len(finished) != 1 || len(partial) != 0 {
		t.Fatalf("discard: %v %q %v %v", discarded.err, discarded.path, finished, partial)
	}
}

func TestRecorderLimitAndOpenFailure(t *testing.T) {
	dir := t.TempDir()
	r := newRecorder(func() (frameSource, error) { return &sweepSource{out: image.Pt(32, 32)}, nil }, dir, 20, 200*time.Millisecond)
	go r.run()
	waitRecorder(t, r)
	if r.err != nil || !r.limited || r.path == "" {
		t.Fatalf("limit: %v limited=%v %q", r.err, r.limited, r.path)
	}
	failing := newRecorder(func() (frameSource, error) { return nil, errors.New("no desktop") }, filepath.Join(dir, "unused"), 20, time.Minute)
	go failing.run()
	waitRecorder(t, failing)
	if failing.err == nil || failing.path != "" {
		t.Fatal("open failure not reported")
	}
	if _, err := os.Stat(filepath.Join(dir, "unused")); !os.IsNotExist(err) {
		t.Fatal("failed recording created its folder")
	}
}

func TestRecordCommandsRouting(t *testing.T) {
	h := handler{&desktop{}}
	rows, err := h.Query(context.Background(), pluginsdk.Query{Text: "录制", MaxResults: 20})
	if err != nil || len(rows) != 4 {
		t.Fatalf("record menu: %v %v", rows, err)
	}
	for _, id := range []string{"record", "record-screen", "record-stop", "record-folder"} {
		found := false
		for _, entry := range entries {
			found = found || entry.id == id
		}
		if !found {
			t.Fatalf("%s missing from the menu", id)
		}
	}
}

func pumpRecordMessages(t *testing.T, d *desktop, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var msg message
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("native recording did not reach the expected state")
		}
		for call(user32, "PeekMessageW", ptr(&msg), 0, 0, 0, 1) != 0 {
			call(user32, "TranslateMessage", ptr(&msg))
			call(user32, "DispatchMessageW", ptr(&msg))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Opt-in: briefly shows the control bar and outline. Frames come from a
// synthetic source unless stated; nothing is written outside the test folder.
func TestNativeRecordingFlow(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native recording QA")
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
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	d.recordDir = t.TempDir()
	d.recordSource = func(_ image.Rectangle, out image.Point) (frameSource, error) { return &sweepSource{out: out}, nil }
	d.preferences.Recording.Countdown = false
	d.preferences.Recording.Format = "gif"
	monitor := currentMonitor(cursorPoint())
	region := image.Rectangle{Min: monitor.Min.Add(image.Pt(60, 60)), Max: monitor.Min.Add(image.Pt(380, 260))}
	artifacts := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR")
	snapshot := func(name string) {
		w := d.recording
		invalidate(w.hwnd)
		call(user32, "UpdateWindow", w.hwnd)
		img, err := w.layeredImage()
		if err != nil {
			t.Fatal(err)
		}
		opaque := false
		for i := 0; i < len(img.Pix); i += 4 {
			opaque = opaque || img.Pix[i] != img.Pix[0] || img.Pix[i+1] != img.Pix[1]
		}
		if !opaque {
			t.Fatalf("%s: the bar painted no content", name)
		}
		if artifacts != "" {
			_ = os.MkdirAll(artifacts, 0755)
			if err := savePNG(filepath.Join(artifacts, name+".png"), img, false); err != nil {
				t.Fatal(err)
			}
		}
	}

	if d.startRecording(image.Rect(0, 0, 8, 8), 0) || d.recording != nil || len(reported) != 1 {
		t.Fatal("tiny region accepted")
	}
	for _, dark := range []bool{false, true} {
		name := map[bool]string{false: "light", true: "dark"}[dark]
		d.applyTheme(testTheme(dark))
		if !d.startRecording(region, 0) {
			t.Fatal("recording bar did not open", reported)
		}
		w := d.recording
		if w.bar.Overlaps(region) || !w.bounds.In(monitor) || w.barLocal().Size() != w.barSize() || w.outline == 0 || !d.capturing.Load() || d.active.Load() != 1 {
			t.Fatalf("setup: bar %v region %v active %d", w.bounds, region, d.active.Load())
		}
		w.activate("fps")
		w.activate("scale")
		if p := loadPreferences().Recording; p.GIFFPS != d.preferences.Recording.GIFFPS || p.Scale != d.preferences.Recording.Scale {
			t.Fatal("setup choices were not saved")
		}
		snapshot("record-" + name + "-setup")
		// Tab skips the disabled pause button; Enter acts on the focused item.
		if w.items()[w.focus].id != "start" {
			t.Fatal("the record button is not the default focus")
		}
		w.handle(wmKeyDown, 0x09, 0)
		if w.items()[w.focus].id != "rect" || !w.keyboard { // pause and GIF sound are disabled
			t.Fatalf("Tab moved the focus to %q", w.items()[w.focus].id)
		}
		fps := d.preferences.Recording.GIFFPS
		w.focus = w.itemIndex("fps")
		w.handle(wmKeyDown, 0x0d, 0)
		if w.state != recordSetup || d.preferences.Recording.GIFFPS == fps {
			t.Fatal("Enter on the frame rate must change it, not start")
		}
		// Dragging the move button shifts region, outline and bar together.
		before, barBefore := w.region, w.bar
		w.dragMode, w.dragStart, w.dragBar, w.dragRegion = 2, cursorPoint().Sub(image.Pt(24, 16)), w.bar, w.region
		w.drag()
		w.dragMode = 0
		var outline rect
		call(user32, "GetWindowRect", w.outline, ptr(&outline))
		if w.region != before.Add(image.Pt(24, 16)) || w.bar != barBefore.Add(image.Pt(24, 16)) || outline.image() != w.region.Inset(-sourceFrameWidth) {
			t.Fatalf("move: region %v bar %v outline %v", w.region, w.bar, outline.image())
		}
		w.focus = w.itemIndex("start")
		w.handle(wmKeyDown, 0x0d, 0)
		if w.state != recordRunning || w.handle(0x0021, 0, 0) != 3 {
			t.Fatal("recording did not start or the bar would take focus on click")
		}
		pumpRecordMessages(t, d, func() bool { return w.rec.elapsed.Load() > int64(300*time.Millisecond) })
		snapshot("record-" + name + "-running")
		w.activate("pause")
		if !w.rec.paused.Load() || w.items()[w.itemIndex("pause")].icon != "start" || w.items()[w.itemIndex("move")].focusable() {
			t.Fatal("pause")
		}
		snapshot("record-" + name + "-paused")
		w.activate("pause")
		d.command("record-stop")
		if w.state != recordSaving {
			t.Fatal("stop command ignored")
		}
		pumpRecordMessages(t, d, func() bool { return w.state == recordDone })
		snapshot("record-" + name + "-done")
		if w.outline != 0 || d.capturing.Load() || d.active.Load() != 1 || w.itemIndex("open") < 0 || w.barLocal().Size() != w.barSize() {
			t.Fatalf("done: outline %d capturing %v active %d", w.outline, d.capturing.Load(), d.active.Load())
		}
		data, err := os.ReadFile(w.path)
		if err != nil {
			t.Fatal(err)
		}
		want := recordOutputSize(region.Size(), d.preferences.Recording.Scale)
		if decoded, frames := composeGIF(t, data); len(frames) < 3 || decoded.Config.Width != want.X || decoded.Config.Height != want.Y {
			t.Fatalf("recorded %d frames at %d×%d, want %v", len(frames), decoded.Config.Width, decoded.Config.Height, want)
		}
		if dark {
			w.handle(wmKeyDown, 0x1b, 0)
		}
		// In the light pass the finished bar stays: the next recording replaces it.
	}
	if d.recording != nil || d.active.Load() != 0 {
		t.Fatalf("closed: active %d", d.active.Load())
	}

	// Cancel during the countdown and while running leaves no file behind.
	d.preferences.Recording.Countdown = true
	d.recordDir = t.TempDir()
	d.startRecording(region, 0)
	w := d.recording
	w.activate("start")
	if w.state != recordCountdown || w.countdown != 3 {
		t.Fatal("countdown")
	}
	d.command("record-stop")
	if d.recording != nil {
		t.Fatal("stop during the countdown must cancel")
	}
	d.startRecording(region, 0)
	w = d.recording
	w.activate("start")
	w.activate("start") // skip the countdown
	pumpRecordMessages(t, d, func() bool { return w.rec.elapsed.Load() > 0 })
	rec := w.rec
	w.activate("cancel")
	waitRecorder(t, rec)
	pumpRecordMessages(t, d, func() bool { return d.active.Load() == 0 })
	if finished, partial := recordedFiles(t, d.recordDir); len(finished)+len(partial) != 0 || d.capturing.Load() {
		t.Fatalf("cancel left %v %v", finished, partial)
	}

	// Shutdown completes a running recording instead of losing it.
	d.preferences.Recording.Countdown = false
	d.startRecording(region, 0)
	d.recording.activate("start")
	rec = d.recording.rec
	pumpRecordMessages(t, d, func() bool { return rec.elapsed.Load() > int64(150*time.Millisecond) })
	d.recording.close(false)
	if finished, partial := recordedFiles(t, d.recordDir); len(finished) != 1 || len(partial) != 0 {
		t.Fatalf("shutdown kept %v %v", finished, partial)
	}

	// MP4 with sound through the bar: format and audio choices, then a real encode.
	d.recordDir = t.TempDir()
	d.recordAudio = func(bool) (audioSource, error) { return &toneSource{}, nil }
	d.startRecording(region.Add(image.Pt(1, 1)).Inset(-1), 0) // 322×202 stays even, the origin is odd
	w = d.recording
	item := func(id string) recordItem { return w.items()[w.itemIndex(id)] }
	if !item("audio").disabled || item("format").label != "GIF" {
		t.Fatal("GIF must not offer sound")
	}
	w.activate("format")
	if !d.preferences.Recording.mp4() || item("audio").disabled || item("fps").label != "30FPS" || !loadPreferences().Recording.mp4() {
		t.Fatalf("format switch: %+v", d.preferences.Recording)
	}
	w.toggleAudio(false)
	if !d.preferences.Recording.Microphone || !strings.Contains(item("audio").hint, "麦克风 开") {
		t.Fatal("microphone choice")
	}
	w.toggleAudio(true)
	w.toggleAudio(false)
	if item("audio").icon != "mute" {
		t.Fatal("no sound selected must show the muted icon")
	}
	w.toggleAudio(true)
	w.toggleAudio(false)
	// Marks: drawn through the overlay's own messages, visible to a screen
	// capture, click-through again once the tool is released.
	point := func(x, y int) uintptr { return uintptr(uint32(y)<<16 | uint32(x)) }
	exstyle := func() uintptr { return call(user32, "GetWindowLongPtrW", w.overlay, coord(-20)) }
	if item("undo").focusable() || item("clear").focusable() || w.overlay != 0 {
		t.Fatal("marks before any tool")
	}
	w.activate("rect")
	if !item("rect").active || w.overlay == 0 || exstyle()&wsExTransparent != 0 || exstyle()&wsExNoactivate == 0 || w.handleMarks(0x0021, 0, 0) != 3 {
		t.Fatal("an active tool must take the mouse without taking focus")
	}
	w.handleMarks(wmLButtonDown, 0, point(40, 30))
	w.handleMarks(wmMouseMove, 0, point(200, 120))
	w.handleMarks(wmLButtonUp, 0, point(200, 120))
	w.handleMarks(wmLButtonDown, 0, point(60, 60)) // a click without movement adds nothing
	w.handleMarks(wmLButtonUp, 0, point(60, 60))
	w.activate("number")
	w.handleMarks(wmLButtonDown, 0, point(260, 60))
	w.handleMarks(wmLButtonDown, 0, point(260, 140))
	if len(w.marks) != 3 || w.marks[2].text != "2" || !item("undo").focusable() {
		t.Fatalf("marks %+v", w.marks)
	}
	w.activate("undo")
	w.handleMarks(wmRButtonUp, 0, 0)
	if len(w.marks) != 2 || w.tool != "" || exstyle()&wsExTransparent == 0 {
		t.Fatal("undo / leaving the tool")
	}
	snapshot("record-marks-bar")
	for i := 0; i < 10; i++ {
		pumpRecordMessages(t, d, func() bool { return true })
		call(dwmapi, "DwmFlush")
	}
	shot, err := captureScreen(w.region)
	if err != nil {
		t.Fatal(err)
	}
	ink := d.theme.Danger
	for _, p := range []image.Point{{40, 30}, {120, 31}, {198, 118}, {250, 60}} {
		if c := shot.RGBAAt(p.X, p.Y); uint32(c.R)<<16|uint32(c.G)<<8|uint32(c.B) != ink {
			t.Fatalf("mark pixel %v = %v, want %06x", p, c, ink)
		}
	}
	// Moving the region carries the marks; clearing hides the overlay.
	w.dragMode, w.dragStart, w.dragBar, w.dragRegion = 2, cursorPoint().Sub(image.Pt(10, 6)), w.bar, w.region
	w.drag()
	w.dragMode = 0
	var moved rect
	call(user32, "GetWindowRect", w.overlay, ptr(&moved))
	if moved.image() != w.region {
		t.Fatalf("overlay %v, region %v", moved.image(), w.region)
	}
	w.activate("clear")
	if len(w.marks) != 0 || call(user32, "IsWindowVisible", w.overlay) != 0 {
		t.Fatal("clear must hide the overlay")
	}
	w.activate("pen")
	w.handleMarks(wmLButtonDown, 0, point(20, 20))
	w.handleMarks(wmMouseMove, 0, point(30, 40))
	w.handleMarks(wmMouseMove, 0, point(50, 45))
	w.handleMarks(wmLButtonUp, 0, point(50, 45))
	if len(w.marks) != 1 || len(w.marks[0].points) != 4 {
		t.Fatalf("pen stroke %+v", w.marks)
	}
	snapshot("record-mp4-setup")
	w.activate("start")
	began := time.Now()
	pumpRecordMessages(t, d, func() bool { return w.rec.elapsed.Load() > 0 })
	t.Logf("MP4 encoder accepted the first frame after %v", time.Since(began).Round(time.Millisecond))
	pumpRecordMessages(t, d, func() bool { return w.rec.elapsed.Load() > int64(600*time.Millisecond) })
	if len(w.rec.audio) != 2 || item("format").focusable() || item("audio").focusable() {
		t.Fatal("running MP4 state")
	}
	snapshot("record-mp4-running")
	w.activate("stop")
	pumpRecordMessages(t, d, func() bool { return w.state == recordDone })
	data, err := os.ReadFile(w.path)
	if w.overlay != 0 || w.tool != "" {
		t.Fatal("marks must be gone once the recording is saved")
	}
	if err != nil || !strings.HasSuffix(w.path, ".mp4") || len(data) < 1000 || string(data[4:8]) != "ftyp" || w.format != "mp4" {
		t.Fatalf("mp4 result %q: %v", w.path, err)
	}
	w.close(false)
	if len(reported) != 1 || d.active.Load() != 0 {
		t.Fatal("unexpected errors:", reported, d.active.Load())
	}
}

// Opt-in: the GDI source must return the same pixels as a screenshot, scale
// with halftone, and leave out the bar that overlaps the recorded region.
func TestNativeScreenSourceMatchesDesktop(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native recording QA")
	}
	t.Setenv("LOCALAPPDATA", t.TempDir())
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d, err := newDesktop()
	if err != nil {
		t.Fatal(err)
	}
	d.onError = func(err error) { t.Errorf("native error: %v", err) }
	defer func() { d.shutdown(); call(ole32, "OleUninitialize") }()
	d.applyTheme(testTheme(false))
	monitor := currentMonitor(cursorPoint())
	// A pinned test card gives known, static pixels under the capture.
	card := image.NewRGBA(image.Rect(0, 0, 240, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 240; x++ {
			card.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	origin := monitor.Min.Add(image.Pt(40, 40))
	region := image.Rectangle{Min: origin, Max: origin.Add(card.Rect.Size())}
	pin, err := d.openPin(card, region)
	if err != nil {
		t.Fatal(err)
	}
	pin.state.Shadow = false
	pin.syncWindow()
	// The whole monitor is the region, so the bar must sit on top of it.
	if !d.startRecording(monitor, 0) || !d.recording.bar.In(monitor) {
		t.Fatal("full-monitor recording bar")
	}
	bar := d.recording.bar
	var msg message
	for i := 0; i < 20; i++ {
		for call(user32, "PeekMessageW", ptr(&msg), 0, 0, 0, 1) != 0 {
			call(user32, "DispatchMessageW", ptr(&msg))
		}
		call(dwmapi, "DwmFlush")
	}
	source, err := newScreenSource(region, region.Size(), false)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, region.Dx()*region.Dy()*4)
	if err := source.grab(frame); err != nil {
		t.Fatal(err)
	}
	source.close()
	for y := 0; y < 160; y++ {
		for x := 0; x < 240; x++ {
			i, c := (y*240+x)*4, card.RGBAAt(x, y)
			if frame[i] != c.B || frame[i+1] != c.G || frame[i+2] != c.R {
				t.Fatalf("pixel %d,%d = %v, want %v", x, y, frame[i:i+3], c)
			}
		}
	}
	half, err := newScreenSource(region, image.Pt(120, 80), true)
	if err != nil {
		t.Fatal(err)
	}
	if half.size() != image.Pt(120, 80) || half.grab(frame[:120*80*4]) != nil {
		t.Fatal("scaled source")
	}
	half.close()
	// Where the bar is, the recording shows the desktop behind it.
	behind, err := captureScreen(bar)
	if err != nil {
		t.Fatal(err)
	}
	theme := d.theme
	accent := 0
	for i := 0; i < len(behind.Pix); i += 4 {
		if uint32(behind.Pix[i])<<16|uint32(behind.Pix[i+1])<<8|uint32(behind.Pix[i+2]) == theme.Accent {
			accent++
		}
	}
	if accent > 50 {
		t.Fatalf("the control bar leaked into the capture: %d accent pixels", accent)
	}
	d.recording.close(true)
}
