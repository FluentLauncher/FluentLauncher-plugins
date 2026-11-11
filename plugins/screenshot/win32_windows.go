//go:build windows

package main

import (
	"fmt"
	"image"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")
)

// Keep pointer arguments visible to the GC until the native call returns.
func call(dll *windows.LazyDLL, name string, args ...any) uintptr {
	return callAddress(dll.NewProc(name).Addr(), args...)
}
func callAddress(address uintptr, args ...any) uintptr {
	native := make([]uintptr, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case uintptr:
			native[i] = v
		case int:
			native[i] = uintptr(v)
		case uint32:
			native[i] = uintptr(v)
		case unsafe.Pointer:
			native[i] = uintptr(v)
		default:
			panic(fmt.Sprintf("unsupported Win32 argument %T", arg))
		}
	}
	r, _, _ := syscall.SyscallN(address, native...)
	runtime.KeepAlive(args)
	return r
}
func u16(s string) *uint16           { return windows.StringToUTF16Ptr(s) }
func ptr[T any](p *T) unsafe.Pointer { return unsafe.Pointer(p) }
func coord(n int) uintptr            { return uintptr(int64(n)) }

const (
	wmDestroy       = 0x0002
	wmSize          = 0x0005
	wmSetFocus      = 0x0007
	wmKillFocus     = 0x0008
	wmPaint         = 0x000F
	wmClose         = 0x0010
	wmKeyDown       = 0x0100
	wmCommand       = 0x0111
	wmMouseMove     = 0x0200
	wmLButtonDown   = 0x0201
	wmLButtonUp     = 0x0202
	wmLButtonDbl    = 0x0203
	wmRButtonUp     = 0x0205
	wmMouseWheel    = 0x020A
	wmContextMenu   = 0x007B
	wmApp           = 0x8001
	wsPopup         = 0x80000000
	wsExTopmost     = 0x00000008
	wsExToolwindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExNoactivate  = 0x08000000
	swpNoactivate   = 0x0010
	swpShowwindow   = 0x0040
	hwndTopmost     = ^uintptr(0)
	srccopy         = 0x00CC0020
)

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

