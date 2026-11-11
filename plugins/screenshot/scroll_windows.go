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

type longSeed struct {
	first *image.RGBA
	marks []annotation
}

type scrollWindow struct {
	d                                *desktop
	hwnd                             uintptr
	region, bounds                   image.Rectangle
	ctx                              context.Context
	cancel                           context.CancelFunc
	discard, finishRequested, paused atomic.Bool
	preview, busy                    bool
	status                           string
	frames, height                   int
	result, rawResult, lastFrame     *image.RGBA
	bitmap, thumbnail                *bitmap
	paintBuffer                      paintBuffer
	offset, hover, focus             int
	dragScroll                       bool
	marks                            []annotation
	capturePinsHidden, themeDeferred bool
	excludedFromCapture              bool
}

func longPanelBounds(region, monitor image.Rectangle, size image.Point, gap int) image.Rectangle {
	size.X = min(size.X, monitor.Dx())
	size.Y = min(size.Y, monitor.Dy())
	x := max(monitor.Min.X, min(region.Min.X, monitor.Max.X-size.X))
	y := max(monitor.Min.Y, min(region.Min.Y, monitor.Max.Y-size.Y))
	candidates := []image.Point{image.Pt(region.Max.X+gap, y), image.Pt(region.Min.X-gap-size.X, y), image.Pt(x, region.Min.Y-gap-size.Y), image.Pt(x, region.Max.Y+gap)}
	for _, p := range candidates {
		r := image.Rectangle{Min: p, Max: p.Add(size)}
		if r.In(monitor) && !r.Overlaps(region) {
			return r
		}
	}
	p := image.Pt(max(monitor.Min.X, monitor.Max.X-size.X-gap), y)
	return image.Rectangle{Min: p, Max: p.Add(size)}
}

func (d *desktop) startLongCapture(region image.Rectangle, scale float64, seeds ...longSeed) {
	if region.Dx() < 64 || region.Dy() < 96 {
		d.report(d.controller, errors.New("长截图选区至少需要 64×96 像素，请重新选择内容区域"))
		return
	}
	if region.Dx() > maxLongPixels/region.Dy() {
		d.report(d.controller, errLongLimit)
		return
	}
	// Manual screen stitching does not drive a target HWND. Window chrome,
	// child HWNDs and invisible resize borders must not reject a screen crop.
	d.openLongCapture(region, scale, seeds...)
}

func (d *desktop) openLongCapture(region image.Rectangle, scale float64, seeds ...longSeed) {
	monitor := currentMonitor(region.Min.Add(region.Size().Div(2)))
	size := image.Pt(min(px(280, scale), monitor.Dx()), min(px(420, scale), monitor.Dy()))
	bounds := longPanelBounds(region, monitor, size, px(10, scale))
	s := &scrollWindow{d: d, region: region, bounds: bounds, hover: -1, status: "滚动页面，内容会自动拼接"}
	s.hwnd = d.createWindow("FLScreenshotLong", "长截图预览", wsExTopmost|wsExToolwindow, d.controller, bounds)
	if s.hwnd == 0 {
		d.report(d.controller, errors.New("无法创建长截图预览"))
		return
	}
	// Exclude the preview instead of blinking it off for every sample.
	s.excludedFromCapture = call(user32, "SetWindowDisplayAffinity", s.hwnd, 0x11) != 0
	if bounds.Overlaps(region) && !s.excludedFromCapture {
		call(user32, "DestroyWindow", s.hwnd)
		d.report(d.controller, errors.New("系统无法排除预览窗口，请缩小选区，给预览留出旁边的位置"))
		return
	}
	d.scrolling = s
	d.capturing.Store(true)
	d.active.Add(1)
	var seed *stitcher
	if len(seeds) > 0 {
		seed = newStitcher(seeds[0].first)
		s.marks = seeds[0].marks
	}
	windowAt(s.hwnd, bounds, false)
	s.startWorker(seed)
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
	s.thumbnail.close()
	s.thumbnail = nil
	s.paintBuffer.close()
	s.d.active.Add(-1)
	s.d.capturing.Store(false)
}

func (s *scrollWindow) startWorker(seed *stitcher) {
	s.preview = false
	s.finishRequested.Store(false)
	s.paused.Store(false)
	s.hover = -1
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	// Hide excluded pins once for the session, restoring on finish/cancel.
	if !s.d.preferences.IncludePins {
		s.capturePinsHidden = true
		for _, p := range s.d.pins {
			call(user32, "ShowWindow", p.hwnd, 0)
			call(user32, "ShowWindow", p.shadow, 0)
		}
	}
	s.d.active.Add(1)
	go s.collect(s.ctx, seed)
}

