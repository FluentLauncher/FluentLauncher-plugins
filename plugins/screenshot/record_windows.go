//go:build windows

package main

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

type recordState int

const (
	recordSetup recordState = iota
	recordCountdown
	recordRunning
	recordSaving
	recordDone
)

const (
	recordCountdownTimer = 1
	recordRefreshTimer   = 2
	wmTimer              = 0x0113
)

type recordButton struct{ id, label, icon, hint string }

// recordWindow is the small control bar next to the recorded region. It and
// the outline are excluded from capture, so neither appears in the result.
type recordWindow struct {
	d             *desktop
	hwnd, outline uintptr
	originalFocus uintptr
	material      string
	opaqueButtons []opaqueSurface
	region        image.Rectangle
	bounds        image.Rectangle
	state         recordState
	countdown     int
	rec           *recorder
	path          string
	fileSize      int64
	limited       bool
	busy          bool
	keyboard      bool // Tab was used: describe the focused button
	hover, focus  int
	paintBuffer   paintBuffer
}

// startRecording opens the control bar for a physical-pixel desktop region.
// Recording itself begins from the bar, after the optional countdown.
func (d *desktop) startRecording(region image.Rectangle, focus uintptr) bool {
	if d.recording != nil {
		if d.recording.state != recordDone {
			d.report(d.controller, errors.New("已有录制正在进行，请先停止或取消"))
			return false
		}
		d.recording.close(false)
	}
	if region.Dx() < 32 || region.Dy() < 32 {
		d.report(d.controller, errors.New("录制区域至少需要 32×32 像素，请重新选择"))
		return false
	}
	monitor := currentMonitor(region.Min.Add(region.Size().Div(2)))
	w := &recordWindow{d: d, region: region, originalFocus: focus, hover: -1}
	// Created on the target monitor first: its DPI decides the bar size.
	w.hwnd = d.createWindow("FLScreenshotRecord", "录制", wsExTopmost|wsExToolwindow|wsExLayered, d.controller, image.Rectangle{Min: monitor.Min, Max: monitor.Min.Add(image.Pt(1, 1))})
	if w.hwnd == 0 {
		d.report(d.controller, errors.New("无法创建录制操作条"))
		return false
	}
	scale := uiScale(w.hwnd)
	w.bounds = recordBarBounds(region, monitor, image.Pt(px(340, scale), px(64, scale)), px(10, scale))
	excluded := call(user32, "SetWindowDisplayAffinity", w.hwnd, 0x11) != 0
	if w.bounds.Overlaps(region) && !excluded {
		call(user32, "DestroyWindow", w.hwnd)
		d.report(d.controller, errors.New("系统无法把操作条排除在录制画面之外，请缩小选区，给操作条留出位置"))
		return false
	}
	if w.outline = d.openOutline(region, "录制选区"); w.outline == 0 {
		call(user32, "DestroyWindow", w.hwnd)
		d.report(d.controller, errors.New("无法创建录制选区边框"))
		return false
	}
	d.recording = w
	d.capturing.Store(true)
	d.active.Add(1)
	w.focus = len(w.buttons()) - 1
	w.applyMaterial()
	windowAt(w.hwnd, w.bounds, true)
	call(user32, "SetForegroundWindow", w.hwnd)
	call(user32, "SetFocus", w.hwnd)
	return true
}

func (w *recordWindow) applyMaterial() {
	w.material = applySurfaceMaterial(w.hwnd, w.d.theme)
	radius := px(int(max(0, w.d.theme.Radius)), uiScale(w.hwnd))
	region := call(gdi32, "CreateRoundRectRgn", 0, 0, w.bounds.Dx()+1, w.bounds.Dy()+1, radius*2, radius*2)
	if call(user32, "SetWindowRgn", w.hwnd, region, 0) == 0 {
		call(gdi32, "DeleteObject", region)
	}
	invalidate(w.hwnd)
	if w.outline != 0 {
		invalidate(w.outline)
	}
}

