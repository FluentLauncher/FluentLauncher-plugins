package main

import (
	"fmt"
	"image"
	"slices"
	"time"
)

var (
	gifFPSOptions   = []int{10, 15, 20}
	gifScaleOptions = []int{100, 75, 50}
)

type recordPreferences struct {
	GIFFPS        int  `json:"gifFps"`
	GIFScale      int  `json:"gifScale"` // percent of the physical pixels
	GIFMaxSeconds int  `json:"gifMaxSeconds"`
	Cursor        bool `json:"cursor"`
	Countdown     bool `json:"countdown"`
}

func defaultRecordPreferences() recordPreferences {
	return recordPreferences{GIFFPS: 15, GIFScale: 100, GIFMaxSeconds: 60, Cursor: true, Countdown: true}
}

// normalized replaces hand-edited or outdated values with supported ones.
func (p recordPreferences) normalized() recordPreferences {
	if !slices.Contains(gifFPSOptions, p.GIFFPS) {
		p.GIFFPS = 15
	}
	if !slices.Contains(gifScaleOptions, p.GIFScale) {
		p.GIFScale = 100
	}
	if p.GIFMaxSeconds < 5 || p.GIFMaxSeconds > 600 {
		p.GIFMaxSeconds = 60
	}
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

func recordingName(now time.Time) string {
	return "Recording_" + now.Format("20060102_150405.000000000") + ".gif"
}

func formatClock(d time.Duration) string {
	s := int(d / time.Second)
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func formatSize(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
