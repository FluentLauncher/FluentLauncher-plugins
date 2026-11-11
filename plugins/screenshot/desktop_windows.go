//go:build windows

package main

import (
	"errors"
	"image"
	"image/draw"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
	"golang.org/x/sys/windows"
)

type desktop struct {
	controller  uintptr
	jobs        chan func()
	done        chan struct{}
	active      atomic.Int32
	capturing   atomic.Bool
	pins        map[uintptr]*pinWindow
	pinOrder    []uintptr // back to front, preserved while hidden or excluded from capture
	selection   *selectionWindow
	scrolling   *scrollWindow
	preferences preferences
	theme       pluginsdk.Theme
	popups      map[uintptr]*popupWindow
	menuRoot    *popupWindow
	tempDir     string
	onError     func(error)                                // controlled native tests can inspect errors without a modal dialog
	captureLong func(image.Rectangle) (*image.RGBA, error) // nil uses the desktop; native QA may render its own controlled target
}

// Accessed only on the locked GUI thread, including synchronous Win32 callbacks.
var gui *desktop

func newDesktop() (*desktop, error) {
	call(user32, "SetProcessDpiAwarenessContext", ^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	call(ole32, "OleInitialize", 0)
	d := &desktop{jobs: make(chan func(), 64), done: make(chan struct{}), pins: make(map[uintptr]*pinWindow), preferences: loadPreferences(), theme: defaultTheme(), popups: make(map[uintptr]*popupWindow)}
	gui = d
	callback := windows.NewCallback(windowProcedure)
	for _, name := range []string{"FLScreenshotController", "FLScreenshotSelection", "FLScreenshotPin", "FLScreenshotShadow", "FLScreenshotMenu", "FLScreenshotLong", "FLScreenshotOutline"} {
		wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Style: 0x0008, Proc: callback,
			Instance: call(kernel32, "GetModuleHandleW", 0), Cursor: call(user32, "LoadCursorW", 0, 32512), Name: u16(name)}
		if call(user32, "RegisterClassExW", ptr(&wc)) == 0 {
			existing := wndClass{Size: uint32(unsafe.Sizeof(wndClass{}))}
			if call(user32, "GetClassInfoExW", wc.Instance, ptr(wc.Name), ptr(&existing)) == 0 {
				return nil, errors.New("无法注册截图窗口")
			}
		}
	}
	d.controller = d.createWindow("FLScreenshotController", "FluentLauncher 截图", 0, 0, image.Rect(0, 0, 1, 1))
	if d.controller == 0 {
		return nil, errors.New("无法创建截图控制窗口")
	}
	return d, nil
}

func (d *desktop) createWindow(class, title string, exstyle, owner uintptr, bounds image.Rectangle) uintptr {
	hwnd := call(user32, "CreateWindowExW", exstyle, ptr(u16(class)), ptr(u16(title)), wsPopup,
		coord(bounds.Min.X), coord(bounds.Min.Y), bounds.Dx(), bounds.Dy(), owner, 0, call(kernel32, "GetModuleHandleW", 0), 0)
	if hwnd != 0 {
		disabled := uint32(1)
		call(dwmapi, "DwmSetWindowAttribute", hwnd, 3, ptr(&disabled), 4)
	}
	return hwnd
}

func (d *desktop) post(fn func()) error {
	select {
	case <-d.done:
		return errors.New("截图插件已关闭")
	default:
	}
	select {
	case d.jobs <- fn:
		if call(user32, "PostMessageW", d.controller, wmApp, 0, 0) == 0 {
			return errors.New("无法发送截图窗口消息")
		}
		return nil
	case <-d.done:
		return errors.New("截图插件已关闭")
	default:
		return errors.New("截图操作过多，请稍后重试")
	}
}

func (d *desktop) run() {
	defer call(ole32, "OleUninitialize")
	var msg message
	for {
		r := int32(call(user32, "GetMessageW", ptr(&msg), 0, 0, 0))
		if r <= 0 {
			break
		}
		call(user32, "TranslateMessage", ptr(&msg))
		call(user32, "DispatchMessageW", ptr(&msg))
	}
}

func windowProcedure(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	d := gui
	if d == nil {
		return call(user32, "DefWindowProcW", hwnd, msg, wparam, lparam)
	}
	if hwnd == d.controller && msg == wmApp {
		for {
			select {
			case job := <-d.jobs:
				job()
			default:
				return 0
			}
		}
	}
	if s := d.selection; s != nil && hwnd == s.hwnd {
		return s.handle(msg, wparam, lparam)
	}
	if s := d.scrolling; s != nil && hwnd == s.hwnd {
		return s.handle(msg, wparam, lparam)
	}
	if s := d.scrolling; s != nil && hwnd == s.outline {
		return s.paintSourceFrame(msg, wparam, lparam)
	}
	if p := d.popups[hwnd]; p != nil {
		return p.handle(msg, wparam, lparam)
	}
	if p := d.pins[hwnd]; p != nil {
		return p.handle(msg, wparam, lparam)
	}
	return call(user32, "DefWindowProcW", hwnd, msg, wparam, lparam)
}

