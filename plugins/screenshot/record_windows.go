//go:build windows

package main

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// recordItem is one slot of the single-row bar. An empty id is static
// content; "grip" drags the bar. Widths are in DIP.
type recordItem struct {
	id, label, icon, hint string
	width                 int
	disabled, danger      bool
	primary               bool
}

// recordWindow is the control bar next to the recorded region. It and the
// outline are excluded from capture, so neither appears in the result.
// The window is the bar plus a transparent band that only shows hints.
type recordWindow struct {
	d             *desktop
	hwnd, outline uintptr
	originalFocus uintptr
	material      string
	opaqueButtons []opaqueSurface
	region        image.Rectangle
	bar           image.Rectangle // screen position of the visible bar
	bounds        image.Rectangle // window: bar and hint band
	hintAbove     bool
	hint          image.Rectangle // window-local hint pill, empty when hidden
	shape         [2]image.Rectangle
	excluded      bool
	state         recordState
	countdown     int
	rec           *recorder
	path          string
	fileSize      int64
	format        string // of the recording in progress or finished
	limited       bool
	busy          bool
	keyboard      bool // Tab was used: describe the focused item
	hover, focus  int
	pressed       int
	dragMode      int // 1 moves the bar, 2 moves the region with the bar
	dragStart     image.Point
	dragBar       image.Rectangle
	dragRegion    image.Rectangle
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
	w := &recordWindow{d: d, region: region, originalFocus: focus, hover: -1, pressed: -1}
	// Created on the target monitor first: its DPI decides the bar size.
	w.hwnd = d.createWindow("FLScreenshotRecord", "录制", wsExTopmost|wsExToolwindow|wsExLayered, d.controller, image.Rectangle{Min: monitor.Min, Max: monitor.Min.Add(image.Pt(1, 1))})
	if w.hwnd == 0 {
		d.report(d.controller, errors.New("无法创建录制操作条"))
		return false
	}
	w.bar = recordBarBounds(region, monitor, w.barSize(), px(10, uiScale(w.hwnd)))
	w.excluded = call(user32, "SetWindowDisplayAffinity", w.hwnd, 0x11) != 0
	if w.bar.Overlaps(region) && !w.excluded {
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
	w.focus = w.defaultFocus()
	w.material = applySurfaceMaterial(w.hwnd, d.theme)
	w.place(true)
	call(user32, "SetForegroundWindow", w.hwnd)
	call(user32, "SetFocus", w.hwnd)
	return true
}

func (w *recordWindow) applyMaterial() {
	w.material = applySurfaceMaterial(w.hwnd, w.d.theme)
	w.shape = [2]image.Rectangle{}
	invalidate(w.hwnd)
	if w.outline != 0 {
		invalidate(w.outline)
	}
}

// place sizes the window for the current items and keeps it on the monitor.
// The hint band sits below the bar, or above it at the bottom screen edge.
func (w *recordWindow) place(activate bool) {
	scale := uiScale(w.hwnd)
	size := w.barSize()
	monitor := currentMonitor(w.bar.Min.Add(w.bar.Size().Div(2)))
	p := image.Pt(max(monitor.Min.X, min(w.bar.Min.X, monitor.Max.X-size.X)), max(monitor.Min.Y, min(w.bar.Min.Y, monitor.Max.Y-size.Y)))
	w.bar = image.Rectangle{Min: p, Max: p.Add(size)}
	band := px(30, scale)
	w.hintAbove = w.bar.Max.Y+band > monitor.Max.Y
	w.bounds = w.bar
	if w.hintAbove {
		w.bounds.Min.Y -= band
	} else {
		w.bounds.Max.Y += band
	}
	w.shape = [2]image.Rectangle{}
	windowAt(w.hwnd, w.bounds, activate)
	invalidate(w.hwnd)
}

func (w *recordWindow) barLocal() image.Rectangle {
	return w.bar.Sub(w.bounds.Min)
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
	if w.dragMode != 0 {
		call(user32, "ReleaseCapture")
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
	w.focus = w.defaultFocus()
	if w.dragMode != 0 {
		w.dragMode = 0
		call(user32, "ReleaseCapture")
	}
	w.place(false)
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
	prefs := w.d.preferences.Recording
	region, out := recordFrame(w.region, prefs.Scale, prefs.mp4())
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
	rec := newRecorder(open, dir, prefs.fps(), prefs.limit())
	if prefs.mp4() {
		openAudio := openWASAPI
		if w.d.recordAudio != nil {
			openAudio = w.d.recordAudio
		}
		for _, track := range []struct {
			enabled, loopback bool
			volume            int
		}{{prefs.SystemAudio, true, prefs.SystemVolume}, {prefs.Microphone, false, prefs.MicVolume}} {
			if track.enabled {
				rec.audio = append(rec.audio, func() (audioSource, error) { return openAudio(track.loopback) })
				rec.volumes = append(rec.volumes, track.volume)
			}
		}
		sound := len(rec.audio) > 0
		rec.extension = "mp4"
		rec.encoder = func(path string, size image.Point) (recordEncoder, error) {
			return newMP4Writer(path, size, prefs.MP4FPS, sound)
		}
	}
	w.rec, w.format = rec, prefs.Format
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

func (w *recordWindow) items() []recordItem {
	prefs := w.d.preferences.Recording
	grip := recordItem{id: "grip", hint: "拖动移动工具栏", width: 14}
	sep := recordItem{icon: "sep", width: 9}
	if w.state == recordDone {
		summary := fmt.Sprintf("已保存 %s · %s", formatClock(time.Duration(w.rec.elapsed.Load())), formatSize(w.fileSize))
		if w.limited {
			summary += " · 已达上限"
		}
		return []recordItem{grip, {label: summary, width: 210},
			{id: "copy", icon: "copy", hint: "复制文件，可粘贴到聊天或文件夹", width: 32},
			{id: "save", icon: "save", hint: "另存为", width: 32},
			{id: "folder", icon: "folder", hint: "在文件夹中显示", width: 32},
			{id: "open", icon: "start", hint: "打开录制结果 · Enter", width: 32, primary: true},
			sep, {id: "cancel", icon: "close", hint: "关闭 · Esc", width: 32}}
	}
	setup := w.state == recordSetup
	clock := recordItem{label: "00:00", width: 50}
	format := recordItem{id: "format", label: "MP4", hint: "格式 · 点击切换 MP4（可带声音）/ GIF", width: 44, disabled: !setup}
	fps := recordItem{id: "fps", label: fmt.Sprintf("%dFPS", prefs.fps()), hint: "帧率 · 点击切换 15 / 30 / 60", width: 50, disabled: !setup}
	audio := recordItem{id: "audio", icon: "speaker", width: 32, disabled: !setup}
	switch {
	case !prefs.mp4():
		format.label, fps.hint = "GIF", "帧率 · 点击切换 10 / 15 / 20"
		audio.icon, audio.hint, audio.disabled = "mute", "GIF 没有声音；切换到 MP4 后可录制系统声音和麦克风", true
	case !prefs.SystemAudio && !prefs.Microphone:
		audio.icon, audio.hint = "mute", "声音 · 未录制，点击选择系统声音或麦克风"
	default:
		state := map[bool]string{true: "开", false: "关"}
		audio.hint = fmt.Sprintf("声音 · 系统声音 %s，麦克风 %s", state[prefs.SystemAudio], state[prefs.Microphone])
	}
	third := recordItem{id: "scale", label: fmt.Sprintf("%d%%", prefs.Scale), hint: "画面缩放 · 点击切换 100% / 75% / 50%", width: 58, disabled: !setup}
	action := recordItem{id: "start", icon: "record", hint: "开始录制 · Enter", width: 32, danger: true}
	pause := recordItem{id: "pause", icon: "pause", hint: "暂停录制", width: 32, disabled: true}
	move := recordItem{id: "move", icon: "move", hint: "按住拖动，移动录制区域", width: 32, disabled: !setup}
	cancel := recordItem{id: "cancel", icon: "close", hint: "取消 · Esc", width: 32}
	switch w.state {
	case recordCountdown:
		clock.label = fmt.Sprintf("%d 秒", w.countdown)
		action.hint = "跳过倒计时，立即开始"
	case recordRunning, recordSaving:
		elapsed := time.Duration(w.rec.elapsed.Load())
		limit := prefs.limit()
		clock.label, clock.danger = formatClock(elapsed), limit-elapsed <= 10*time.Second
		third = recordItem{label: formatSize(w.rec.bytes.Load()), width: 58, disabled: true}
		action = recordItem{id: "stop", icon: "stop", hint: fmt.Sprintf("停止并保存 · 最长 %s", formatClock(limit)), width: 32, danger: true}
		pause.disabled = false
		if w.rec.paused.Load() {
			pause.icon, pause.hint = "start", "继续录制"
		}
		cancel.hint = "取消并丢弃录制"
		if w.state == recordSaving {
			clock.label, clock.danger = "保存中", false
			action.disabled, pause.disabled, cancel.disabled = true, true, true
		}
	}
	return []recordItem{grip, clock, format, fps, third, action, pause, audio, sep, move, cancel}
}

func (w *recordWindow) barSize() image.Point {
	scale := uiScale(w.hwnd)
	width := px(4, scale) * 2
	for _, it := range w.items() {
		width += px(it.width, scale)
	}
	return image.Pt(width, px(40, scale))
}

// itemBounds are window-local, in the order of items().
func (w *recordWindow) itemBounds() []image.Rectangle {
	scale, bar := uiScale(w.hwnd), w.barLocal()
	items := w.items()
	result := make([]image.Rectangle, len(items))
	x := bar.Min.X + px(4, scale)
	for i, it := range items {
		width := px(it.width, scale)
		result[i] = image.Rect(x, bar.Min.Y+px(4, scale), x+width, bar.Max.Y-px(4, scale))
		x += width
	}
	return result
}

func (w *recordWindow) itemAt(p image.Point) int {
	for i, r := range w.itemBounds() {
		if p.In(r) {
			return i
		}
	}
	return -1
}

func (w *recordWindow) itemIndex(id string) int {
	for i, it := range w.items() {
		if it.id == id {
			return i
		}
	}
	return -1
}

func (it recordItem) focusable() bool { return it.id != "" && it.id != "grip" && !it.disabled }

func (w *recordWindow) defaultFocus() int {
	for _, id := range []string{"start", "stop", "open"} {
		if i := w.itemIndex(id); i >= 0 {
			return i
		}
	}
	return -1
}

func (w *recordWindow) paint() {
	var ps paintStruct
	call(user32, "BeginPaint", w.hwnd, ptr(&ps))
	call(user32, "EndPaint", w.hwnd, ptr(&ps))
	img, err := w.layeredImage()
	if err == nil {
		w.updateShape()
		err = presentLayered(w.hwnd, w.bounds, img)
	}
	if err != nil {
		log.Print(err)
	}
}

// layeredImage is the presented frame: a translucent bar, an opaque hint
// and nothing else.
func (w *recordWindow) layeredImage() (*image.RGBA, error) {
	if err := w.paintBuffer.render(w.hwnd, w.bounds.Size(), w.draw); err != nil {
		return nil, err
	}
	img, err := w.paintBuffer.pixels()
	if err != nil {
		return nil, err
	}
	t, scale, bar := w.d.theme, uiScale(w.hwnd), w.barLocal()
	surfaceAlpha(img, t, bar, px(int(t.Radius), scale), materialAlpha(t, w.material), w.opaqueButtons)
	for y := 0; y < img.Rect.Dy(); y++ {
		if y >= bar.Min.Y && y < bar.Max.Y {
			continue
		}
		for x := 0; x < img.Rect.Dx(); x++ {
			if !inRound(image.Pt(x, y), w.hint, px(4, scale)) {
				img.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
	return img, nil
}

// The window region keeps materials (blur) and clicks to the visible parts.
func (w *recordWindow) updateShape() {
	bar := w.barLocal()
	if w.shape == [2]image.Rectangle{bar, w.hint} {
		return
	}
	w.shape = [2]image.Rectangle{bar, w.hint}
	radius := px(int(max(0, w.d.theme.Radius)), uiScale(w.hwnd))
	rgn := call(gdi32, "CreateRoundRectRgn", coord(bar.Min.X), coord(bar.Min.Y), coord(bar.Max.X+1), coord(bar.Max.Y+1), radius*2, radius*2)
	if !w.hint.Empty() {
		hint := call(gdi32, "CreateRectRgn", coord(w.hint.Min.X), coord(w.hint.Min.Y), coord(w.hint.Max.X), coord(w.hint.Max.Y))
		call(gdi32, "CombineRgn", rgn, rgn, hint, 2)
		call(gdi32, "DeleteObject", hint)
	}
	if call(user32, "SetWindowRgn", w.hwnd, rgn, 0) == 0 {
		call(gdi32, "DeleteObject", rgn)
	}
}

func (w *recordWindow) draw(dc uintptr) {
	t, scale := w.d.theme, uiScale(w.hwnd)
	window := image.Rectangle{Max: w.bounds.Size()}
	fill(dc, window, rgbRef(t.Background))
	surfacePanel(dc, w.barLocal(), px(int(t.Radius), scale), t)
	w.opaqueButtons = nil
	items, bounds := w.items(), w.itemBounds()
	focused := call(user32, "GetFocus") == w.hwnd
	for i, r := range bounds {
		it := items[i]
		switch {
		case it.id == "grip":
			dot := max(2, px(2, scale))
			for row := -1; row <= 1; row++ {
				for col := 0; col < 2; col++ {
					x, y := (r.Min.X+r.Max.X)/2-dot*2+col*dot*3, (r.Min.Y+r.Max.Y)/2+row*dot*3-dot/2
					fill(dc, image.Rect(x, y, x+dot, y+dot), rgbRef(t.TextMuted))
				}
			}
			continue
		case it.icon == "sep":
			x := (r.Min.X + r.Max.X) / 2
			line(dc, image.Pt(x, r.Min.Y+px(6, scale)), image.Pt(x, r.Max.Y-px(6, scale)), t.Border, 1)
			continue
		}
		disabled := it.disabled || (w.busy && it.id != "")
		bg, fg := t.Background, t.Text
		switch {
		case disabled:
			fg = t.TextMuted
		case it.primary:
			bg, fg = t.Accent, t.AccentText
		case it.danger:
			fg = t.Danger
		}
		if i == w.hover && it.id != "" && !disabled && !it.primary {
			bg = t.SurfaceHover
			if it.id == "cancel" {
				fg = t.Danger
			}
		}
		if bg != t.Background {
			roundPanel(dc, r, px(4, scale), bg, bg)
			w.opaqueButtons = append(w.opaqueButtons, opaqueSurface{r, px(4, scale)})
		}
		if it.label != "" {
			text(dc, it.label, r, rgbRef(fg))
		} else {
			size := px(18, scale)
			x, y := (r.Min.X+r.Max.X-size)/2, (r.Min.Y+r.Max.Y-size)/2
			icon(dc, it.icon, image.Rect(x, y, x+size, y+size), fg)
		}
		if focused && i == w.focus && (w.keyboard || w.state != recordRunning) {
			focusColor := t.Accent
			if it.primary {
				focusColor = t.AccentText
			}
			frame(dc, r.Inset(px(2, scale)), focusColor, 2)
		}
	}
	hint := w.hover
	if hint < 0 && focused && w.keyboard {
		hint = w.focus
	}
	w.hint = image.Rectangle{}
	// Without capture exclusion a hint could be burnt into the recording.
	if hint >= 0 && hint < len(items) && items[hint].hint != "" && w.dragMode == 0 && w.excluded {
		w.hint = paintToolbarHint(dc, items[hint].hint, bounds[hint], window, scale, t)
	}
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

// drag follows the pointer in screen pixels: the bar alone, or the region
// together with its outline and the bar.
func (w *recordWindow) drag() {
	delta := cursorPoint().Sub(w.dragStart)
	if w.dragMode == 2 {
		desktop, r := virtualBounds(), w.dragRegion
		delta.X = max(desktop.Min.X-r.Min.X, min(delta.X, desktop.Max.X-r.Max.X))
		delta.Y = max(desktop.Min.Y-r.Min.Y, min(delta.Y, desktop.Max.Y-r.Max.Y))
		w.region = r.Add(delta)
		windowAt(w.outline, w.region.Inset(-sourceFrameWidth), false)
	}
	bar := w.dragBar.Add(delta)
	if !w.excluded && bar.Overlaps(w.region) {
		return
	}
	w.bar = bar
	w.place(false)
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
	case wmLButtonDown:
		p := mousePoint(lparam)
		w.pressed = w.itemAt(p)
		mode := 0
		if items := w.items(); w.pressed < 0 || items[w.pressed].id == "" || items[w.pressed].id == "grip" {
			if p.In(w.barLocal()) {
				mode = 1
			}
		} else if items[w.pressed].id == "move" && !items[w.pressed].disabled && !w.busy {
			mode = 2
		}
		if mode != 0 {
			w.dragMode, w.dragStart, w.dragBar, w.dragRegion = mode, cursorPoint(), w.bar, w.region
			call(user32, "SetCapture", w.hwnd)
			invalidate(w.hwnd)
		}
		return 0
	case wmMouseMove:
		if w.dragMode != 0 {
			w.drag()
			return 0
		}
		previous := w.hover
		w.hover = w.itemAt(mousePoint(lparam))
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
	case 0x0215: // WM_CAPTURECHANGED
		w.dragMode = 0
		return 0
	case wmLButtonUp:
		if w.dragMode != 0 {
			w.dragMode = 0
			call(user32, "ReleaseCapture")
			invalidate(w.hwnd)
			return 0
		}
		items := w.items()
		if i := w.itemAt(mousePoint(lparam)); i >= 0 && i == w.pressed && items[i].focusable() {
			w.focus = i
			w.activate(items[i].id)
		}
		return 0
	case wmKeyDown:
		items := w.items()
		switch wparam {
		case 0x1b:
			if w.state != recordRunning {
				w.activate("cancel")
			}
		case 0x09:
			delta := 1
			if shiftDown() {
				delta = -1
			}
			for step := 1; step <= len(items); step++ {
				if i := ((w.focus+delta*step)%len(items) + len(items)) % len(items); items[i].focusable() {
					w.focus, w.keyboard = i, true
					break
				}
			}
			invalidate(w.hwnd)
		case 0x0d, 0x20:
			if w.focus >= 0 && w.focus < len(items) && items[w.focus].focusable() {
				w.activate(items[w.focus].id)
			}
		}
		return 0
	}
	return call(user32, "DefWindowProcW", w.hwnd, msg, wparam, lparam)
}

// change applies and saves a setup choice, or reports why it was not kept.
func (w *recordWindow) change(apply func(*recordPreferences)) {
	previous := w.d.preferences.Recording
	apply(&w.d.preferences.Recording)
	if err := savePreferences(w.d.preferences); err != nil {
		w.d.preferences.Recording = previous
		w.d.report(w.hwnd, err)
	}
	invalidate(w.hwnd)
}

func (w *recordWindow) toggleAudio(system bool) {
	w.change(func(p *recordPreferences) {
		if system {
			p.SystemAudio = !p.SystemAudio
		} else {
			p.Microphone = !p.Microphone
		}
	})
}

func (w *recordWindow) activate(id string) {
	if w.busy || w.state == recordSaving {
		return
	}
	prefs := w.d.preferences.Recording
	switch id {
	case "format", "fps", "scale":
		w.change(func(p *recordPreferences) {
			switch {
			case id == "format":
				p.Format = map[bool]string{true: "gif", false: "mp4"}[p.mp4()]
			case id == "scale":
				p.Scale = nextOption(scaleOptions, p.Scale)
			case p.mp4():
				p.MP4FPS = nextOption(mp4FPSOptions, p.MP4FPS)
			default:
				p.GIFFPS = nextOption(gifFPSOptions, p.GIFFPS)
			}
		})
	case "audio":
		r := w.itemBounds()[w.itemIndex("audio")]
		w.d.showMenu(w.hwnd, w.bounds.Min.Add(image.Pt(r.Min.X, w.barLocal().Max.Y+px(4, uiScale(w.hwnd)))), []menuItem{
			{ID: 1, Label: "录制系统声音", Icon: "speaker", Checked: prefs.SystemAudio},
			{ID: 2, Label: "录制麦克风", Icon: "speaker", Checked: prefs.Microphone},
		}, func(choice int) {
			if w.d.recording == w && w.state == recordSetup {
				w.toggleAudio(choice == 1)
			}
		})
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
		target, err := chooseSave(w.hwnd, "另存录制", filepath.Base(w.path), filepath.Dir(w.path), strings.ToUpper(w.format), w.format)
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
