//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func TestQueryAndActivity(t *testing.T) {
	d := &desktop{}
	h := handler{d}
	rows, err := h.Query(context.Background(), pluginsdk.Query{Text: "区域", MaxResults: 20})
	if err != nil || len(rows) != 2 {
		t.Fatalf("region menu: %v %v", rows, err)
	}
	if err := h.ExecuteCommand(context.Background(), pluginsdk.ExecuteCommandRequest{CommandID: "missing"}); err == nil {
		t.Fatal("unknown command accepted")
	}
	if active, _ := h.HasActivity(context.Background()); active {
		t.Fatal("new plugin active")
	}
	d.active.Add(1)
	if active, _ := h.HasActivity(context.Background()); !active {
		t.Fatal("hidden pin not active")
	}
	d.active.Add(-1)
	d.capturing.Store(true)
	if active, _ := h.HasActivity(context.Background()); !active {
		t.Fatal("capture not active")
	}
}

func TestPreferencesAndPNG(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	if p := loadPreferences(); !p.IncludePins || !p.NewPinShadow {
		t.Fatal(p)
	}
	if err := savePreferences(preferences{false, false}); err != nil {
		t.Fatal(err)
	}
	if p := loadPreferences(); p.IncludePins || p.NewPinShadow {
		t.Fatal(p)
	}
	if err := savePreferences(preferences{true, false}); err != nil {
		t.Fatal("replace preferences:", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{R: 245, A: 255})
	path := filepath.Join(t.TempDir(), screenshotName(time.Now()))
	if err := savePNG(path, img, true); err != nil {
		t.Fatal(err)
	}
	if err := savePNG(path, img, true); err == nil {
		t.Fatal("quick save overwrote file")
	}
	data, _ := os.ReadFile(path)
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil || color.RGBAModel.Convert(decoded.At(0, 0)) != img.At(0, 0) {
		t.Fatalf("PNG round trip: %v", err)
	}
	if unsafe.Sizeof(openFilename{}) != 152 || unsafe.Sizeof(message{}) != 48 || unsafe.Sizeof(wndClass{}) != 80 {
		t.Fatal("Windows x64 ABI layout")
	}
	data, _ = os.ReadFile("plugin.json")
	var manifest struct {
		ID       string
		Commands []struct{ ID string }
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.ID != "official.screenshot" {
		t.Fatal("manifest", err)
	}
	for _, cmd := range manifest.Commands {
		found := false
		for _, entry := range entries {
			if cmd.ID == entry.id {
				found = true
			}
		}
		if !found {
			t.Fatalf("manifest command %s is unreachable", cmd.ID)
		}
	}
}

// Opt-in: shows synthetic test windows briefly. It never sends desktop input,
// writes the user's clipboard, saves their screen, or reads launcher settings.
func TestNativePinCompositeAndKeyboard(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("set FL_SCREENSHOT_NATIVE_TEST=1 for controlled native-window QA")
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
		call(ole32, "CoUninitialize")
		if previous != 0 {
			call(user32, "SetForegroundWindow", previous)
		}
	}()
	monitor := currentMonitor(cursorPoint())
	origin := monitor.Min.Add(image.Pt(60, 60))
	baseImg := image.NewRGBA(image.Rect(0, 0, 640, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 640; x++ {
			baseImg.SetRGBA(x, y, color.RGBA{R: uint8(160 + x%40), G: 215, B: 235, A: 255})
		}
	}
	base, err := d.openPin(baseImg, image.Rectangle{Min: origin, Max: origin.Add(baseImg.Rect.Size())})
	if err != nil {
		t.Fatal(err)
	}
	base.state.Shadow = false
	base.syncWindow()
	img := image.NewRGBA(image.Rect(0, 0, 240, 140))
	for y := 0; y < 140; y++ {
		for x := 0; x < 240; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 40, G: uint8(80 + x/3), B: 185, A: 255})
		}
	}
	bounds := image.Rectangle{Min: origin.Add(image.Pt(100, 100)), Max: origin.Add(image.Pt(340, 240))}
	p, err := d.openPin(img, bounds)
	if err != nil {
		t.Fatal(err)
	}
	pump := func() {
		var msg message
		for call(user32, "PeekMessageW", ptr(&msg), 0, 0, 0, 1) != 0 {
			if msg.ID != 0x12 {
				call(user32, "TranslateMessage", ptr(&msg))
				call(user32, "DispatchMessageW", ptr(&msg))
			}
		}
		call(user32, "UpdateWindow", base.hwnd)
		call(user32, "UpdateWindow", p.hwnd)
		call(dwmapi, "DwmFlush")
	}
	pump()
	before, err := captureScreen(p.state.Bounds)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before.Pix, img.Pix) {
		t.Fatal("original-position pin did not cover source pixels exactly")
	}
	call(user32, "SendMessageW", p.hwnd, wmKeyDown, 0x27, 0)
	if p.state.Bounds != bounds.Add(image.Pt(1, 0)) {
		t.Fatal("native right arrow did not move one physical pixel")
	}
	p.state.Locked = true
	call(user32, "SendMessageW", p.hwnd, wmKeyDown, 0x25, 0)
	if p.state.Bounds != bounds.Add(image.Pt(1, 0)) {
		t.Fatal("locked pin moved")
	}
	p.state.Locked = false
	p.applyMenu(6)
	if p.state.Bounds != bounds {
		t.Fatal("original position not restored")
	}
	pump()
	composite, err := captureScreen(base.state.Bounds)
	if err != nil {
		t.Fatal(err)
	}
	if composite.RGBAAt(110, 110) != img.RGBAAt(10, 10) {
		t.Fatal("second capture excluded existing pin")
	}
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "pin-shadow.png"), composite, false); err != nil {
			t.Fatal(err)
		}
	}
	p.applyMenu(3)
	pump()
	withoutShadow, err := captureScreen(base.state.Bounds)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(composite.Pix, withoutShadow.Pix) {
		t.Fatal("shadow toggle did not change the desktop composite")
	}
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		if err := savePNG(filepath.Join(dir, "pin-no-shadow.png"), withoutShadow, false); err != nil {
			t.Fatal(err)
		}
	}
	p.applyMenu(7)
	if active, _ := (handler{d}).HasActivity(context.Background()); !active {
		t.Fatal("hidden images lost activity")
	}
	d.command("show")
	if p.state.Hidden {
		t.Fatal("hidden pin did not return")
	}
	if err := d.openSelection(baseImg, base.state.Bounds, true, 0); err != nil {
		t.Fatal(err)
	}
	s := d.selection
	pack := func(x, y int) uintptr { return uintptr(uint32(uint16(x)) | uint32(uint16(y))<<16) }
	call(user32, "SendMessageW", s.hwnd, wmLButtonDown, 1, pack(30, 40))
	call(user32, "SendMessageW", s.hwnd, wmLButtonUp, 0, pack(130, 100))
	if s.selected != image.Rect(30, 40, 130, 100) {
		t.Fatal("native selection geometry", s.selected)
	}
	call(user32, "UpdateWindow", s.hwnd)
	call(dwmapi, "DwmFlush")
	if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" {
		preview, err := captureScreen(base.state.Bounds)
		if err != nil {
			t.Fatal(err)
		}
		if err := savePNG(filepath.Join(dir, "selection.png"), preview, false); err != nil {
			t.Fatal(err)
		}
	}
	s.output("pin")
	if d.selection != nil || len(d.pins) != 3 {
		t.Fatal("selection did not create a persistent pin")
	}
	d.command("close")
	if active, _ := (handler{d}).HasActivity(context.Background()); active {
		t.Fatal("closed images retained activity")
	}
}
