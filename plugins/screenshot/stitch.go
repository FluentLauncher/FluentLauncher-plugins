package main

import (
	"bytes"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"sort"
)

var (
	errNoOverlap = errors.New("未识别出可靠的向下移动；若已到末尾可完成，否则减小滚动幅度后重试")
	errAmbiguous = errors.New("画面重复，无法确定滚动距离；请调整选区或滚动幅度后重试")
	errLongLimit = errors.New("已达到长截图大小上限，请完成并保存当前结果")
)

const maxLongPixels = 32_000_000
const maxLongHeight = 30_000

type imagePart struct {
	img        *image.RGBA
	start, end int
}

// Parts retain captured frames without copying the whole growing image on
// each step. A seam replaces the old viewport tail: fixed headers stay once
// at the beginning, and only the final viewport's footer survives.
type stitcher struct {
	parts                  []imagePart
	last                   *image.RGBA
	origin, height, frames int
}

func newStitcher(first *image.RGBA) *stitcher {
	return &stitcher{parts: []imagePart{{first, 0, first.Rect.Dy()}}, last: first, height: first.Rect.Dy(), frames: 1}
}

func (s *stitcher) add(next *image.RGBA) (bool, error) {
	if next.Bounds().Size() != s.last.Bounds().Size() {
		return false, errors.New("采集区域尺寸发生变化，请重新开始")
	}
	if bytes.Equal(s.last.Pix, next.Pix) {
		return false, nil
	}
	match, err := findOverlap(s.last, next)
	if err != nil {
		return false, err
	}
	newHeight := s.height + match.shift
	if newHeight > maxLongHeight || newHeight > maxLongPixels/next.Rect.Dx() {
		return false, errLongLimit
	}
	seam := match.seam
	cut := s.origin + match.shift + seam
	remaining := cut
	kept := make([]imagePart, 0, len(s.parts)+1)
	for _, part := range s.parts {
		if remaining <= 0 {
			break
		}
		h := min(part.end-part.start, remaining)
		part.end = part.start + h
		kept = append(kept, part)
		remaining -= h
	}
	s.parts = append(kept, imagePart{next, seam, next.Rect.Dy()})
	s.last = next
	s.origin += match.shift
	s.height = newHeight
	s.frames++
	return true, nil
}

func (s *stitcher) result() *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, s.last.Rect.Dx(), s.height))
	y := 0
	for _, part := range s.parts {
		h := part.end - part.start
		draw.Draw(out, image.Rect(0, y, out.Rect.Dx(), y+h), part.img, part.img.Rect.Min.Add(image.Pt(0, part.start)), draw.Src)
		y += h
	}
	return out
}

// Sample the retained segments directly: the live preview never allocates a
// growing full-resolution result for every incoming frame.
func (s *stitcher) thumbnail(maxWidth, maxHeight int) *image.RGBA {
	w, h := s.last.Rect.Dx(), s.height
	factor := min(1, float64(maxWidth)/float64(w), float64(maxHeight)/float64(h))
	out := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*factor)), max(1, int(float64(h)*factor))))
	partIndex, start := 0, 0
	rowAt := func(y int) (*image.RGBA, int) {
		for partIndex < len(s.parts)-1 && y >= start+s.parts[partIndex].end-s.parts[partIndex].start {
			start += s.parts[partIndex].end - s.parts[partIndex].start
			partIndex++
		}
		part := s.parts[partIndex]
		return part.img, part.img.Rect.Min.Y + part.start + y - start
	}
	for y := 0; y < out.Rect.Dy(); y++ {
		fy := max(0, min(float64(h-1), (float64(y)+0.5)*float64(h)/float64(out.Rect.Dy())-0.5))
		y0 := int(fy)
		ty := fy - float64(y0)
		a, ay := rowAt(y0)
		b, by := rowAt(min(y0+1, h-1))
		for x := 0; x < out.Rect.Dx(); x++ {
			fx := max(0, min(float64(w-1), (float64(x)+0.5)*float64(w)/float64(out.Rect.Dx())-0.5))
			x0 := int(fx)
			x1 := min(x0+1, w-1)
			tx := fx - float64(x0)
			c00, c10 := a.RGBAAt(a.Rect.Min.X+x0, ay), a.RGBAAt(a.Rect.Min.X+x1, ay)
			c01, c11 := b.RGBAAt(b.Rect.Min.X+x0, by), b.RGBAAt(b.Rect.Min.X+x1, by)
			blend := func(v00, v10, v01, v11 uint8) uint8 {
				return uint8(((float64(v00)*(1-tx)+float64(v10)*tx)*(1-ty) + (float64(v01)*(1-tx)+float64(v11)*tx)*ty) + 0.5)
			}
			out.SetRGBA(x, y, color.RGBA{blend(c00.R, c10.R, c01.R, c11.R), blend(c00.G, c10.G, c01.G, c11.G), blend(c00.B, c10.B, c01.B, c11.B), 255})
		}
	}
	return out
}

type overlap struct{ shift, start, end, hits, seam int }

func rowSignature(img *image.RGBA, y int) (uint32, bool) {
	var samples [32 * 3]byte
	lo, hi := 255, 0
	for i := 0; i < 32; i++ {
		x := img.Rect.Min.X + (i+1)*img.Rect.Dx()/33
		c := img.RGBAAt(x, img.Rect.Min.Y+y)
		samples[i*3], samples[i*3+1], samples[i*3+2] = c.R>>3, c.G>>3, c.B>>3
		l := (int(c.R) + int(c.G) + int(c.B)) / 3
		lo = min(lo, l)
		hi = max(hi, l)
	}
	return crc32.ChecksumIEEE(samples[:]), hi-lo >= 16
}