// close removes the bar. An unfinished recording is discarded on request;
// otherwise (plugin shutdown) it is completed so the file stays playable.
func (w *recordWindow) close(discard bool) {
	if w.d.recording != w {
		return
	}
	w.d.recording = nil
	if w.rec != nil && w.state != recordDone {
		w.rec.finish(discard)
		if !discard {
			select {
			case <-w.rec.done:
			case <-time.After(3 * time.Second):
				log.Print("recording did not finish before shutdown; the .partial file is kept")
			}
		}
	}
	call(user32, "KillTimer", w.hwnd, recordCountdownTimer)
	call(user32, "KillTimer", w.hwnd, recordRefreshTimer)
	call(user32, "DestroyWindow", w.hwnd)
	if w.outline != 0 {
		call(user32, "DestroyWindow", w.outline)
	}
	w.paintBuffer.close()
	w.d.active.Add(-1)
	if w.state != recordDone {
		w.d.capturing.Store(false)
	}
}

func (w *recordWindow) setState(state recordState) {
	w.state = state
	w.hover, w.keyboard = -1, false
	w.focus = len(w.buttons()) - 1
	invalidate(w.hwnd)
	if w.outline != 0 {
		invalidate(w.outline)
	}
}

func (w *recordWindow) start() {
	if w.d.preferences.Recording.Countdown && w.state == recordSetup {
		w.countdown = 3
		w.setState(recordCountdown)
		call(user32, "SetTimer", w.hwnd, recordCountdownTimer, 1000, 0)
		return
	}
	call(user32, "KillTimer", w.hwnd, recordCountdownTimer)
	prefs, region := w.d.preferences.Recording, w.region
	out := recordOutputSize(region.Size(), prefs.GIFScale)
	open := func() (frameSource, error) { return newScreenSource(region, out, prefs.Cursor) }
	if w.d.recordSource != nil {
		open = func() (frameSource, error) { return w.d.recordSource(region, out) }
	}
	dir := w.d.recordDir
	if dir == "" {
		var err error
		if dir, err = recordingDirectory(); err != nil {
			w.close(true)
			w.d.report(w.d.controller, err)
			return
		}
	}
	rec := newRecorder(open, dir, prefs.GIFFPS, time.Duration(prefs.GIFMaxSeconds)*time.Second)
	w.rec = rec
	w.setState(recordRunning)
	call(user32, "SetTimer", w.hwnd, recordRefreshTimer, 250, 0)
	// Hand the keyboard back, so the recorded app looks and behaves active.
	if w.originalFocus != 0 && call(user32, "IsWindow", w.originalFocus) != 0 {
		call(user32, "SetForegroundWindow", w.originalFocus)
	}
	w.d.active.Add(1) // keeps the plugin alive until the file is complete or removed
	go func() {
		rec.run()
		w.d.active.Add(-1)
		_ = w.d.post(func() { w.finished(rec) })
	}()
}

func (w *recordWindow) finished(rec *recorder) {
	if w.d.recording != w || w.rec != rec {
		return
	}
	call(user32, "KillTimer", w.hwnd, recordRefreshTimer)
	if rec.err != nil || rec.path == "" {
		w.close(true)
		w.d.report(w.d.controller, rec.err)
		return
	}
	w.path, w.limited = rec.path, rec.limited
	if info, err := os.Stat(rec.path); err == nil {
		w.fileSize = info.Size()
	}
	call(user32, "DestroyWindow", w.outline)
	w.outline = 0
	w.d.capturing.Store(false)
	w.setState(recordDone)
}

// stop serves the global stop command: it saves a running recording and
// backs out of one that has not started.
func (w *recordWindow) stop() {
	switch w.state {
	case recordSetup, recordCountdown:
		w.close(true)
	case recordRunning:
		w.activate("stop")
	}
}

func (w *recordWindow) buttons() []recordButton {
	prefs := w.d.preferences.Recording
	switch w.state {
	case recordSetup:
		return []recordButton{
			{"fps", fmt.Sprintf("%d 帧/秒", prefs.GIFFPS), "", "帧率 · 点击切换 10 / 15 / 20"},
			{"scale", fmt.Sprintf("%d%%", prefs.GIFScale), "", "画面缩放 · 点击切换 100% / 75% / 50%"},
			{"cancel", "取消", "close", "取消 · Esc"},
			{"start", "开始", "record", "开始录制 · Enter"},
		}
	case recordCountdown:
		return []recordButton{{"cancel", "取消", "close", "取消 · Esc"}, {"start", "立即开始", "record", "跳过倒计时"}}
	case recordRunning:
		pause := recordButton{"pause", "暂停", "pause", "暂停录制"}
		if w.rec.paused.Load() {
			pause = recordButton{"pause", "继续", "start", "继续录制"}
		}
		return []recordButton{pause, {"cancel", "取消", "close", "取消并丢弃录制"}, {"stop", "停止", "stop", "停止并保存"}}
	case recordDone:
		return []recordButton{
			{"copy", "", "copy", "复制文件，可粘贴到聊天或文件夹"},
			{"save", "", "save", "另存为"},
			{"folder", "", "folder", "在文件夹中显示"},
			{"cancel", "", "close", "关闭 · Esc"},
			{"open", "", "start", "打开录制结果 · Enter"},
		}
	}
	return nil
}

