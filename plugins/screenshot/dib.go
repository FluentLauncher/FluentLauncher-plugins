package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/bits"
)

// Clipboard images come from other applications: every size is checked
// before memory is allocated for it.
const (
	maxPastedSide   = 16384
	maxPastedPixels = 64 << 20
)

var errPastedFormat = errors.New("不支持剪贴板里的这种图片格式")

func pastedSize(w, h int) error {
	if w <= 0 || h <= 0 || w > maxPastedSide || h > maxPastedSide || w > maxPastedPixels/h {
		return errors.New("剪贴板里的图片尺寸无效或过大")
	}
	return nil
}

// parseDIB reads CF_DIB: an uncompressed 24 or 32 bit bitmap, bottom-up or
// top-down, with optional channel masks. Pins are opaque, and most producers
// leave the fourth byte at zero, so alpha is not read.
func parseDIB(data []byte) (*image.RGBA, error) {
	if len(data) < 40 {
		return nil, errPastedFormat
	}
	header := int(binary.LittleEndian.Uint32(data))
	w, h := int(int32(binary.LittleEndian.Uint32(data[4:]))), int(int32(binary.LittleEndian.Uint32(data[8:])))
	depth, compression := int(binary.LittleEndian.Uint16(data[14:])), binary.LittleEndian.Uint32(data[16:])
	palette := int(binary.LittleEndian.Uint32(data[32:]))
	topDown := h < 0
	if topDown {
		h = -h
	}
	if header < 40 || header > len(data) || (depth != 24 && depth != 32) || palette < 0 || palette > 256 ||
		(compression != 0 && !(compression == 3 && depth == 32)) {
		return nil, errPastedFormat
	}
	if err := pastedSize(w, h); err != nil {
		return nil, err
	}
	masks := [3]uint32{0x00ff0000, 0x0000ff00, 0x000000ff}
	offset := header + palette*4
	if compression == 3 {
		// BITMAPINFOHEADER is followed by the masks; larger headers contain them.
		if header == 40 {
			offset += 12
		}
		if len(data) < 52 {
			return nil, errPastedFormat
		}
		for i := range masks {
			masks[i] = binary.LittleEndian.Uint32(data[40+i*4:])
		}
	}
	var shift [3]int
	for i, mask := range masks {
		shift[i] = bits.TrailingZeros32(mask)
		if mask == 0 || mask>>shift[i] != 0xff {
			return nil, errPastedFormat // only whole-byte channels
		}
	}
	stride := (w*depth + 31) / 32 * 4
	if offset > len(data) || stride > (len(data)-offset)/h {
		return nil, errPastedFormat
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	step := depth / 8
	for y := 0; y < h; y++ {
		row := y
		if !topDown {
			row = h - 1 - y
		}
		src := data[offset+row*stride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			var v uint32
			if step == 4 {
				v = binary.LittleEndian.Uint32(src[x*4:])
			} else {
				v = uint32(src[x*3]) | uint32(src[x*3+1])<<8 | uint32(src[x*3+2])<<16
			}
			dst[x*4], dst[x*4+1], dst[x*4+2], dst[x*4+3] = uint8(v>>shift[0]), uint8(v>>shift[1]), uint8(v>>shift[2]), 255
		}
	}
	return img, nil
}

// parsePNG flattens transparency onto white: a pin has no see-through parts.
func parsePNG(data []byte) (*image.RGBA, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errPastedFormat
	}
	if err := pastedSize(config.Width, config.Height); err != nil {
		return nil, err
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errPastedFormat
	}
	img := image.NewRGBA(image.Rect(0, 0, config.Width, config.Height))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(img, img.Bounds(), decoded, decoded.Bounds().Min, draw.Over)
	return img, nil
}

// pastedPinBounds centers the image on the monitor, shrunk to four fifths of
// it when larger. The pin keeps the original pixels for copy and save.
func pastedPinBounds(size image.Point, monitor image.Rectangle) image.Rectangle {
	factor := min(1.0, float64(monitor.Dx()*4/5)/float64(size.X), float64(monitor.Dy()*4/5)/float64(size.Y))
	shown := image.Pt(max(1, int(float64(size.X)*factor)), max(1, int(float64(size.Y)*factor)))
	pos := monitor.Min.Add(monitor.Size().Sub(shown).Div(2))
	return image.Rectangle{Min: pos, Max: pos.Add(shown)}
}
