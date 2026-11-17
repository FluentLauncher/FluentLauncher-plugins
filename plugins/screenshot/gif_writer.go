package main

import (
	"bufio"
	"bytes"
	"compress/lzw"
	"errors"
	"io"
	"time"
)

// A streaming GIF89a encoder for screen recordings. Memory stays bounded by
// two index frames plus one compressed frame, independent of the duration.
//
// The palette is fixed (6×7×6 levels plus one transparent index) and frames
// use ordered dithering: the pattern depends only on the pixel position, so
// unchanged screen content quantizes identically and delta frames stay small.

const gifTransparent = 255

var (
	gifPalette [256 * 3]byte
	gifLUT     [3][16][256]uint8 // channel (R, G, B) → Bayer cell → value → weighted level
)

func init() {
	bayer := [16]int{0, 8, 2, 10, 12, 4, 14, 6, 3, 11, 1, 9, 15, 7, 13, 5}
	levels, weight := [3]int{6, 7, 6}, [3]int{42, 6, 1}
	for c := range 3 {
		for t := range 16 {
			threshold := (bayer[t]*255 + 127) / 16
			for v := range 256 {
				gifLUT[c][t][v] = uint8((v*(levels[c]-1) + threshold) / 255 * weight[c])
			}
		}
	}
	for i := range 252 {
		gifPalette[i*3], gifPalette[i*3+1], gifPalette[i*3+2] = uint8(i/42*51), uint8((i/6%7*255+3)/6), uint8(i%6*51)
	}
}

type gifWriter struct {
	w             *bufio.Writer
	width, height int
	prev, cur     []byte // palette indices of the last written and the incoming frame
	scratch       []byte
	pending       bytes.Buffer // descriptor + image data of the frame waiting for its delay
	pendingAt     time.Duration
	hasPending    bool
	frames        int
	carry         time.Duration // rounding error carried between centisecond delays
	written       int64
	err           error
}

func newGIFWriter(w io.Writer, width, height int) (*gifWriter, error) {
	if width <= 0 || height <= 0 || width > 0xffff || height > 0xffff {
		return nil, errors.New("GIF 尺寸超出范围")
	}
	g := &gifWriter{w: bufio.NewWriterSize(w, 64<<10), width: width, height: height,
		prev: make([]byte, width*height), cur: make([]byte, width*height)}
	g.write([]byte("GIF89a"))
	g.write([]byte{byte(width), byte(width >> 8), byte(height), byte(height >> 8), 0xF7, 0, 0})
	g.write(gifPalette[:])
	g.write([]byte{0x21, 0xFF, 11, 'N', 'E', 'T', 'S', 'C', 'A', 'P', 'E', '2', '.', '0', 3, 1, 0, 0, 0}) // loop forever
	return g, nil
}

// bufio keeps the first write error and returns it from every later call.
func (g *gifWriter) write(p []byte) error {
	n, err := g.w.Write(p)
	g.written += int64(n)
	return err
}

// size is the length of the file once everything encoded so far is written.
func (g *gifWriter) size() int64 {
	if g.hasPending {
		return g.written + 8 + int64(g.pending.Len())
	}
	return g.written
}

