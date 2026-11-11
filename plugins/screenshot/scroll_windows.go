//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync/atomic"
	"time"
)

type scrollAction struct {
	kind string
	auto bool
}

type longSeed struct {
	first *image.RGBA
	marks []annotation
}

type scrollWindow struct {
	d                            *desktop
	hwnd, target, root           uintptr
	region, bounds, rootBounds   image.Rectangle
	ctx                          context.Context
	cancel                       context.CancelFunc
	discard                      atomic.Bool
	finishRequested              atomic.Bool
	actions                      chan scrollAction
	preview, auto, busy          bool
	status                       string
	frames, height               int
	result, rawResult, lastFrame *image.RGBA
	bitmap                       *bitmap
	offset                       int
	hover, focus                 int
	dragScroll                   bool
	marks                        []annotation

	capturePinsHidden, captureUIHidden, themeDeferred bool
}

func scrollTarget(region image.Rectangle) (uintptr, uintptr, image.Rectangle, error) {
	center := region.Min.Add(region.Size().Div(2))
	for _, delta := range []image.Point{{}, image.Pt(-region.Dx()/4, 0), image.Pt(region.Dx()/4, 0), image.Pt(0, -region.Dy()/4)} {
		p := center.Add(delta)
		packed := uintptr(uint64(uint32(p.X)) | uint64(uint32(p.Y))<<32)
		hwnd := call(user32, "WindowFromPoint", packed)
		var pid uint32
		call(user32, "GetWindowThreadProcessId", hwnd, ptr(&pid))
		if hwnd == 0 || pid == uint32(call(kernel32, "GetCurrentProcessId")) {
			continue
		}
		root := call(user32, "GetAncestor", hwnd, 2)
		var r rect
		if call(user32, "GetWindowRect", root, ptr(&r)) == 0 {
			continue
		}
		if !region.In(r.image()) {
			return 0, 0, image.Rectangle{}, errors.New("长截图选区应位于同一个窗口内，请只选择可滚动的内容")
		}
		return hwnd, root, r.image(), nil
	}
	return 0, 0, image.Rectangle{}, errors.New("没有找到可滚动的目标窗口，请将选区放在页面内容中，避开贴图和操作窗口")
}

func longPanelBounds(region, monitor image.Rectangle, size image.Point, gap int) image.Rectangle {
	x := max(monitor.Min.X, min(region.Min.X, monitor.Max.X-size.X))
	candidates := []image.Point{image.Pt(x, region.Min.Y-gap-size.Y), image.Pt(x, region.Max.Y+gap), image.Pt(region.Max.X+gap, region.Min.Y), image.Pt(region.Min.X-gap-size.X, region.Min.Y)}
	for _, p := range candidates {
		r := image.Rectangle{Min: p, Max: p.Add(size)}
		if r.In(monitor) && !r.Overlaps(region) {
			return r
		}
	}
	p := image.Pt(max(monitor.Min.X, monitor.Max.X-size.X-gap), max(monitor.Min.Y, monitor.Max.Y-size.Y-gap))
	return image.Rectangle{Min: p, Max: p.Add(size)}
}

func (d *desktop) startLongCapture(region image.Rectangle, auto bool, scale float64, seeds ...longSeed) {
	if region.Dx() < 64 || region.Dy() < 96 {
		d.report(d.controller, errors.New("长截图选区至少需要 64×96 像素，请重新选择内容区域"))
		return
	}
	if region.Dx() > maxLongPixels/region.Dy() {
		d.report(d.controller, errLongLimit)
		return
	}
	target, root, rootBounds, err := scrollTarget(region)
	if err != nil {
		d.report(d.controller, err)
		return
	}
	d.openLongCapture(region, auto, scale, target, root, rootBounds, seeds...)
}