func (w *recordWindow) buttonBounds() []image.Rectangle {
	scale := uiScale(w.hwnd)
	pad, gap, h := px(6, scale), px(4, scale), px(32, scale)
	count := len(w.buttons())
	if count == 0 {
		return nil
	}
	width := (w.bounds.Dx() - pad*2 - gap*(count-1)) / count
	result := make([]image.Rectangle, count)
	y := w.bounds.Dy() - pad - h
	for i := range result {
		x := pad + i*(width+gap)
		result[i] = image.Rect(x, y, x+width, y+h)
	}
	return result
}

func (w *recordWindow) caption() string {
	size := recordOutputSize(w.region.Size(), w.d.preferences.Recording.GIFScale)
	switch w.state {
	case recordSetup:
		return fmt.Sprintf("GIF · %d × %d · 无声音", size.X, size.Y)
	case recordCountdown:
		return fmt.Sprintf("%d 秒后开始录制", w.countdown)
	case recordRunning:
		status := "录制中"
		if w.rec.paused.Load() {
			status = "已暂停"
		}
		limit := time.Duration(w.d.preferences.Recording.GIFMaxSeconds) * time.Second
		return fmt.Sprintf("%s %s / %s · %s", status, formatClock(time.Duration(w.rec.elapsed.Load())), formatClock(limit), formatSize(w.rec.bytes.Load()))
	case recordSaving:
		return "正在保存…"
	}
	text := fmt.Sprintf("已保存 · %s · %s", formatClock(time.Duration(w.rec.elapsed.Load())), formatSize(w.fileSize))
	if w.limited {
		text += " · 已达时长上限"
	}
	return text
}

func (w *recordWindow) paint() {
	var ps paintStruct
	call(user32, "BeginPaint", w.hwnd, ptr(&ps))
	call(user32, "EndPaint", w.hwnd, ptr(&ps))
	err := w.paintBuffer.render(w.hwnd, w.bounds.Size(), w.draw)
	if err == nil {
		var img *image.RGBA
		if img, err = w.paintBuffer.pixels(); err == nil {
			t := w.d.theme
			surfaceAlpha(img, t, img.Bounds(), px(int(t.Radius), uiScale(w.hwnd)), materialAlpha(t, w.material), w.opaqueButtons)
			err = presentLayered(w.hwnd, w.bounds, img)
		}
	}
	if err != nil {
		log.Print(err)
	}
}

