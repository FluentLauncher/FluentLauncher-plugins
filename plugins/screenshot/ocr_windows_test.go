//go:build windows

package main

import (
	"bytes"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOCRTextKeepsLatinSpacesAndJoinsChinese(t *testing.T) {
	for in, want := range map[string]string{
		"截 图 文 字 识 别":        "截图文字识别",
		"  Hello World  ":    "Hello World",
		"使 用 Go 1.27 编 译":    "使用 Go 1.27 编译",
		"你 好 ， 世 界 。":        "你好，世界。",
		"版 本 1.10.0 （ 测 试 ）": "版本 1.10.0（测试）",
		"ひ ら が な と カ タ カ ナ":  "ひらがなとカタカナ",
		"한국어 띄어쓰기":           "한국어 띄어쓰기",
		"a  b":               "a  b",
	} {
		if got := tidyOCRLine(in); got != want {
			t.Errorf("tidyOCRLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := joinOCRLines([]string{" 第 一 行 ", "", "   ", "second line"}); got != "第一行\r\nsecond line" {
		t.Fatalf("lines: %q", got)
	}
}

func squeeze(text string) string { return strings.ReplaceAll(text, " ", "") }

// ocrFixture draws text the way an application would: the system UI font at
// the system DPI, dark on light. Only synthetic content is recognized.
func ocrFixture(t *testing.T, lines []string) *image.RGBA {
	t.Helper()
	size := image.Pt(520, 30+len(lines)*34)
	b, err := newBitmap(image.NewRGBA(image.Rectangle{Max: size}))
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	dc := call(gdi32, "CreateCompatibleDC", 0)
	old := call(gdi32, "SelectObject", dc, b.handle)
	fill(dc, image.Rectangle{Max: size}, rgbRef(0xffffff))
	for i, line := range lines {
		textAligned(dc, line, image.Rect(20, 15+i*34, size.X-20, 15+(i+1)*34), rgbRef(0x18181b), false)
	}
	img, err := b.pixels()
	call(gdi32, "SelectObject", dc, old)
	call(gdi32, "DeleteDC", dc)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// The only check that the Windows Runtime calls use the right interfaces and
// vtable slots: recognize rendered text with the engine installed on this PC.
func TestNativeOCRRecognizesRenderedText(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native OCR check")
	}
	img := ocrFixture(t, []string{"Fluent Launcher 2026", "Screenshot OCR test", "截图文字识别测试"})
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "ocr-fixture.png"), img, false); err != nil {
			t.Fatal(err)
		}
	}
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		text, err := recognizeText(img)
		done <- outcome{text, err}
	}()
	got := <-done
	if errors.Is(got.err, errNoOCRLanguage) {
		t.Skip("no OCR language is installed on this system")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Logf("recognized: %q", got.text)
	lines := strings.Split(got.text, "\r\n")
	if len(lines) != 3 {
		t.Fatalf("expected three lines, got %d: %q", len(lines), got.text)
	}
	// The engine sometimes splits a word ("te st"); that is its accuracy, not
	// the call path under test, so spaces are ignored here.
	for _, want := range []string{"FluentLauncher2026", "ScreenshotOCRtest"} {
		if !strings.Contains(squeeze(got.text), want) {
			t.Errorf("missing %q in %q", want, got.text)
		}
	}
	// Chinese needs a Chinese recognizer; other languages only prove the call path.
	if strings.ContainsRune(got.text, '截') && !strings.Contains(got.text, "截图文字识别测试") {
		t.Errorf("Chinese line was not joined or recognized: %q", got.text)
	}
	// A blank image is a clear message, not an empty clipboard.
	blank := image.NewRGBA(image.Rect(0, 0, 200, 120))
	for i := range blank.Pix {
		blank.Pix[i] = 255
	}
	if text, err := recognizeText(blank); err == nil {
		t.Fatalf("blank image produced text %q", text)
	}
}