func (d *desktop) openLongCapture(region image.Rectangle, auto bool, scale float64, target, root uintptr, rootBounds image.Rectangle, seeds ...longSeed) {
	monitor := currentMonitor(region.Min.Add(region.Size().Div(2)))
	size := image.Pt(min(px(520, scale), monitor.Dx()-16), px(104, scale))
	bounds := longPanelBounds(region, monitor, size, px(8, scale))
	s := &scrollWindow{d: d, target: target, root: root, rootBounds: rootBounds, region: region, bounds: bounds, auto: auto, actions: make(chan scrollAction, 8), hover: -1, status: "正在准备首帧…"}
	s.hwnd = d.createWindow("FLScreenshotLong", "滚动长截图", wsExTopmost|wsExToolwindow, d.controller, bounds)
	if s.hwnd == 0 {
		d.report(d.controller, errors.New("无法创建长截图控制条"))
		return
	}
	d.scrolling = s
	d.capturing.Store(true)
	d.active.Add(1)
	windowAt(s.hwnd, bounds, false)
	call(user32, "SetForegroundWindow", root)
	var seed *stitcher
	if len(seeds) > 0 {
		seed = newStitcher(seeds[0].first)
		s.marks = seeds[0].marks
	}
	s.startWorker(auto, seed)
}

func (s *scrollWindow) close(discard bool) {
	if s.d.scrolling != s {
		return
	}
	s.discard.Store(discard)
	if s.cancel != nil {
		s.cancel()
	}
	s.d.scrolling = nil
	s.restoreCaptureUI()
	if s.dragScroll {
		call(user32, "ReleaseCapture")
	}
	call(user32, "DestroyWindow", s.hwnd)
	s.bitmap.close()
	s.bitmap = nil
	s.d.active.Add(-1)
	s.d.capturing.Store(false)
}

func (s *scrollWindow) startWorker(auto bool, seed *stitcher) {
	s.preview = false
	s.finishRequested.Store(false)
	s.auto = auto
	s.hover = -1
	s.actions = make(chan scrollAction, 8)
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	s.d.active.Add(1)
	go s.collect(s.ctx, auto, seed)
}

func (s *scrollWindow) updateProgress(builder *stitcher, auto bool, status string) {
	frames, height := builder.frames, builder.height
	_ = s.d.post(func() {
		if s.d.scrolling != s || s.preview {
			return
		}
		s.auto = auto
		s.frames = frames
		s.height = height
		s.status = status
		invalidate(s.hwnd)
	})
}

func (s *scrollWindow) targetValid() bool {
	if call(user32, "IsWindow", s.root) == 0 || call(user32, "IsWindowVisible", s.root) == 0 || call(user32, "IsIconic", s.root) != 0 {
		return false
	}
	var r rect
	if call(user32, "GetWindowRect", s.root, ptr(&r)) == 0 || r.image() != s.rootBounds {
		return false
	}
	ownPID := uint32(call(kernel32, "GetCurrentProcessId"))
	var rootPID uint32
	call(user32, "GetWindowThreadProcessId", s.root, ptr(&rootPID))
	foreground := call(user32, "GetForegroundWindow")
	var pid uint32
	call(user32, "GetWindowThreadProcessId", foreground, ptr(&pid))
	if rootPID != ownPID && foreground != 0 && pid != ownPID && call(user32, "GetAncestor", foreground, 2) != s.root {
		return false
	}
	for _, p := range []image.Point{s.region.Min.Add(image.Pt(3, 3)), s.region.Max.Sub(image.Pt(4, 4)), s.region.Min.Add(s.region.Size().Div(2))} {
		packed := uintptr(uint64(uint32(p.X)) | uint64(uint32(p.Y))<<32)
		hwnd := call(user32, "WindowFromPoint", packed)
		call(user32, "GetWindowThreadProcessId", hwnd, ptr(&pid))
		if rootPID != ownPID && hwnd != 0 && pid != ownPID && call(user32, "GetAncestor", hwnd, 2) != s.root {
			return false
		}
	}
	return true
}