// addFrame takes top-down BGRA pixels captured at the given recording time.
// Frames identical to the previous one only extend its display time.
func (g *gifWriter) addFrame(bgra []byte, stride int, at time.Duration) error {
	if g.err != nil {
		return g.err
	}
	if stride < g.width*4 || len(bgra) < stride*(g.height-1)+g.width*4 {
		return errors.New("录制帧尺寸不匹配")
	}
	w, h := g.width, g.height
	for y := 0; y < h; y++ {
		row, out, cell := bgra[y*stride:], g.cur[y*w:(y+1)*w], (y&3)*4
		for x := range out {
			t, p := cell+x&3, row[x*4:x*4+3]
			out[x] = gifLUT[2][t][p[0]] + gifLUT[1][t][p[1]] + gifLUT[0][t][p[2]]
		}
	}
	left, top, right, bottom := 0, 0, w, h
	pixels := g.cur
	if g.frames > 0 {
		for top < h && bytes.Equal(g.cur[top*w:(top+1)*w], g.prev[top*w:(top+1)*w]) {
			top++
		}
		if top == h {
			return nil
		}
		for bytes.Equal(g.cur[(bottom-1)*w:bottom*w], g.prev[(bottom-1)*w:bottom*w]) {
			bottom--
		}
		left, right = w, 0
		for y := top; y < bottom; y++ {
			a, b := g.cur[y*w:(y+1)*w], g.prev[y*w:(y+1)*w]
			for x := 0; x < left; x++ {
				if a[x] != b[x] {
					left = x
				}
			}
			for x := w - 1; x >= right; x-- {
				if a[x] != b[x] {
					right = x + 1
				}
			}
		}
		// Unchanged pixels inside the box become transparent: long runs of one
		// index compress far better than repeating the previous content.
		g.scratch = g.scratch[:0]
		for y := top; y < bottom; y++ {
			a, b := g.cur[y*w+left:y*w+right], g.prev[y*w+left:y*w+right]
			for x, v := range a {
				if v == b[x] {
					v = gifTransparent
				}
				g.scratch = append(g.scratch, v)
			}
		}
		pixels = g.scratch
		if err := g.flush(at - g.pendingAt); err != nil {
			return err
		}
	}
	bw, bh := right-left, bottom-top
	g.pending.Reset()
	g.pending.Write([]byte{0x2C, byte(left), byte(left >> 8), byte(top), byte(top >> 8), byte(bw), byte(bw >> 8), byte(bh), byte(bh >> 8), 0, 8})
	blocks := &gifBlocks{w: &g.pending}
	lz := lzw.NewWriter(blocks, lzw.LSB, 8)
	if _, err := lz.Write(pixels); err != nil {
		return g.fail(err)
	}
	if err := lz.Close(); err != nil {
		return g.fail(err)
	}
	blocks.flush()
	g.pending.WriteByte(0)
	g.hasPending, g.pendingAt = true, at
	g.frames++
	g.prev, g.cur = g.cur, g.prev
	return nil
}

func (g *gifWriter) fail(err error) error {
	g.err = err
	return err
}

// flush writes the pending frame once its display time is known.
func (g *gifWriter) flush(delay time.Duration) error {
	if !g.hasPending {
		return nil
	}
	g.hasPending = false
	total := delay + g.carry
	cs := max(2, int64((total+5*time.Millisecond)/(10*time.Millisecond))) // players slow down delays under 20 ms
	cs = min(cs, 0xffff)
	g.carry = total - time.Duration(cs)*10*time.Millisecond
	flags := byte(0x04) // keep the frame in place
	if g.frames > 1 {
		flags |= 0x01 // transparent index is in use
	}
	g.write([]byte{0x21, 0xF9, 4, flags, byte(cs), byte(cs >> 8), gifTransparent, 0})
	if err := g.write(g.pending.Bytes()); err != nil {
		return g.fail(err)
	}
	return nil
}

// close writes the last frame, shown until end, and the trailer.
func (g *gifWriter) close(end time.Duration) error {
	if g.err != nil {
		return g.err
	}
	if g.frames == 0 {
		return g.fail(errors.New("没有录到任何画面"))
	}
	if err := g.flush(end - g.pendingAt); err != nil {
		return err
	}
	g.write([]byte{0x3B})
	if err := g.w.Flush(); err != nil {
		return g.fail(err)
	}
	g.err = errors.New("GIF 已结束")
	return nil
}

// gifBlocks splits the LZW stream into GIF data sub-blocks of at most 255 bytes.
type gifBlocks struct {
	w   *bytes.Buffer
	buf [255]byte
	n   int
}

func (b *gifBlocks) Write(p []byte) (int, error) {
	for _, v := range p {
		b.buf[b.n] = v
		b.n++
		if b.n == len(b.buf) {
			b.flush()
		}
	}
	return len(p), nil
}

func (b *gifBlocks) flush() {
	if b.n > 0 {
		b.w.WriteByte(byte(b.n))
		b.w.Write(b.buf[:b.n])
		b.n = 0
	}
}
