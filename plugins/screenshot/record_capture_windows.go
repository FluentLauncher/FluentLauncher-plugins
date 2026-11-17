//go:build windows

package main

import (
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// frameSource delivers top-down BGRA frames of a fixed size. It is created,
// used and closed on one locked OS thread. Later encoders (MP4) and faster
// capture back ends plug in here.
type frameSource interface {
	size() image.Point
	grab(dst []byte) error
	close()
}

type dibSurface struct {
	dc, bitmap, original, address uintptr
	size                          image.Point
}

func newDIBSurface(reference uintptr, size image.Point) (*dibSurface, error) {
	s := &dibSurface{size: size, dc: call(gdi32, "CreateCompatibleDC", reference)}
	header := bitmapHeader{Size: 40, Width: int32(size.X), Height: -int32(size.Y), Planes: 1, Bits: 32}
	s.bitmap = call(gdi32, "CreateDIBSection", reference, ptr(&header), 0, ptr(&s.address), 0, 0)
	if s.dc == 0 || s.bitmap == 0 {
		s.close()
		return nil, errors.New("无法分配录制缓冲")
	}
	s.original = call(gdi32, "SelectObject", s.dc, s.bitmap)
	return s, nil
}

func (s *dibSurface) close() {
	if s == nil {
		return
	}
	if s.original != 0 {
		call(gdi32, "SelectObject", s.dc, s.original)
	}
	if s.bitmap != 0 {
		call(gdi32, "DeleteObject", s.bitmap)
	}
	if s.dc != 0 {
		call(gdi32, "DeleteDC", s.dc)
	}
}

// screenSource copies the composed desktop with GDI. Windows excluded from
// capture (the recording bar and outline) are left out by the compositor.
type screenSource struct {
	region       image.Rectangle
	screen       uintptr
	full, scaled *dibSurface
	cursor       bool
	cursorHandle uintptr
	hotspot      image.Point
}

func newScreenSource(region image.Rectangle, out image.Point, cursor bool) (frameSource, error) {
	s := &screenSource{region: region, cursor: cursor, screen: call(user32, "GetDC", 0)}
	if s.screen == 0 {
		return nil, errors.New("无法读取桌面")
	}
	var err error
	if s.full, err = newDIBSurface(s.screen, region.Size()); err == nil && out != region.Size() {
		s.scaled, err = newDIBSurface(s.screen, out)
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *screenSource) size() image.Point {
	if s.scaled != nil {
		return s.scaled.size
	}
	return s.full.size
}

func (s *screenSource) close() {
	s.full.close()
	s.scaled.close()
	call(user32, "ReleaseDC", 0, s.screen)
}

func (s *screenSource) grab(dst []byte) error {
	w, h := s.region.Dx(), s.region.Dy()
	if call(gdi32, "BitBlt", s.full.dc, 0, 0, w, h, s.screen, coord(s.region.Min.X), coord(s.region.Min.Y), srccopy|0x40000000) == 0 {
		return errors.New("桌面捕获失败；受保护内容可能无法录制")
	}
	if s.cursor {
		s.drawCursor()
	}
	target := s.full
	if s.scaled != nil {
		target = s.scaled
		call(gdi32, "SetStretchBltMode", target.dc, 4) // HALFTONE keeps downscaled text readable.
		call(gdi32, "SetBrushOrgEx", target.dc, 0, 0, 0)
		call(gdi32, "StretchBlt", target.dc, 0, 0, target.size.X, target.size.Y, s.full.dc, 0, 0, w, h, srccopy)
	}
	call(gdi32, "GdiFlush")
	return windows.ReadProcessMemory(windows.CurrentProcess(), target.address, &dst[0], uintptr(target.size.X*target.size.Y*4), nil)
}

type cursorInfo struct {
	Size, Flags uint32
	Cursor      uintptr
	Position    point
}
type iconInfo struct {
	Icon             int32
	HotspotX         uint32
	HotspotY         uint32
	Mask, ColorImage uintptr
}

// BitBlt never contains the pointer; paint the current one at its hotspot.
func (s *screenSource) drawCursor() {
	info := cursorInfo{Size: 24}
	if call(user32, "GetCursorInfo", ptr(&info)) == 0 || info.Flags&1 == 0 || info.Cursor == 0 {
		return
	}
	if info.Cursor != s.cursorHandle {
		var icon iconInfo
		if call(user32, "GetIconInfo", info.Cursor, ptr(&icon)) == 0 {
			return
		}
		call(gdi32, "DeleteObject", icon.Mask)
		if icon.ColorImage != 0 {
			call(gdi32, "DeleteObject", icon.ColorImage)
		}
		s.cursorHandle, s.hotspot = info.Cursor, image.Pt(int(icon.HotspotX), int(icon.HotspotY))
	}
	p := image.Pt(int(info.Position.X), int(info.Position.Y)).Sub(s.hotspot).Sub(s.region.Min)
	call(user32, "DrawIconEx", s.full.dc, coord(p.X), coord(p.Y), info.Cursor, 0, 0, 0, 0, 3)
}

// recorder owns one recording from the first frame to the finished file. The
// GUI thread only flips its flags and reads its counters.
type recorder struct {
	open     func() (frameSource, error)
	dir      string
	fps      int
	limit    time.Duration
	stop     chan struct{}
	stopOnce sync.Once
	discard  atomic.Bool
	paused   atomic.Bool
	elapsed  atomic.Int64 // recorded time in nanoseconds, pauses excluded
	bytes    atomic.Int64
	done     chan struct{}

	// Valid once done is closed.
	path    string
	limited bool
	err     error
}

func newRecorder(open func() (frameSource, error), dir string, fps int, limit time.Duration) *recorder {
	return &recorder{open: open, dir: dir, fps: fps, limit: limit, stop: make(chan struct{}), done: make(chan struct{})}
}

// finish ends the capture loop; discard removes the unfinished file instead
// of completing it.
func (r *recorder) finish(discard bool) {
	if discard {
		r.discard.Store(true)
	}
	r.stopOnce.Do(func() { close(r.stop) })
}

func (r *recorder) run() {
	defer close(r.done)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r.err = r.record()
}

func (r *recorder) record() error {
	source, err := r.open()
	if err != nil {
		return err
	}
	defer source.close()
	if err := os.MkdirAll(r.dir, 0755); err != nil {
		return err
	}
	final := filepath.Join(r.dir, recordingName(time.Now()))
	partial := final + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	// A killed process leaves only the .partial file, never a broken recording.
	size := source.size()
	writer, err := newGIFWriter(file, size.X, size.Y)
	interval := time.Second / time.Duration(r.fps)
	var active time.Duration
	if err == nil {
		frame := make([]byte, size.X*size.Y*4)
		if err = source.grab(frame); err == nil {
			err = writer.addFrame(frame, size.X*4, 0)
		}
		r.bytes.Store(writer.size())
		ticker := time.NewTicker(interval)
		last := time.Now()
	capture:
		for err == nil {
			select {
			case <-r.stop:
				break capture
			case now := <-ticker.C:
				step := now.Sub(last)
				last = now
				if r.paused.Load() {
					continue
				}
				active += step
				// A locked or secure desktop cannot be copied; keep the last
				// frame on screen and continue when the desktop returns.
				if source.grab(frame) == nil {
					err = writer.addFrame(frame, size.X*4, active)
				}
				r.elapsed.Store(int64(active))
				r.bytes.Store(writer.size())
				if active >= r.limit {
					r.limited = true
					break capture
				}
			}
		}
		ticker.Stop()
	}
	if err == nil && !r.discard.Load() {
		err = writer.close(active + interval)
		r.bytes.Store(writer.size())
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && !r.discard.Load() {
		if err = os.Rename(partial, final); err == nil {
			r.path = final
			return nil
		}
	}
	_ = os.Remove(partial)
	return err
}