func waitScroll(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *scrollWindow) capture(ctx context.Context) (*image.RGBA, error) {
	if !s.targetValid() {
		return nil, errors.New("目标窗口已关闭、移动或被其他应用遮挡，已停止采集并保留进度")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer func() { _ = s.d.post(s.restoreCaptureUI) }()
	type visibility struct{ valid, hidden bool }
	ack := make(chan visibility, 1)
	if err := s.d.post(func() {
		if s.d.scrolling != s || s.preview {
			ack <- visibility{}
			return
		}
		s.d.closeMenu()
		overlaps := s.bounds.Overlaps(s.region)
		if overlaps {
			call(user32, "ShowWindow", s.hwnd, 0)
		}
		hidePins := !s.d.preferences.IncludePins
		s.capturePinsHidden = hidePins
		s.captureUIHidden = overlaps || hidePins
		if hidePins {
			for _, p := range s.d.pins {
				call(user32, "ShowWindow", p.hwnd, 0)
				call(user32, "ShowWindow", p.shadow, 0)
			}
		}
		ack <- visibility{true, overlaps || hidePins}
	}); err != nil {
		return nil, err
	}
	var state visibility
	select {
	case state = <-ack:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !state.valid {
		return nil, context.Canceled
	}
	if state.hidden && !waitScroll(ctx, 40*time.Millisecond) {
		return nil, ctx.Err()
	}
	call(dwmapi, "DwmFlush")
	if s.d.captureLong != nil {
		return s.d.captureLong(s.region)
	}
	return captureScreen(s.region)
}

// GUI-thread only, including cancellation: hidden pins must be restored even
// when the worker stops between the visibility request and its acknowledgement.
func (s *scrollWindow) restoreCaptureUI() {
	if s.capturePinsHidden {
		for _, hwnd := range s.d.pinOrder {
			p := s.d.pins[hwnd]
			if !p.state.Hidden {
				call(user32, "ShowWindow", p.hwnd, 4)
				if p.state.Shadow {
					call(user32, "ShowWindow", p.shadow, 4)
				}
			}
		}
	}
	s.capturePinsHidden = false
	s.captureUIHidden = false
	if s.themeDeferred {
		s.themeDeferred = false
		s.d.applyTheme(s.d.theme)
	}
	if s.d.scrolling == s && !s.preview {
		windowAt(s.hwnd, s.bounds, false)
	}
}

func (s *scrollWindow) stableCapture(ctx context.Context) (*image.RGBA, error) {
	if !waitScroll(ctx, 180*time.Millisecond) {
		return nil, ctx.Err()
	}
	last, err := s.capture(ctx)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 8; i++ {
		if !waitScroll(ctx, 90*time.Millisecond) {
			return nil, ctx.Err()
		}
		next, err := s.capture(ctx)
		if err != nil {
			return nil, err
		}
		if frameDifference(last, next) < 0.7 {
			return next, nil
		}
		last = next
	}
	return last, nil
}

func (s *scrollWindow) wheel(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ack := make(chan error, 1)
	if err := s.d.post(func() {
		if err := ctx.Err(); err != nil {
			ack <- err
			return
		}
		if s.d.scrolling != s || s.preview {
			ack <- context.Canceled
			return
		}
		ack <- s.postWheel()
	}); err != nil {
		return err
	}
	select {
	case err := <-ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *scrollWindow) postWheel() error {
	if !s.targetValid() {
		return errors.New("目标窗口位置已变化，停止自动滚动")
	}
	point := s.region.Min.Add(s.region.Size().Div(2))
	notches := 1
	if s.region.Dy() > 500 {
		notches = 2
	}
	if s.region.Dy() > 1000 {
		notches = 3
	}
	delta := int16(-120 * notches)
	wparam := uintptr(uint32(uint16(delta)) << 16)
	lparam := uintptr(uint32(uint16(point.X)) | uint32(uint16(point.Y))<<16)
	if call(user32, "PostMessageW", s.target, 0x020A, wparam, lparam) == 0 {
		return errors.New("目标应用拒绝滚动消息，请切换手动滚动采集")
	}
	return nil
}

func (s *scrollWindow) collect(ctx context.Context, auto bool, builder *stitcher) {
	defer s.d.active.Add(-1)
	if builder == nil {
		first, err := s.stableCapture(ctx)
		if err != nil {
			s.failStart(err)
			return
		}
		builder = newStitcher(first)
	}
	status := "手动向下滚动后点击“采集”"
	if auto {
		status = "正在自动滚动，随时可以完成或切换手动"
	}
	s.updateProgress(builder, auto, status)
	unchanged := 0
captureLoop:
	for {
		var tick <-chan time.Time
		var timer *time.Timer
		if auto {
			timer = time.NewTimer(160 * time.Millisecond)
			tick = timer.C
		}
		capture, scroll := false, false
		select {
		case <-ctx.Done():
			status = "采集已结束，保留已捕获内容"
			if s.finishRequested.Load() {
				status = "采集完成"
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "达到 2 分钟上限，已保留当前结果"
			}
			if timer != nil {
				timer.Stop()
			}
			break captureLoop
		case action := <-s.actions:
			switch action.kind {
			case "mode":
				auto = action.auto
				unchanged = 0
				s.updateProgress(builder, auto, "模式已切换；手动滚动后点击采集")
			case "capture":
				capture = true
			}
		case <-tick:
			capture, scroll = true, true
		}
		if timer != nil {
			timer.Stop()
		}
		if !capture {
			continue
		}
		if ctx.Err() != nil {
			continue
		}
		if scroll {
			if err := s.wheel(ctx); err != nil {
				if ctx.Err() != nil {
					continue
				}
				auto = false
				s.updateProgress(builder, false, err.Error())
				continue
			}
		}
		frame, err := s.stableCapture(ctx)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			status = err.Error()
			break
		}
		added, err := builder.add(frame)
		if errors.Is(err, errLongLimit) {
			status = err.Error()
			break
		}
		if err != nil {
			auto = false
			s.updateProgress(builder, false, "自动已暂停。"+err.Error())
			continue
		}
		if !added {
			unchanged++
			if auto && unchanged >= 2 {
				if builder.frames == 1 {
					auto = false
					s.updateProgress(builder, false, "未检测到滚动，请手动滚动后采集，或完成当前单屏")
				} else {
					status = "画面不再移动，已结束；请检查末尾，必要时可继续采集"
					break
				}
			}
			continue
		}
		unchanged = 0
		s.updateProgress(builder, auto, "已拼接；可随时完成，匹配不可靠时会暂停")
		if builder.frames >= 80 {
			status = "达到 80 帧上限，已保留当前结果"
			break
		}
	}
	if s.discard.Load() {
		return
	}
	raw := builder.result()
	img, err := renderAnnotations(raw, s.marks)
	if err != nil {
		s.failStart(err)
		return
	}
	last := builder.last
	frames := builder.frames
	builder.parts = nil
	bmp, err := newBitmap(img)
	if err != nil {
		s.failStart(err)
		return
	}
	if err := s.d.post(func() {
		if s.d.scrolling != s || s.discard.Load() {
			bmp.close()
			return
		}
		s.result, s.lastFrame, s.bitmap = img, last, bmp
		s.rawResult = raw
		s.frames = frames
		s.height = img.Rect.Dy()
		s.status = status
		s.showPreview()
	}); err != nil {
		bmp.close()
	}
}

func (s *scrollWindow) failStart(err error) {
	_ = s.d.post(func() {
		if s.d.scrolling == s {
			s.close(true)
			if !errors.Is(err, context.Canceled) {
				s.d.report(s.d.controller, err)
			}
		}
	})
}

func (s *scrollWindow) send(action scrollAction) {
	select {
	case s.actions <- action:
	default:
	}
}

func (s *scrollWindow) resume() {
	if s.busy || !s.preview {
		return
	}
	if s.frames >= 80 || s.height >= maxLongHeight || s.result.Rect.Dx() > maxLongPixels/s.height {
		s.d.report(s.hwnd, errLongLimit)
		return
	}
	seed := &stitcher{parts: []imagePart{{s.rawResult, 0, s.rawResult.Rect.Dy()}}, last: s.lastFrame, origin: s.rawResult.Rect.Dy() - s.lastFrame.Rect.Dy(), height: s.rawResult.Rect.Dy(), frames: s.frames}
	s.bitmap.close()
	s.bitmap = nil
	s.bounds = longPanelBounds(s.region, currentMonitor(s.region.Min), image.Pt(min(px(520, uiScale(s.hwnd)), currentMonitor(s.region.Min).Dx()-16), px(104, uiScale(s.hwnd))), px(8, uiScale(s.hwnd)))
	windowAt(s.hwnd, s.bounds, false)
	call(user32, "SetForegroundWindow", s.root)
	s.startWorker(false, seed)
	invalidate(s.hwnd)
}

func (s *scrollWindow) progressText() string {
	return fmt.Sprintf("%d 帧 · %d × %d 像素", s.frames, s.region.Dx(), s.height)
}
