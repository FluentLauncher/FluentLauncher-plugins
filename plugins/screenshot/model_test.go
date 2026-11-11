package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"testing"
)

func TestPinPhysicalPixelNudgeAndLock(t *testing.T) {
	for _, origin := range []image.Point{image.Pt(100, 100), image.Pt(-1920, -200)} {
		p := pinState{Original: image.Rectangle{Min: origin, Max: origin.Add(image.Pt(80, 50))}}
		p.Bounds = p.Original
		p.nudge(0x27, false)
		p.nudge(0x28, true)
		if p.Bounds != p.Original.Add(image.Pt(1, 10)) {
			t.Fatalf("physical pixels: %v", p.Bounds)
		}
		p.nudge(0x25, false)
		p.nudge(0x26, true)
		if p.Bounds != p.Original {
			t.Fatalf("round trip: %v", p.Bounds)
		}
		p.Locked = true
		p.nudge(0x25, true)
		p.move(200, 200)
		p.zoom(2)
		if p.Bounds != p.Original {
			t.Fatalf("locked image moved: %v", p.Bounds)
		}
	}
}

func TestDIBOrientationAndSubimage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.SetRGBA(1, 1, color.RGBA{R: 255, A: 255})
	img.SetRGBA(1, 2, color.RGBA{B: 255, A: 255})
	data := makeDIB(img.SubImage(image.Rect(1, 1, 3, 3)).(*image.RGBA))
	if binary.LittleEndian.Uint32(data[4:]) != 2 || len(data) != 56 {
		t.Fatalf("DIB header: %v", data[:40])
	}
	if data[40] != 255 || data[42] != 0 || data[48] != 0 || data[50] != 255 || data[51] != 255 {
		t.Fatalf("BGRA orientation: %v", data[40:])
	}
}

func TestPinZoomPreservesOriginAndAspect(t *testing.T) {
	p := pinState{Original: image.Rect(-100, 200, 100, 300), Bounds: image.Rect(-100, 200, 100, 300)}
	p.zoom(2)
	if p.Bounds != image.Rect(-100, 200, 300, 400) {
		t.Fatal(p.Bounds)
	}
	if orderedRect(image.Pt(30, 20), image.Pt(-10, -20)) != image.Rect(-10, -20, 30, 20) {
		t.Fatal("reverse drag")
	}
}