func winRect(r image.Rectangle) rect {
	return rect{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
}
func (r rect) image() image.Rectangle {
	return image.Rect(int(r.Left), int(r.Top), int(r.Right), int(r.Bottom))
}

type message struct {
	Window         uintptr
	ID             uint32
	WParam, LParam uintptr
	Time           uint32
	Point          point
	Private        uint32
}
type wndClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type paintStruct struct {
	DC                 uintptr
	Erase              int32
	Rect               rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type bitmapHeader struct {
	Size                   uint32
	Width, Height          int32
	Planes, Bits           uint16
	Compression, ImageSize uint32
	XPels, YPels           int32
	Used, Important        uint32
}
type monitorInfo struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
}

type logFont struct {
	Height, Width, Escapement, Orientation, Weight                                     int32
	Italic, Underline, Strikeout, Charset, OutPrecision, ClipPrecision, Quality, Pitch byte
	Face                                                                               [32]uint16
}
type nonclientMetrics struct {
	Size                                                           uint32
	Border, ScrollWidth, ScrollHeight, CaptionWidth, CaptionHeight int32
	CaptionFont                                                    logFont
	SmallWidth, SmallHeight                                        int32
	SmallFont                                                      logFont
	MenuWidth, MenuHeight                                          int32
	MenuFont, StatusFont, MessageFont                              logFont
	PaddedBorder                                                   int32
}

func mousePoint(lparam uintptr) image.Point {
	return image.Pt(int(int16(lparam&0xffff)), int(int16((lparam>>16)&0xffff)))
}
func cursorPoint() image.Point {
	var p point
	call(user32, "GetCursorPos", ptr(&p))
	return image.Pt(int(p.X), int(p.Y))
}
func shiftDown() bool { return call(user32, "GetKeyState", 0x10)&0x8000 != 0 }
func ctrlDown() bool  { return call(user32, "GetKeyState", 0x11)&0x8000 != 0 }
func virtualBounds() image.Rectangle {
	metric := func(i uintptr) int { return int(int32(call(user32, "GetSystemMetrics", i))) }
	x, y := metric(76), metric(77)
	return image.Rect(x, y, x+metric(78), y+metric(79))
}
func currentMonitor(p image.Point) image.Rectangle {
	// POINT is passed by value in the Windows x64 ABI.
	packed := uintptr(uint64(uint32(p.X)) | uint64(uint32(p.Y))<<32)
	m := call(user32, "MonitorFromPoint", packed, 2)
	info := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if call(user32, "GetMonitorInfoW", m, ptr(&info)) == 0 {
		return virtualBounds()
	}
	return info.Monitor.image()
}
func windowAt(hwnd uintptr, bounds image.Rectangle, activate bool) {
	windowAtLevel(hwnd, bounds, activate, true)
}
func windowAtLevel(hwnd uintptr, bounds image.Rectangle, activate, topmost bool) {
	flags := uintptr(swpShowwindow | swpNoactivate)
	if activate {
		flags &^= swpNoactivate
	}
	level := hwndTopmost
	if !topmost {
		level = ^uintptr(1)
	} // HWND_NOTOPMOST
	call(user32, "SetWindowPos", hwnd, level, coord(bounds.Min.X), coord(bounds.Min.Y), uintptr(bounds.Dx()), uintptr(bounds.Dy()), flags)
}
func invalidate(hwnd uintptr)           { call(user32, "InvalidateRect", hwnd, 0, 0) }
func systemColor(index uintptr) uintptr { return call(user32, "GetSysColor", index) }

func fill(dc uintptr, r image.Rectangle, color uintptr) {
	if r.Empty() {
		return
	}
	brush := call(gdi32, "CreateSolidBrush", color)
	defer call(gdi32, "DeleteObject", brush)
	wr := winRect(r)
	call(user32, "FillRect", dc, ptr(&wr), brush)
}
func text(dc uintptr, value string, r image.Rectangle, color uintptr) {
	drawText(dc, value, r, color, 1)
}

func textAligned(dc uintptr, value string, r image.Rectangle, color uintptr, right bool) {
	flags := uintptr(0)
	if right {
		flags = 2
	}
	drawText(dc, value, r, color, flags)
}

func drawText(dc uintptr, value string, r image.Rectangle, color uintptr, alignment uintptr) {
	call(gdi32, "SetBkMode", dc, 1)
	call(gdi32, "SetTextColor", dc, color)
	font := uintptr(0)
	dpi := call(user32, "GetDpiForWindow", call(user32, "WindowFromDC", dc))
	if dpi == 0 {
		dpi = call(user32, "GetDpiForSystem")
	}
	metrics := nonclientMetrics{Size: uint32(unsafe.Sizeof(nonclientMetrics{}))}
	if call(user32, "SystemParametersInfoForDpi", 0x29, metrics.Size, ptr(&metrics), 0, max(dpi, 96)) != 0 {
		font = call(gdi32, "CreateFontIndirectW", ptr(&metrics.MessageFont))
	}
	selectedFont := font
	if selectedFont == 0 {
		selectedFont = call(gdi32, "GetStockObject", 17)
	}
	old := call(gdi32, "SelectObject", dc, selectedFont)
	defer call(gdi32, "SelectObject", dc, old)
	defer func() {
		if font != 0 {
			call(gdi32, "SelectObject", dc, old)
			call(gdi32, "DeleteObject", font)
		}
	}()
	wr := winRect(r)
	call(user32, "DrawTextW", dc, ptr(u16(value)), ^uintptr(0), ptr(&wr), 0x20|0x04|alignment|0x8000)
}

type bitmap struct {
	handle        uintptr
	width, height int
	address       uintptr
}

func newBitmap(img *image.RGBA) (*bitmap, error) {
	header := bitmapHeader{Size: 40, Width: int32(img.Rect.Dx()), Height: -int32(img.Rect.Dy()), Planes: 1, Bits: 32}
	var address uintptr
	h := call(gdi32, "CreateDIBSection", 0, ptr(&header), 0, ptr(&address), 0, 0)
	if h == 0 {
		return nil, fmt.Errorf("无法创建截图位图")
	}
	data := make([]byte, img.Rect.Dx()*img.Rect.Dy()*4)
	for y := 0; y < img.Rect.Dy(); y++ {
		for x := 0; x < img.Rect.Dx(); x++ {
			c := img.RGBAAt(img.Rect.Min.X+x, img.Rect.Min.Y+y)
			i := (y*img.Rect.Dx() + x) * 4
			data[i], data[i+1], data[i+2], data[i+3] = c.B, c.G, c.R, c.A
		}
	}
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), address, &data[0], uintptr(len(data)), nil); err != nil {
		call(gdi32, "DeleteObject", h)
		return nil, err
	}
	return &bitmap{handle: h, width: img.Rect.Dx(), height: img.Rect.Dy(), address: address}, nil
}

func (b *bitmap) pixels() (*image.RGBA, error) {
	call(gdi32, "GdiFlush")
	img := image.NewRGBA(image.Rect(0, 0, b.width, b.height))
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), b.address, &img.Pix[0], uintptr(len(img.Pix)), nil); err != nil {
		return nil, err
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	return img, nil
}
func (b *bitmap) close() {
	if b != nil && b.handle != 0 {
		call(gdi32, "DeleteObject", b.handle)
		b.handle = 0
	}
}
func (b *bitmap) draw(dc uintptr, dest, src image.Rectangle) {
	b.drawMode(dc, dest, src, 4)
}

func (b *bitmap) drawMode(dc uintptr, dest, src image.Rectangle, mode int) {
	mem := call(gdi32, "CreateCompatibleDC", dc)
	old := call(gdi32, "SelectObject", mem, b.handle)
	defer func() { call(gdi32, "SelectObject", mem, old); call(gdi32, "DeleteDC", mem) }()
	call(gdi32, "SetStretchBltMode", dc, mode)
	call(gdi32, "SetBrushOrgEx", dc, 0, 0, 0)
	call(gdi32, "StretchBlt", dc, coord(dest.Min.X), coord(dest.Min.Y), uintptr(dest.Dx()), uintptr(dest.Dy()), mem,
		coord(src.Min.X), coord(src.Min.Y), uintptr(src.Dx()), uintptr(src.Dy()), srccopy)
}
