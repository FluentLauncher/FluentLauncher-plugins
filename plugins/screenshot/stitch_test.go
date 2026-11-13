package main

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"testing"
)

func scrollFixture(width, height int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := uint32(y+1)*2654435761 ^ uint32(x+19)*2246822519
			v ^= v >> 13
			v *= 3266489917
			img.SetRGBA(x, y, color.RGBA{uint8(v), uint8(v >> 8), uint8(v >> 16), 255})
		}
	}
	return img
}

func fixtureViewport(content *image.RGBA, offset, height, header, footer int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, content.Rect.Dx(), height))
	for y := 0; y < height; y++ {
		for x := 0; x < out.Rect.Dx(); x++ {
			var c color.RGBA
			switch {
			case y < header:
				c = color.RGBA{uint8(x * 7), uint8(y * 13), 150, 255}
			case y >= height-footer:
				c = color.RGBA{uint8(x * 11), 150, uint8(y * 9), 255}
			default:
				c = content.RGBAAt(x, offset+y-header)
			}
			out.SetRGBA(x, y, c)
		}
	}
	return out
}

func TestLongStitchPreservesPixelsAndFixedBarsOnce(t *testing.T) {
	content := scrollFixture(96, 1600)
	const h, header, footer = 240, 24, 18
	first := fixtureViewport(content, 0, h, header, footer)
	s := newStitcher(first)
	for _, offset := range []int{75, 150, 225, 260} {
		added, err := s.add(fixtureViewport(content, offset, h, header, footer))
		if err != nil || !added {
			t.Fatalf("offset %d: %v / %v", offset, added, err)
		}
	}
	last := fixtureViewport(content, 260, h, header, footer)
	if added, err := s.add(last); added || err != nil {
		t.Fatal("bottom frame duplicated", added, err)
	}
	result := s.result()
	if result.Rect.Dy() != h+260 {
		t.Fatal("wrong height", result.Rect)
	}
	for y := 0; y < result.Rect.Dy(); y++ {
		for x := 0; x < result.Rect.Dx(); x++ {
			want := color.RGBA{}
			switch {
			case y < header:
				want = first.RGBAAt(x, y)
			case y >= result.Rect.Dy()-footer:
				want = last.RGBAAt(x, y-260)
			default:
				want = content.RGBAAt(x, y-header)
			}
			if got := result.RGBAAt(x, y); got != want {
				t.Fatalf("stitch mismatch at %d,%d: %v != %v", x, y, got, want)
			}
		}
	}
}

func TestLongStitchRejectsJumpAndPreservesPartial(t *testing.T) {
	content := scrollFixture(96, 1800)
	s := newStitcher(fixtureViewport(content, 0, 240, 20, 15))
	if _, err := s.add(fixtureViewport(content, 80, 240, 20, 15)); err != nil {
		t.Fatal(err)
	}
	before := s.result()
	if added, err := s.add(fixtureViewport(content, 700, 240, 20, 15)); err == nil || added {
		t.Fatal("non-overlap accepted")
	}
	if after := s.result(); !bytes.Equal(before.Pix, after.Pix) {
		t.Fatal("failure modified captured result")
	}
	s.height = maxLongHeight
	if added, err := s.add(fixtureViewport(content, 160, 240, 20, 15)); added || !errors.Is(err, errLongLimit) {
		t.Fatal("limit ignored", added, err)
	}
}

func TestLongStitchRejectsAmbiguousRepeatedRows(t *testing.T) {
	pattern := image.NewRGBA(image.Rect(0, 0, 96, 1200))
	base := scrollFixture(96, 24)
	for y := 0; y < 1200; y++ {
		for x := 0; x < 96; x++ {
			pattern.SetRGBA(x, y, base.RGBAAt(x, y%24))
		}
	}
	s := newStitcher(fixtureViewport(pattern, 0, 240, 0, 0))
	if _, err := s.add(fixtureViewport(pattern, 75, 240, 0, 0)); !errors.Is(err, errAmbiguous) {
		t.Fatal("ambiguous periodic pattern accepted", err)
	}
}

func TestLongStitchToleratesSmallRenderingChanges(t *testing.T) {
	content := scrollFixture(96, 1000)
	first := fixtureViewport(content, 0, 240, 24, 18)
	next := fixtureViewport(content, 80, 240, 24, 18)
	for i := range next.Pix {
		if i%4 != 3 {
			next.Pix[i] = uint8(min(255, int(next.Pix[i])+2))
		}
	}
	match, err := findOverlap(first, next)
	if err != nil || match.shift != 80 {
		t.Fatalf("rendering changes lost the overlap: %+v, %v", match, err)
	}
}

func TestLongThumbnailIncludesAllRetainedSegments(t *testing.T) {
	content := scrollFixture(96, 1000)
	s := newStitcher(fixtureViewport(content, 0, 240, 24, 18))
	for _, offset := range []int{80, 160, 240} {
		if _, err := s.add(fixtureViewport(content, offset, 240, 24, 18)); err != nil {
			t.Fatal(err)
		}
	}
	full := s.result()
	for _, size := range []image.Point{image.Pt(48, 240), image.Pt(24, 120)} {
		thumb := s.thumbnail(size.X, size.Y)
		if thumb.Rect.Dx() > size.X || thumb.Rect.Dy() > size.Y {
			t.Fatal("thumbnail exceeded bounds", thumb.Rect)
		}
		for y := 0; y < thumb.Rect.Dy(); y++ {
			for x := 0; x < thumb.Rect.Dx(); x++ {
				step := full.Rect.Dx() / thumb.Rect.Dx()
				sx, sy := x*step+step/2-1, y*step+step/2-1
				a, b, c, d := full.RGBAAt(sx, sy), full.RGBAAt(sx+1, sy), full.RGBAAt(sx, sy+1), full.RGBAAt(sx+1, sy+1)
				mean := func(a, b, c, d uint8) uint8 { return uint8((int(a) + int(b) + int(c) + int(d) + 2) / 4) }
				want := color.RGBA{mean(a.R, b.R, c.R, d.R), mean(a.G, b.G, c.G, d.G), mean(a.B, b.B, c.B, d.B), 255}
				if thumb.RGBAAt(x, y) != want {
					t.Fatalf("thumbnail seam mismatch at %d,%d", x, y)
				}
			}
		}
	}
}