func (d *desktop) shutdown() {
	select {
	case <-d.done:
		return
	default:
		close(d.done)
	}
	if d.selection != nil {
		d.selection.close()
	}
	if d.scrolling != nil {
		d.scrolling.close(true)
	}
	d.closeMenu()
	for _, p := range d.pins {
		p.close()
	}
	if d.tempDir != "" {
		if err := os.RemoveAll(d.tempDir); err != nil {
			log.Print("cleanup screenshot exports: ", err)
		}
	}
	call(user32, "DestroyWindow", d.controller)
	call(user32, "PostQuitMessage", 0)
}

func (d *desktop) report(owner uintptr, err error) {
	if err == nil {
		return
	}
	log.Print(err)
	if d.onError != nil {
		d.onError(err)
		return
	}
	call(user32, "MessageBoxW", owner, ptr(u16(err.Error())), ptr(u16("截图与贴图")), 0x10|0x40000)
}

func (d *desktop) command(id string) {
	switch id {
	case "show", "hide":
		for _, hwnd := range d.pinOrder {
			p := d.pins[hwnd]
			p.state.Hidden = id == "hide"
			p.syncWindow()
		}
	case "close":
		for _, p := range d.pins {
			p.close()
		}
	case "settings":
		d.settingsMenu()
	case "folder":
		d.active.Add(1)
		go func() {
			dir, err := screenshotDirectory()
			if err == nil {
				err = os.MkdirAll(dir, 0755)
			}
			if err == nil && call(shell32, "ShellExecuteW", 0, ptr(u16("open")), ptr(u16(dir)), 0, 0, 1) <= 32 {
				err = errors.New("无法打开截图保存目录")
			}
			_ = d.post(func() { defer d.active.Add(-1); d.report(d.controller, err) })
		}()
	}
}

func (d *desktop) startCapture(id string) {
	if d.selection != nil {
		d.capturing.Store(false)
		return
	}
	d.closeMenu()
	originalFocus := call(user32, "GetForegroundWindow")
	bounds := virtualBounds()
	if id == "screen" {
		bounds = currentMonitor(cursorPoint())
	}
	excludePins := !d.preferences.IncludePins
	if excludePins {
		for _, p := range d.pins {
			call(user32, "ShowWindow", p.hwnd, 0)
			call(user32, "ShowWindow", p.shadow, 0)
		}
	}
	// The RPC returns before capturing. Let the host hide its panel, then wait
	// for DWM to present the composite before copying the physical desktop.
	go func() {
		time.Sleep(100 * time.Millisecond)
		call(dwmapi, "DwmFlush")
		img, err := captureScreen(bounds)
		_ = d.post(func() {
			if excludePins {
				for _, hwnd := range d.pinOrder {
					p := d.pins[hwnd]
					p.syncWindow()
				}
			}
			if err != nil {
				defer d.capturing.Store(false)
				d.report(d.controller, err)
				return
			}
			if id == "region" || id == "region-pin" || id == "scroll-auto" || id == "scroll-manual" {
				if err := d.openSelection(img, bounds, id == "region-pin", originalFocus); err != nil {
					d.capturing.Store(false)
					d.report(d.controller, err)
				}
				if d.selection != nil {
					invalidate(d.selection.hwnd)
				}
				return
			}
			d.outputImage(img, bounds, id == "quick-save", func(err error) {
				defer d.capturing.Store(false)
				if err != nil {
					_, pinErr := d.openPin(img, bounds)
					d.report(d.controller, errors.Join(err, pinErr))
				}
			})
		})
	}()
}

func (d *desktop) outputImage(img *image.RGBA, bounds image.Rectangle, quickSave bool, complete func(error)) {
	d.active.Add(1)
	go func() {
		var err error
		if quickSave {
			var dir string
			dir, err = screenshotDirectory()
			if err == nil {
				err = os.MkdirAll(dir, 0755)
			}
			if err == nil {
				err = savePNG(filepath.Join(dir, screenshotName(time.Now())), img, true)
			}
		} else {
			err = copyImage(d.controller, img)
		}
		_ = d.post(func() { defer d.active.Add(-1); complete(err) })
	}()
}

func crop(img *image.RGBA, r image.Rectangle) *image.RGBA {
	r = r.Intersect(img.Bounds())
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(out, out.Bounds(), img, r.Min, draw.Src)
	return out
}

func (d *desktop) saveAs(owner uintptr, img *image.RGBA, complete func(error, bool)) {
	path, err := chooseSavePath(owner)
	if err != nil || path == "" {
		complete(err, false)
		return
	}
	d.active.Add(1)
	go func() {
		err := savePNG(path, img, false)
		_ = d.post(func() { defer d.active.Add(-1); complete(err, err == nil) })
	}()
}

func (d *desktop) settingsMenu() {
	d.showMenu(d.controller, cursorPoint(), []menuItem{
		{ID: 1, Label: "再次截图时包含贴图", Checked: d.preferences.IncludePins},
		{ID: 2, Label: "新贴图显示阴影", Checked: d.preferences.NewPinShadow},
	}, func(id int) {
		previous := d.preferences
		if id == 1 {
			d.preferences.IncludePins = !d.preferences.IncludePins
		} else {
			d.preferences.NewPinShadow = !d.preferences.NewPinShadow
		}
		if err := savePreferences(d.preferences); err != nil {
			d.preferences = previous
			d.report(d.controller, err)
		}
	})
}
