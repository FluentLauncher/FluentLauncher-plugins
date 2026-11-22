package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func pastedFixture() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 5, 3)) // odd width: 24-bit rows need padding
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(40 + x*40), uint8(200 - y*60), uint8(x*17 + y*31), 255})
		}
	}
	return img
}

func TestParseDIBVariants(t *testing.T) {
	want := pastedFixture()
	same := func(name string, data []byte) {
		t.Helper()
		got, err := parseDIB(data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Rect != want.Rect || !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("%s: pixels differ", name)
		}
	}
	// What this plugin writes itself: 32 bit, bottom-up.
	bottomUp := makeDIB(want)
	same("bottom-up 32", bottomUp)

	// Most producers leave the fourth byte at zero; the pin must stay opaque.
	zeroAlpha := append([]byte(nil), bottomUp...)
	for i := 43; i < len(zeroAlpha); i += 4 {
		zeroAlpha[i] = 0
	}
	same("zero alpha", zeroAlpha)

	header := func(height int32, depth uint16, compression uint32) []byte {
		h := make([]byte, 40)
		binary.LittleEndian.PutUint32(h, 40)
		binary.LittleEndian.PutUint32(h[4:], 5)
		binary.LittleEndian.PutUint32(h[8:], uint32(height))
		binary.LittleEndian.PutUint16(h[12:], 1)
		binary.LittleEndian.PutUint16(h[14:], depth)
		binary.LittleEndian.PutUint32(h[16:], compression)
		return h
	}
	// Top-down 24 bit with 4-byte row alignment (15 bytes of pixels + 1 pad).
	topDown := header(-3, 24, 0)
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			c := want.RGBAAt(x, y)
			topDown = append(topDown, c.B, c.G, c.R)
		}
		topDown = append(topDown, 0xEE)
	}
	same("top-down 24", topDown)

	// BI_BITFIELDS with channels in RGBA byte order instead of BGRA.
	fields := header(-3, 32, 3)
	for _, mask := range []uint32{0x000000ff, 0x0000ff00, 0x00ff0000} {
		fields = binary.LittleEndian.AppendUint32(fields, mask)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 5; x++ {
			c := want.RGBAAt(x, y)
			fields = append(fields, c.R, c.G, c.B, 0)
		}
	}
	same("bitfields", fields)
}

func TestParseDIBRejectsMalformedData(t *testing.T) {
	valid := makeDIB(pastedFixture())
	patched := func(offset int, value uint32) []byte {
		data := append([]byte(nil), valid...)
		binary.LittleEndian.PutUint32(data[offset:], value)
		return data
	}
	sixteen := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint16(sixteen[14:], 16)
	odd := append(patched(16, 3), make([]byte, 12)...)
	binary.LittleEndian.PutUint32(odd[40:], 0x00f80000) // a 5-bit channel
	for name, data := range map[string][]byte{
		"empty":            nil,
		"short header":     valid[:39],
		"truncated pixels": valid[:len(valid)-1],
		"zero width":       patched(4, 0),
		"negative width":   patched(4, 0xffffffff),
		"huge width":       patched(4, 1<<30),
		"huge height":      patched(8, 1<<30),
		"huge area":        patched(4, 16000), // within each side's limit, far beyond the data
		"header past end":  patched(0, 1<<20),
		"compressed":       patched(16, 1),
		"huge palette":     patched(32, 1<<31),
		"16 bit":           sixteen,
		"partial masks":    odd,
	} {
		if img, err := parseDIB(data); err == nil {
			t.Errorf("%s: accepted as %v", name, img.Rect)
		}
	}
}

func TestParsePNGFlattensTransparency(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 200, G: 10, B: 10, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{}) // fully transparent
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	img, err := parsePNG(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if img.RGBAAt(0, 0) != (color.RGBA{200, 10, 10, 255}) || img.RGBAAt(1, 0) != (color.RGBA{255, 255, 255, 255}) {
		t.Fatal("unexpected pixels:", img.Pix)
	}
	if _, err := parsePNG([]byte("not a png")); err == nil {
		t.Fatal("garbage accepted")
	}
	// A header that announces an enormous image is refused before decoding.
	huge := append([]byte(nil), buf.Bytes()...)
	binary.BigEndian.PutUint32(huge[16:], 1<<20)
	binary.BigEndian.PutUint32(huge[20:], 1<<20)
	if _, err := parsePNG(huge); err == nil {
		t.Fatal("oversized PNG accepted")
	}
}

func TestPastedPinPlacement(t *testing.T) {
	monitor := image.Rect(-1920, 0, 0, 1080)
	if got := pastedPinBounds(image.Pt(400, 200), monitor); got != image.Rect(-1160, 440, -760, 640) {
		t.Fatal("small image is not centered at its own size:", got)
	}
	got := pastedPinBounds(image.Pt(4000, 1000), monitor)
	if !got.In(monitor) || got.Dx() != 1536 || got.Dy() != 384 {
		t.Fatal("large image was not shrunk to four fifths of the monitor:", got)
	}
	if got := pastedPinBounds(image.Pt(10, 30000), monitor); !got.In(monitor) || got.Dx() < 1 {
		t.Fatal("extreme aspect ratio:", got)
	}
}

func TestEnlargeKeepsFlatAreasAndSize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 10, 200, 90, 255
	}
	img.SetRGBA(3, 1, color.RGBA{255, 255, 255, 255})
	out := enlarge(img, 2)
	if out.Rect != image.Rect(0, 0, 8, 4) {
		t.Fatal(out.Rect)
	}
	if out.RGBAAt(0, 0) != (color.RGBA{10, 200, 90, 255}) || out.RGBAAt(7, 3) != (color.RGBA{255, 255, 255, 255}) {
		t.Fatal("corners changed:", out.RGBAAt(0, 0), out.RGBAAt(7, 3))
	}
	if enlarge(img, 1) != img {
		t.Fatal("factor 1 must not copy")
	}
}