func (s *scrollWindow) restoreCaptureUI() {
	if s.capturePinsHidden {
		s.capturePinsHidden = false
		for _, hwnd := range s.d.pinOrder {
			p := s.d.pins[hwnd]
			if !p.state.Hidden {
				p.syncWindow()
			}
		}
	}
	if s.themeDeferred {
		s.themeDeferred = false
		s.d.applyTheme(s.d.theme)
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.d.captureLong != nil {
		return s.d.captureLong(s.region)
	}
	return captureScreen(s.region)
}

func (s *scrollWindow) updateLive(builder *stitcher, status string) {
	img := builder.thumbnail(280, 1400)
	factor := float64(img.Rect.Dx()) / float64(builder.last.Rect.Dx())
	marks := make([]annotation, len(s.marks))
	for i, m := range s.marks {
		marks[i] = m
		marks[i].points = make([]image.Point, len(m.points))
		marks[i].width = max(1, int(float64(m.width)*factor))
		for j, p := range m.points {
			marks[i].points[j] = image.Pt(int(float64(p.X)*factor), int(float64(p.Y)*factor))
		}
	}
	img, err := renderAnnotations(img, marks)
	if err != nil {
		s.publishStatus(err.Error())
		return
	}
	b, err := newBitmap(img)
	if err != nil {
		s.publishStatus(err.Error())
		return
	}
	frames, height := builder.frames, builder.height
	if err := s.d.post(func() {
		if s.d.scrolling != s || s.preview {
			b.close()
			return
		}
		s.thumbnail.close()
		s.thumbnail = b
		s.frames = frames
		s.height = height
		s.status = status
		invalidate(s.hwnd)
	}); err != nil {
		b.close()
	}
}

func (s *scrollWindow) publishStatus(status string) {
	_ = s.d.post(func() {
		if s.d.scrolling == s && !s.preview && s.status != status {
			s.status = status
			invalidate(s.hwnd)
		}
	})
}

func (s *scrollWindow) collect(ctx context.Context, builder *stitcher) {
	defer s.d.active.Add(-1)
	if builder == nil {
		first, err := s.capture(ctx)
		if err != nil {
			s.failStart(err)
			return
		}
		builder = newStitcher(first)
	}
	s.updateLive(builder, "滚动页面，内容会自动拼接")
	ticker := time.NewTicker(140 * time.Millisecond)
	defer ticker.Stop()
	var pending *image.RGBA
	status := "采集完成"
collection:
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "达到 2 分钟上限，已保留当前结果"
			}
			break collection
		case <-ticker.C:
		}
		if s.paused.Load() {
			pending = nil
			continue
		}
		frame, err := s.capture(ctx)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			status = err.Error()
			break
		}
		if s.paused.Load() {
			pending = nil
			continue
		}
		// Wait for two settled samples instead of stitching animation frames.
		// A still page stays live until the user chooses Finish.
		if pending == nil || frameDifference(pending, frame) >= 0.7 {
			pending = frame
			continue
		}
		pending = nil
		added, err := builder.add(frame)
		if errors.Is(err, errLongLimit) {
			status = err.Error()
			break
		}
		if err != nil {
			s.publishStatus("未拼接：请慢慢滚动，或回滚一些再试")
			continue
		}
		if !added {
			continue
		}
		s.updateLive(builder, "已拼接 · 继续滚动页面，完成后点完成")
		if builder.frames >= 80 {
			status = "达到 80 帧上限，已保留当前结果"
			break
		}
	}
	if s.discard.Load() {
		return
	}
	// Include the last settled viewport even if Finish was clicked between two
	// polling ticks. Cancellation/close still discards without further sampling.
	if s.finishRequested.Load() && builder.frames < 80 {
		finalCtx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		last, err := s.capture(finalCtx)
		if err == nil && waitScroll(finalCtx, 90*time.Millisecond) && !s.discard.Load() {
			if next, err := s.capture(finalCtx); err == nil && frameDifference(last, next) < 0.7 {
				if _, err := builder.add(next); err != nil {
					status = "已保留结果，最后一屏未找到可靠重叠"
				}
			}
		}
		cancel()
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
	last, frames := builder.last, builder.frames
	b, err := newBitmap(img)
	if err != nil {
		s.failStart(err)
		return
	}
	if err := s.d.post(func() {
		if s.d.scrolling != s || s.discard.Load() {
			b.close()
			return
		}
		s.bitmap.close()
		s.bitmap = b
		s.result = img
		s.rawResult = raw
		s.lastFrame = last
		s.frames = frames
		s.height = img.Rect.Dy()
		s.status = status
		s.restoreCaptureUI()
		s.showPreview()
	}); err != nil {
		b.close()
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

func (s *scrollWindow) resume() {
	if s.busy || !s.preview {
		return
	}
	if s.frames >= 80 || s.height >= maxLongHeight || s.result.Rect.Dx() > maxLongPixels/s.height {
		s.d.report(s.hwnd, errLongLimit)
		return
	}
	seed := &stitcher{parts: []imagePart{{s.rawResult, 0, s.rawResult.Rect.Dy()}}, last: s.lastFrame, origin: s.rawResult.Rect.Dy() - s.lastFrame.Rect.Dy(), height: s.rawResult.Rect.Dy(), frames: s.frames}
	s.startWorker(seed)
	invalidate(s.hwnd)
}

func (s *scrollWindow) progressText() string {
	return fmt.Sprintf("%d 帧 · %d × %d", s.frames, s.region.Dx(), s.height)
}
