package main

import (
	"fmt"
	"image"
	"slices"
	"time"
)

var (
	gifFPSOptions = []int{10, 15, 20}
	mp4FPSOptions = []int{15, 30, 60}
	scaleOptions  = []int{100, 75, 50}
)

type recordPreferences struct {
	Format        string `json:"format"` // "mp4" or "gif"
	GIFFPS        int    `json:"gifFps"`
	MP4FPS        int    `json:"mp4Fps"`
	Scale         int    `json:"scale"` // percent of the physical pixels
	GIFMaxSeconds int    `json:"gifMaxSeconds"`
	MP4MaxSeconds int    `json:"mp4MaxSeconds"`
	SystemAudio   bool   `json:"systemAudio"` // MP4 only
	Microphone    bool   `json:"microphone"`  // MP4 only
	SystemVolume  int    `json:"systemVolume"`
	MicVolume     int    `json:"micVolume"`
	Cursor        bool   `json:"cursor"`
	Countdown     bool   `json:"countdown"`
}

func defaultRecordPreferences() recordPreferences {
	return recordPreferences{Format: "mp4", GIFFPS: 15, MP4FPS: 30, Scale: 100, GIFMaxSeconds: 60, MP4MaxSeconds: 3600,
		SystemAudio: true, SystemVolume: 100, MicVolume: 100, Cursor: true, Countdown: true}
}

func (p recordPreferences) mp4() bool { return p.Format == "mp4" }

func (p recordPreferences) fps() int {
	if p.mp4() {
		return p.MP4FPS
	}
	return p.GIFFPS
}

func (p recordPreferences) limit() time.Duration {
	if p.mp4() {
		return time.Duration(p.MP4MaxSeconds) * time.Second
	}
	return time.Duration(p.GIFMaxSeconds) * time.Second
}

// recordFrame is the captured region and the encoded size. H.264 needs even
// sizes within the range every system encoder accepts: an odd edge loses one
// pixel instead of resampling the whole picture.
func recordFrame(region image.Rectangle, scale int, video bool) (image.Rectangle, image.Point) {
	if !video {
		return region, recordOutputSize(region.Size(), scale)
	}
	region.Max = region.Max.Sub(image.Pt(region.Dx()&1, region.Dy()&1))
	out := recordOutputSize(region.Size(), scale)
	if factor := min(4096/float64(out.X), 2304/float64(out.Y)); factor < 1 {
		out = image.Pt(int(float64(out.X)*factor), int(float64(out.Y)*factor))
	}
	return region, image.Pt(max(2, out.X&^1), max(2, out.Y&^1))
}

// normalized replaces hand-edited or outdated values with supported ones.
func (p recordPreferences) normalized() recordPreferences {
	if p.Format != "gif" {
		p.Format = "mp4"
	}
	if !slices.Contains(gifFPSOptions, p.GIFFPS) {
		p.GIFFPS = 15
	}
	if !slices.Contains(mp4FPSOptions, p.MP4FPS) {
		p.MP4FPS = 30
	}
	if !slices.Contains(scaleOptions, p.Scale) {
		p.Scale = 100
	}
	if p.GIFMaxSeconds < 5 || p.GIFMaxSeconds > 600 {
		p.GIFMaxSeconds = 60
	}
	if p.MP4MaxSeconds < 5 || p.MP4MaxSeconds > 4*3600 {
		p.MP4MaxSeconds = 3600
	}
	p.SystemVolume, p.MicVolume = max(0, min(p.SystemVolume, 100)), max(0, min(p.MicVolume, 100))
	return p
}

func nextOption(options []int, current int) int {
	return options[(slices.Index(options, current)+1)%len(options)]
}

func recordOutputSize(region image.Point, scale int) image.Point {
	return image.Pt(max(1, region.X*scale/100), max(1, region.Y*scale/100))
}

// The control bar prefers the space below or above the region, then its
// sides. A region filling the monitor gets the bar inside, near the bottom;
// the caller must then be able to exclude the bar from the capture.
func recordBarBounds(region, monitor image.Rectangle, size image.Point, gap int) image.Rectangle {
	size.X, size.Y = min(size.X, monitor.Dx()), min(size.Y, monitor.Dy())
	x := max(monitor.Min.X, min(region.Max.X-size.X, monitor.Max.X-size.X))
	y := max(monitor.Min.Y, min(region.Max.Y-size.Y, monitor.Max.Y-size.Y))
	for _, p := range []image.Point{{x, region.Max.Y + gap}, {x, region.Min.Y - gap - size.Y}, {region.Max.X + gap, y}, {region.Min.X - gap - size.X, y}} {
		if r := (image.Rectangle{Min: p, Max: p.Add(size)}); r.In(monitor) {
			return r
		}
	}
	p := image.Pt(monitor.Min.X+(monitor.Dx()-size.X)/2, max(monitor.Min.Y, monitor.Max.Y-size.Y-gap*6))
	return image.Rectangle{Min: p, Max: p.Add(size)}
}

func recordingName(now time.Time, extension string) string {
	return "Recording_" + now.Format("20060102_150405.000000000") + "." + extension
}

func formatClock(d time.Duration) string {
	s := int(d / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func formatSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
