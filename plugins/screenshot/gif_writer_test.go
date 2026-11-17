package main

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"testing"
	"time"
)

func bgraFrame(w, h int, c color.RGBA) []byte {
	pix := make([]byte, w*h*4)
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+1], pix[i+2], pix[i+3] = c.B, c.G, c.R, 255
	}
	return pix
}

func paintBGRA(pix []byte, w int, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			i := (y*w + x) * 4
			pix[i], pix[i+1], pix[i+2] = c.B, c.G, c.R
		}
	}
}

// Replays the animation the way a player does: every frame is drawn over the
// previous canvas, transparent pixels keep what was there.
func composeGIF(t *testing.T, data []byte) (*gif.GIF, []*image.RGBA) {
	t.Helper()
	decoded, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, decoded.Config.Width, decoded.Config.Height))
	var frames []*image.RGBA
	for _, frame := range decoded.Image {
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		copied := image.NewRGBA(canvas.Bounds())
		copy(copied.Pix, canvas.Pix)
		frames = append(frames, copied)
	}
	return decoded, frames
}

func TestGIFWriterDeltaFramesAndDelays(t *testing.T) {
	const w, h = 40, 30
	white, red := color.RGBA{255, 255, 255, 255}, color.RGBA{255, 0, 0, 255}
	var out bytes.Buffer
	g, err := newGIFWriter(&out, w, h)
	if err != nil {
		t.Fatal(err)
	}
	frame := bgraFrame(w, h, white)
	box := image.Rect(10, 8, 22, 17)
	steps := []func(){func() {}, func() {}, func() { paintBGRA(frame, w, box, red) }, func() {}}
	for i, step := range steps {
		step()
		if err := g.addFrame(frame, w*4, time.Duration(i)*100*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.close(450 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if g.addFrame(frame, w*4, time.Second) == nil {
		t.Fatal("closed writer accepted a frame")
	}
	if g.size() != int64(out.Len()) {
		t.Fatalf("size %d, written %d", g.size(), out.Len())
	}
	decoded, frames := composeGIF(t, out.Bytes())
	if len(frames) != 2 || decoded.LoopCount != 0 {
		t.Fatalf("frames %d loop %d: identical frames must merge", len(frames), decoded.LoopCount)
	}
	if decoded.Delay[0] != 20 || decoded.Delay[1] != 25 {
		t.Fatalf("delays %v", decoded.Delay)
	}
	if decoded.Image[1].Bounds() != box {
		t.Fatalf("delta frame covers %v, want %v", decoded.Image[1].Bounds(), box)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			want := white
			if frames[0].RGBAAt(x, y) != white {
				t.Fatalf("first frame %d,%d = %v", x, y, frames[0].RGBAAt(x, y))
			}
			if image.Pt(x, y).In(box) {
				want = red
			}
			if frames[1].RGBAAt(x, y) != want {
				t.Fatalf("second frame %d,%d = %v, want %v", x, y, frames[1].RGBAAt(x, y), want)
			}
		}
	}
}

// Dithered gradients must stay close to the source and cover pixels that
// change back to an earlier value (transparency must not leak old content).
func TestGIFWriterDitherAccuracyAndRestore(t *testing.T) {
	const w, h = 64, 48
	gradient := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			gradient[i], gradient[i+1], gradient[i+2], gradient[i+3] = uint8(x*4), uint8(y*5), uint8((x+y)*2), 255
		}
	}
	covered := append([]byte(nil), gradient...)
	paintBGRA(covered, w, image.Rect(5, 5, 60, 40), color.RGBA{0, 0, 0, 255})
	var out bytes.Buffer
	g, _ := newGIFWriter(&out, w, h)
	for i, frame := range [][]byte{gradient, covered, gradient} {
		if err := g.addFrame(frame, w*4, time.Duration(i)*50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.close(150 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	_, frames := composeGIF(t, out.Bytes())
	if len(frames) != 3 {
		t.Fatalf("frames %d", len(frames))
	}
	if !bytes.Equal(frames[0].Pix, frames[2].Pix) {
		t.Fatal("restored content differs from the first frame")
	}
	// 4×4 block averages undo the ordered dither; one palette step is 51.
	for by := 0; by < h; by += 4 {
		for bx := 0; bx < w; bx += 4 {
			var got, want [3]int
			for y := by; y < by+4; y++ {
				for x := bx; x < bx+4; x++ {
					c, i := frames[0].RGBAAt(x, y), (y*w+x)*4
					got[0], got[1], got[2] = got[0]+int(c.R), got[1]+int(c.G), got[2]+int(c.B)
					want[0], want[1], want[2] = want[0]+int(gradient[i+2]), want[1]+int(gradient[i+1]), want[2]+int(gradient[i])
				}
			}
			for c := range 3 {
				if d := (got[c] - want[c]) / 16; d < -14 || d > 14 {
					t.Fatalf("block %d,%d channel %d off by %d", bx, by, c, d)
				}
			}
		}
	}
}

func TestGIFWriterRejectsBadInput(t *testing.T) {
	if _, err := newGIFWriter(&bytes.Buffer{}, 0, 10); err == nil {
		t.Fatal("empty size accepted")
	}
	g, _ := newGIFWriter(&bytes.Buffer{}, 8, 8)
	if g.addFrame(make([]byte, 16), 32, 0) == nil {
		t.Fatal("short frame accepted")
	}
	if g.close(0) == nil {
		t.Fatal("empty recording produced a file")
	}
}

func TestRecordModel(t *testing.T) {
	p := recordPreferences{GIFFPS: 99, GIFScale: 1, GIFMaxSeconds: 0}.normalized()
	if p.GIFFPS != 15 || p.GIFScale != 100 || p.GIFMaxSeconds != 60 {
		t.Fatal(p)
	}
	if nextOption(gifFPSOptions, 20) != 10 || nextOption(gifScaleOptions, 100) != 75 {
		t.Fatal("option cycle")
	}
	if recordOutputSize(image.Pt(641, 3), 50) != image.Pt(320, 1) {
		t.Fatal("output size")
	}
	if formatClock(75*time.Second) != "01:15" || formatSize(1) != "1 KB" || formatSize(3<<19) != "1.5 MB" {
		t.Fatal(formatClock(75*time.Second), formatSize(1), formatSize(3<<19))
	}
	monitor, size := image.Rect(0, 0, 1920, 1080), image.Pt(340, 64)
	for name, c := range map[string]struct{ region, want image.Rectangle }{
		"below":  {image.Rect(100, 100, 900, 600), image.Rect(560, 610, 900, 674)},
		"above":  {image.Rect(100, 300, 900, 1080), image.Rect(560, 226, 900, 290)},
		"right":  {image.Rect(0, 0, 900, 1080), image.Rect(910, 1016, 1250, 1080)},
		"inside": {monitor, image.Rect(790, 956, 1130, 1020)},
	} {
		if got := recordBarBounds(c.region, monitor, size, 10); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
