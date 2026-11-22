package main

import (
	"image"
	"strings"
	"unicode"
)

// The system recognizer returns every Chinese or Japanese character as its
// own word and joins words with spaces. Spaces between two such characters,
// or next to full-width punctuation, are noise; Latin words keep theirs.
func ocrPunctuation(r rune) bool {
	return (r > 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFF0F) || (r >= 0xFF1A && r <= 0xFF20) || (r >= 0xFF3B && r <= 0xFF40) || (r >= 0xFF5B && r <= 0xFF65)
}
func ocrWide(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || ocrPunctuation(r)
}

func tidyOCRLine(line string) string {
	runes := []rune(strings.TrimSpace(line))
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == ' ' && i > 0 && i+1 < len(runes) && (ocrWide(runes[i-1]) && ocrWide(runes[i+1]) || ocrPunctuation(runes[i-1]) || ocrPunctuation(runes[i+1])) {
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

// joinOCRLines keeps the recognizer's line breaks and drops empty lines.
func joinOCRLines(lines []string) string {
	var out []string
	for _, line := range lines {
		if line = tidyOCRLine(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\r\n")
}

// enlarge resamples with bilinear interpolation. Screen text is only a
// dozen pixels tall; the recognizer reads it far better when enlarged.
func enlarge(img *image.RGBA, factor int) *image.RGBA {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if factor <= 1 || w == 0 || h == 0 {
		return img
	}
	out := image.NewRGBA(image.Rect(0, 0, w*factor, h*factor))
	for y := 0; y < h*factor; y++ {
		// Sample at pixel centres, in 8.8 fixed point.
		sy := max(0, (y*2+1)*256/(factor*2)-128)
		y0, fy := sy>>8, sy&255
		y1 := min(y0+1, h-1)
		top, bottom := img.Pix[y0*img.Stride:], img.Pix[y1*img.Stride:]
		dst := out.Pix[y*out.Stride:]
		for x := 0; x < w*factor; x++ {
			sx := max(0, (x*2+1)*256/(factor*2)-128)
			x0, fx := sx>>8, sx&255
			x1 := min(x0+1, w-1)
			for c := 0; c < 3; c++ {
				a := int(top[x0*4+c])*(256-fx) + int(top[x1*4+c])*fx
				b := int(bottom[x0*4+c])*(256-fx) + int(bottom[x1*4+c])*fx
				dst[x*4+c] = uint8((a*(256-fy) + b*fy + 1<<15) >> 16)
			}
			dst[x*4+3] = 255
		}
	}
	return out
}