func (w *recordWindow) draw(dc uintptr) {
	t, scale := w.d.theme, uiScale(w.hwnd)
	bar := image.Rectangle{Max: w.bounds.Size()}
	fill(dc, bar, rgbRef(t.Background))
	surfacePanel(dc, bar, px(int(t.Radius), scale), t)
	w.opaqueButtons = nil
	buttons := w.buttons()
	focused := call(user32, "GetFocus") == w.hwnd
	for i, r := range w.buttonBounds() {
		b := buttons[i]
		bg, fg := t.Background, t.Text
		primary := i == len(buttons)-1
		switch {
		case w.busy:
			bg, fg = t.Surface, t.TextMuted
		case primary && b.id == "stop":
			bg, fg = t.Danger, t.AccentText
		case primary:
			bg, fg = t.Accent, t.AccentText
		case i == w.hover:
			bg = t.SurfaceHover
			if b.id == "cancel" {
				fg = t.Danger
			}
		}
		if bg != t.Background {
			roundPanel(dc, r, px(4, scale), bg, bg)
			w.opaqueButtons = append(w.opaqueButtons, opaqueSurface{r, px(4, scale)})
		}
		size := px(18, scale)
		y := (r.Min.Y + r.Max.Y - size) / 2
		switch {
		case b.label == "":
			x := (r.Min.X + r.Max.X - size) / 2
			icon(dc, b.icon, image.Rect(x, y, x+size, y+size), fg)
		case b.icon == "":
			text(dc, b.label, r, rgbRef(fg))
		default:
			x := r.Min.X + px(8, scale)
			icon(dc, b.icon, image.Rect(x, y, x+size, y+size), fg)
			text(dc, b.label, image.Rect(x+size+px(2, scale), r.Min.Y, r.Max.X-px(3, scale), r.Max.Y), rgbRef(fg))
		}
		if focused && i == w.focus && (w.keyboard || w.state != recordRunning) {
			focusColor := t.Accent
			if primary {
				focusColor = t.AccentText
			}
			frame(dc, r.Inset(px(2, scale)), focusColor, 2)
		}
	}
	caption := w.caption()
	hint := w.hover
	if hint < 0 && focused && w.keyboard {
		hint = w.focus
	}
	if hint >= 0 && hint < len(buttons) && !w.busy {
		caption = buttons[hint].hint
	}
	captionColor := t.TextMuted
	if w.state == recordRunning && !w.rec.paused.Load() && hint < 0 {
		dot := image.Rect(bar.Min.X+px(10, scale), bar.Min.Y+px(9, scale), bar.Min.X+px(20, scale), bar.Min.Y+px(19, scale))
		icon(dc, "record", dot, t.Danger)
		bar.Min.X += px(14, scale)
		captionColor = t.Text
	}
	textAligned(dc, caption, image.Rect(bar.Min.X+px(10, scale), px(3, scale), bar.Max.X-px(10, scale), px(25, scale)), rgbRef(captionColor), false)
}

func (w *recordWindow) drawOutline(dc uintptr, r image.Rectangle) {
	t := w.d.theme
	color := t.Accent
	if w.state == recordRunning {
		color = t.Danger
		if w.rec.paused.Load() {
			color = t.Border
		}
	}
	frame(dc, r, t.Inverse, sourceFrameWidth)
	frame(dc, r, color, 1)
}

