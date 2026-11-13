//go:build windows

package main

import (
	"image"
	"unsafe"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
	"golang.org/x/sys/windows"
)

func toolbarHintBounds(button image.Rectangle, textSize image.Point, bounds image.Rectangle, scale float64) image.Rectangle {
	size := image.Pt(min(bounds.Dx(), textSize.X+px(14, scale)), max(px(20, scale), textSize.Y+px(6, scale)))
	x := (button.Min.X + button.Max.X - size.X) / 2
	x = max(bounds.Min.X, min(x, bounds.Max.X-size.X))
	y := button.Min.Y - px(6, scale) - size.Y
	if y < bounds.Min.Y {
		y = button.Max.Y + px(6, scale)
	}
	y = max(bounds.Min.Y, min(y, bounds.Max.Y-size.Y))
	return image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+size.X, y+size.Y)}
}

// Measure and draw with the same font and explicit window DPI, including when
// the target is a memory DC with no WindowFromDC association.
func paintToolbarHint(dc uintptr, value string, button, bounds image.Rectangle, scale float64, theme pluginsdk.Theme) image.Rectangle {
	metrics := nonclientMetrics{Size: uint32(unsafe.Sizeof(nonclientMetrics{}))}
	call(user32, "SystemParametersInfoForDpi", 0x29, metrics.Size, ptr(&metrics), 0, px(96, scale))
	metrics.MessageFont.Height = -int32(px(11, scale))
	font := call(gdi32, "CreateFontIndirectW", ptr(&metrics.MessageFont))
	selectedFont := font
	if selectedFont == 0 {
		selectedFont = call(gdi32, "GetStockObject", 17)
	}
	old := call(gdi32, "SelectObject", dc, selectedFont)
	defer func() {
		call(gdi32, "SelectObject", dc, old)
		if font != 0 {
			call(gdi32, "DeleteObject", font)
		}
	}()
	valueUTF16, _ := windows.UTF16FromString(value)
	var extent point
	call(gdi32, "GetTextExtentPoint32W", dc, ptr(&valueUTF16[0]), len(valueUTF16)-1, ptr(&extent))
	r := toolbarHintBounds(button, image.Pt(int(extent.X), int(extent.Y)), bounds, scale)
	roundPanel(dc, r, px(4, scale), theme.Inverse, theme.Inverse)
	call(gdi32, "SetBkMode", dc, 1)
	call(gdi32, "SetTextColor", dc, rgbRef(theme.InverseText))
	wr := winRect(r)
	call(user32, "DrawTextW", dc, ptr(&valueUTF16[0]), len(valueUTF16)-1, ptr(&wr), 0x20|0x04|0x01|0x8000)
	return r
}