func rowDistance(a, b *image.RGBA, ay, by int) (float64, bool) {
	sum, lo, hi := 0, 255, 0
	for i := 0; i < 32; i++ {
		x := (i + 1) * a.Rect.Dx() / 33
		ac, bc := a.RGBAAt(a.Rect.Min.X+x, a.Rect.Min.Y+ay), b.RGBAAt(b.Rect.Min.X+x, b.Rect.Min.Y+by)
		sum += abs(int(ac.R)-int(bc.R)) + abs(int(ac.G)-int(bc.G)) + abs(int(ac.B)-int(bc.B))
		l := (int(ac.R) + int(ac.G) + int(ac.B)) / 3
		lo = min(lo, l)
		hi = max(hi, l)
	}
	return float64(sum) / 96, hi-lo >= 16
}

func frameDifference(a, b *image.RGBA) float64 {
	if a.Bounds().Size() != b.Bounds().Size() {
		return 255
	}
	sum := 0.0
	for i := 0; i < 32; i++ {
		v, _ := rowDistance(a, b, (i+1)*a.Rect.Dy()/33, (i+1)*a.Rect.Dy()/33)
		sum += v
	}
	return sum / 32
}

func evaluateShift(a, b *image.RGBA, shift int) overlap {
	start, hits, bad := 0, 0, 0
	best := overlap{shift: shift}
	for y := 0; y < a.Rect.Dy()-shift; y++ {
		err, textured := rowDistance(a, b, y+shift, y)
		if err <= 4 {
			bad = 0
			if textured {
				hits++
			}
			if y-start > best.end-best.start && hits >= 4 {
				best.start, best.end, best.hits = start, y+1, hits
			}
		} else {
			bad++
			if bad > 3 {
				start = y + 1
				hits = 0
			}
		}
	}
	return best
}

func reliableSeam(a, b *image.RGBA, m *overlap) bool {
	middle := (m.start + m.end) / 2
	for step := 0; step < 40; step++ {
		delta := (step + 1) / 2
		if step%2 != 0 {
			delta = -delta
		}
		y := middle + delta
		if y < m.start || y >= m.end {
			continue
		}
		sum, bad := 0, 0
		for x := 0; x < a.Rect.Dx(); x++ {
			ac, bc := a.RGBAAt(a.Rect.Min.X+x, a.Rect.Min.Y+y+m.shift), b.RGBAAt(b.Rect.Min.X+x, b.Rect.Min.Y+y)
			d := abs(int(ac.R)-int(bc.R)) + abs(int(ac.G)-int(bc.G)) + abs(int(ac.B)-int(bc.B))
			sum += d
			if d > 36 {
				bad++
			}
		}
		if sum <= a.Rect.Dx()*12 && bad <= max(1, a.Rect.Dx()/30) {
			m.seam = y
			return true
		}
	}
	return false
}

func findOverlap(a, b *image.RGBA) (overlap, error) {
	h := a.Rect.Dy()
	minimum := max(24, h/12)
	if h < 48 {
		return overlap{}, errors.New("长截图选区过矮，请至少选择 48 像素高的内容区域")
	}
	index := make(map[uint32][]int)
	for y := 0; y < h; y++ {
		if key, ok := rowSignature(b, y); ok {
			index[key] = append(index[key], y)
		}
	}
	votes := make(map[int]int)
	for y := 0; y < h; y++ {
		key, ok := rowSignature(a, y)
		if !ok {
			continue
		}
		positions := index[key]
		if len(positions) > 12 {
			continue
		}
		for _, other := range positions {
			d := y - other
			if d > 0 && d <= h-minimum {
				votes[d]++
			}
		}
	}
	type candidate struct {
		shift, votes int
		error        float64
	}
	var candidates []candidate
	for shift, n := range votes {
		if n >= 3 {
			candidates = append(candidates, candidate{shift: shift, votes: n})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].votes > candidates[j].votes })
	if len(candidates) > 12 {
		candidates = candidates[:12]
	}
	if len(candidates) == 0 {
		// Tolerate small rendering changes that invalidate exact row hashes.
		for d := 1; d <= h-minimum; d++ {
			err := 0.0
			for i := 0; i < 20; i++ {
				y := (i + 1) * (h - d) / 21
				v, _ := rowDistance(a, b, y+d, y)
				err += v
			}
			candidates = append(candidates, candidate{shift: d, error: err})
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].error < candidates[j].error })
		if len(candidates) > 12 {
			candidates = candidates[:12]
		}
	}
	var matches []overlap
	for _, candidate := range candidates {
		m := evaluateShift(a, b, candidate.shift)
		if m.end-m.start >= minimum && m.hits >= 4 && reliableSeam(a, b, &m) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return overlap{}, errNoOverlap
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].end-matches[i].start > matches[j].end-matches[j].start })
	best := matches[0]
	for _, other := range matches[1:] {
		if abs(other.shift-best.shift) > 2 && (other.end-other.start)*100 >= (best.end-best.start)*85 && other.hits*100 >= best.hits*80 {
			return overlap{}, errAmbiguous
		}
	}
	return best, nil
}