func (w *recordWindow) handle(msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case 0x0014:
		return 1
	case wmPaint:
		w.paint()
		return 0
	case 0x0318: // WM_PRINTCLIENT: render a complete client for native QA.
		w.draw(wparam)
		return 0
	case 0x0021: // WM_MOUSEACTIVATE: clicks must not take focus from the recorded app.
		if w.state == recordRunning || w.state == recordSaving {
			return 3
		}
	case wmSetFocus, wmKillFocus:
		invalidate(w.hwnd)
		return 0
	case wmTimer:
		if wparam == recordCountdownTimer && w.state == recordCountdown {
			if w.countdown--; w.countdown <= 0 {
				w.start()
			}
		}
		invalidate(w.hwnd)
		return 0
	case wmClose:
		if !w.busy && w.state != recordSaving {
			w.close(true)
		}
		return 0
	case wmMouseMove:
		previous := w.hover
		w.hover = -1
		for i, r := range w.buttonBounds() {
			if mousePoint(lparam).In(r) {
				w.hover = i
			}
		}
		if previous != w.hover {
			invalidate(w.hwnd)
			// WM_MOUSELEAVE clears the hover hint when the pointer leaves the bar.
			track := struct {
				Size, Flags uint32
				Window      uintptr
				Hover       uint32
			}{24, 2, w.hwnd, 0}
			call(user32, "TrackMouseEvent", ptr(&track))
		}
		return 0
	case 0x02A3: // WM_MOUSELEAVE
		w.hover = -1
		invalidate(w.hwnd)
		return 0
	case wmLButtonUp:
		for i, r := range w.buttonBounds() {
			if mousePoint(lparam).In(r) {
				w.focus = i
				w.activate(w.buttons()[i].id)
				break
			}
		}
		return 0
	case wmKeyDown:
		buttons := w.buttons()
		switch wparam {
		case 0x1b:
			if w.state != recordRunning {
				w.activate("cancel")
			}
		case 0x09:
			if len(buttons) > 0 {
				delta := 1
				if shiftDown() {
					delta = -1
				}
				w.focus, w.keyboard = (w.focus+delta+len(buttons))%len(buttons), true
				invalidate(w.hwnd)
			}
		case 0x0d, 0x20:
			if w.focus >= 0 && w.focus < len(buttons) {
				w.activate(buttons[w.focus].id)
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", w.hwnd, msg, wparam, lparam)
}

func (w *recordWindow) activate(id string) {
	if w.busy || w.state == recordSaving {
		return
	}
	prefs := &w.d.preferences.Recording
	switch id {
	case "fps", "scale":
		previous := *prefs
		if id == "fps" {
			prefs.GIFFPS = nextOption(gifFPSOptions, prefs.GIFFPS)
		} else {
			prefs.GIFScale = nextOption(gifScaleOptions, prefs.GIFScale)
		}
		if err := savePreferences(w.d.preferences); err != nil {
			*prefs = previous
			w.d.report(w.hwnd, err)
		}
	case "start":
		w.start()
	case "pause":
		w.rec.paused.Store(!w.rec.paused.Load())
		invalidate(w.outline)
	case "stop":
		w.setState(recordSaving)
		w.rec.finish(false)
	case "cancel":
		w.close(true)
		return
	case "open":
		if call(shell32, "ShellExecuteW", w.hwnd, ptr(u16("open")), ptr(u16(w.path)), 0, 0, 1) <= 32 {
			w.d.report(w.hwnd, errors.New("无法使用默认应用打开录制结果"))
		}
	case "folder":
		if call(shell32, "ShellExecuteW", w.hwnd, ptr(u16("open")), ptr(u16("explorer.exe")), ptr(u16(`/select,"`+w.path+`"`)), 0, 1) <= 32 {
			w.d.report(w.hwnd, errors.New("无法打开录制保存目录"))
		}
	case "copy":
		w.d.report(w.hwnd, copyFileDrop(w.hwnd, w.path))
	case "save":
		target, err := chooseSave(w.hwnd, "另存录制", filepath.Base(w.path), filepath.Dir(w.path), "GIF", "gif")
		if err != nil || target == "" || target == w.path {
			w.d.report(w.hwnd, err)
			break
		}
		w.busy = true
		w.d.active.Add(1)
		source := w.path
		go func() {
			err := copyFile(source, target)
			_ = w.d.post(func() {
				w.d.active.Add(-1)
				w.busy = false
				if w.d.recording == w {
					w.d.report(w.hwnd, err)
					invalidate(w.hwnd)
				}
			})
		}()
	}
	invalidate(w.hwnd)
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

// copyFileDrop puts the file itself (CF_HDROP) on the clipboard, the form
// chat apps and Explorer accept for animated images.
func copyFileDrop(owner uintptr, path string) error {
	name, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	data := make([]byte, 20+len(name)*2+2) // DROPFILES, path, list terminator
	data[0], data[16] = 20, 1              // pFiles offset, fWide
	for i, v := range name {
		data[20+i*2], data[21+i*2] = byte(v), byte(v>>8)
	}
	handle := call(kernel32, "GlobalAlloc", 2, len(data))
	if handle == 0 {
		return errors.New("无法分配剪贴板内存")
	}
	address := call(kernel32, "GlobalLock", handle)
	if address != 0 {
		err = windows.WriteProcessMemory(windows.CurrentProcess(), address, &data[0], uintptr(len(data)), nil)
		call(kernel32, "GlobalUnlock", handle)
	}
	if address == 0 || err != nil {
		call(kernel32, "GlobalFree", handle)
		return errors.Join(errors.New("无法写入剪贴板内存"), err)
	}
	if call(user32, "OpenClipboard", owner) == 0 {
		call(kernel32, "GlobalFree", handle)
		return errors.New("剪贴板正被其他应用占用，请重试")
	}
	defer call(user32, "CloseClipboard")
	call(user32, "EmptyClipboard")
	if call(user32, "SetClipboardData", 15, handle) == 0 {
		call(kernel32, "GlobalFree", handle)
		return errors.New("写入剪贴板失败，请重试")
	}
	return nil
}

// FOLDERID_Videos honors relocated and OneDrive-backed Videos folders.
func recordingDirectory() (string, error) {
	dir, err := knownFolder(windows.GUID{Data1: 0x18989B1D, Data2: 0x99B5, Data3: 0x455B, Data4: [8]byte{0x84, 0x1C, 0xAB, 0x7C, 0x74, 0xE4, 0xDD, 0xFC}})
	if err != nil {
		return "", errors.New("无法定位系统视频文件夹")
	}
	return filepath.Join(dir, "FluentLauncher"), nil
}