// The shipped path from the toolbar button to the text that would reach the
// clipboard, using the real recognizer. The clipboard itself is replaced so
// the user's content is never touched; pasting uses a synthetic image.
func TestNativeOCRAndClipboardPinFlow(t *testing.T) {
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
	var copied []string
	d.textOut = func(text string) error { copied = append(copied, text); return nil }
	defer func() {
		d.shutdown()
		call(ole32, "OleUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	await := func(what string, done func() bool) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for !done() {
			pumpLongMessages()
			if time.Now().After(deadline) {
				t.Fatal("timed out waiting for " + what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	index := -1
	for i, tool := range captureTools {
		if tool.id == "ocr" {
			index = i
		}
	}
	if index < 0 {
		t.Fatal("toolbar has no text recognition button")
	}
	img := ocrFixture(t, []string{"Fluent Launcher 2026", "Screenshot OCR test"})
	origin := currentMonitor(cursorPoint()).Min.Add(image.Pt(80, 80))
	open := func() *selectionWindow {
		if err := d.openSelection(img, img.Bounds().Add(origin), false, previous); err != nil {
			t.Fatal(err)
		}
		d.selection.selected = img.Bounds()
		return d.selection
	}

	// A mark over the text must not reach the recognizer.
	s := open()
	s.marks = []annotation{{kind: "rect", points: []image.Point{image.Pt(0, 0), image.Pt(500, 90)}, color: 0xff0000, width: 30}}
	var seen *image.RGBA
	d.ocr = func(in *image.RGBA) (string, error) { seen = in; return recognizeText(in) }
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		s.toolbarFocus = index
		invalidate(s.hwnd)
		call(user32, "UpdateWindow", s.hwnd)
		shot, err := s.paintBuffer.pixels()
		if err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "ocr-toolbar.png"), shot, false); err != nil {
			t.Fatal(err)
		}
	}
	s.activateTool(index)
	if !s.busy {
		t.Fatal("recognition did not start from the toolbar")
	}
	await("recognition", func() bool { return d.selection == nil || len(reported) > 0 })
	if errors.Is(errors.Join(reported...), errNoOCRLanguage) {
		t.Skip("no OCR language is installed on this system")
	}
	if len(reported) > 0 || len(copied) != 1 || !strings.Contains(squeeze(copied[0]), "FluentLauncher2026") || !strings.Contains(squeeze(copied[0]), "ScreenshotOCRtest") {
		t.Fatalf("copied %q, errors %v", copied, reported)
	}
	if seen == nil || !bytes.Equal(seen.Pix, img.Pix) {
		t.Fatal("recognizer did not receive the unannotated selection")
	}
	if d.active.Load() != 0 {
		t.Fatal("finished recognition left the plugin active")
	}

	// Failure keeps the selection for another try and copies nothing.
	s = open()
	d.ocr = func(*image.RGBA) (string, error) {
		return "", errors.New("没有识别到文字，请调整选区后重试")
	}
	s.handle(wmKeyDown, 'O', 0)
	await("failure", func() bool { return len(reported) == 1 })
	if d.selection != s || s.busy || len(copied) != 1 {
		t.Fatal("failed recognition closed the selection or copied text")
	}
	// The text command makes recognition the default action and the accented button.
	reported = nil
	s.preferOCR = true
	d.ocr = func(*image.RGBA) (string, error) { return "默认动作", nil }
	if s.defaultOutput() != "ocr" {
		t.Fatal("text command does not default to recognition")
	}
	s.handle(wmKeyDown, 0x0D, 0)
	await("default action", func() bool { return d.selection == nil })
	if len(copied) != 2 || copied[1] != "默认动作" {
		t.Fatal("Enter did not recognize:", copied)
	}

	// Paste a picture: centered on the pointer's monitor, original pixels kept.
	pasted := scrollFixture(300, 200)
	d.pastedImage = func() (*image.RGBA, error) { return pasted, nil }
	d.command("pin-clipboard")
	await("clipboard pin", func() bool { return len(d.pins) == 1 })
	monitor := currentMonitor(cursorPoint())
	for _, p := range d.pins {
		if p.state.Bounds != pastedPinBounds(image.Pt(300, 200), monitor) || p.state.Original.Size() != image.Pt(300, 200) || p.image != pasted {
			t.Fatal("pin placement or pixels:", p.state.Bounds, p.state.Original)
		}
	}
	d.pastedImage = func() (*image.RGBA, error) { return nil, errors.New("剪贴板里没有图片") }
	d.command("pin-clipboard")
	await("empty clipboard message", func() bool { return len(reported) == 1 })
	if len(d.pins) != 1 {
		t.Fatal("an empty clipboard created a pin")
	}
	d.command("close")
}
