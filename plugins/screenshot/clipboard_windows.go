//go:build windows

package main

import (
	"errors"
	"image"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cfDIB         = 8
	cfUnicodeText = 13
	cfHDrop       = 15
)

// clipboardMemory copies data into a movable global block, the form the
// clipboard takes ownership of. The caller frees it unless SetClipboardData succeeds.
func clipboardMemory(data []byte) (uintptr, error) {
	h := call(kernel32, "GlobalAlloc", 2, len(data))
	if h == 0 {
		return 0, errors.New("无法分配剪贴板内存")
	}
	address := call(kernel32, "GlobalLock", h)
	if address == 0 {
		call(kernel32, "GlobalFree", h)
		return 0, errors.New("无法锁定剪贴板内存")
	}
	err := windows.WriteProcessMemory(windows.CurrentProcess(), address, &data[0], uintptr(len(data)), nil)
	call(kernel32, "GlobalUnlock", h)
	if err != nil {
		call(kernel32, "GlobalFree", h)
		return 0, err
	}
	return h, nil
}

// openClipboard waits briefly for another application to let go of it.
func openClipboard(owner uintptr) bool {
	for i := 0; i < 5; i++ {
		if call(user32, "OpenClipboard", owner) != 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func copyText(owner uintptr, text string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	wide, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(&wide[0])), len(wide)*2)
	handle, err := clipboardMemory(data)
	if err != nil {
		return err
	}
	if !openClipboard(owner) {
		call(kernel32, "GlobalFree", handle)
		return errors.New("剪贴板正被其他应用占用，请重试")
	}
	defer call(user32, "CloseClipboard")
	if call(user32, "EmptyClipboard") == 0 || call(user32, "SetClipboardData", cfUnicodeText, handle) == 0 {
		call(kernel32, "GlobalFree", handle)
		return errors.New("写入剪贴板失败，请重试")
	}
	return nil
}

// clipboardBytes copies one format out of the open clipboard; nil if absent.
func clipboardBytes(format uintptr) []byte {
	if call(user32, "IsClipboardFormatAvailable", format) == 0 {
		return nil
	}
	handle := call(user32, "GetClipboardData", format)
	if handle == 0 {
		return nil
	}
	size := call(kernel32, "GlobalSize", handle)
	address := call(kernel32, "GlobalLock", handle)
	if address == 0 || size == 0 {
		return nil
	}
	defer call(kernel32, "GlobalUnlock", handle)
	data := make([]byte, size)
	if windows.ReadProcessMemory(windows.CurrentProcess(), address, &data[0], size, nil) != nil {
		return nil
	}
	return data
}

// readClipboardImage returns the picture on the clipboard without changing
// it. PNG keeps what the source application meant; CF_DIB is what Windows
// synthesizes for every other bitmap.
func readClipboardImage(owner uintptr) (*image.RGBA, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if !openClipboard(owner) {
		return nil, errors.New("剪贴板正被其他应用占用，请重试")
	}
	encoded := clipboardBytes(call(user32, "RegisterClipboardFormatW", ptr(u16("PNG"))))
	var dib []byte
	if encoded == nil {
		dib = clipboardBytes(cfDIB)
	}
	files := call(user32, "IsClipboardFormatAvailable", cfHDrop) != 0
	call(user32, "CloseClipboard") // decode after releasing it for other applications
	if encoded != nil {
		if img, err := parsePNG(encoded); err == nil {
			return img, nil
		}
		// A producer with a broken PNG usually still offers a bitmap.
		if !openClipboard(owner) {
			return nil, errPastedFormat
		}
		dib = clipboardBytes(cfDIB)
		call(user32, "CloseClipboard")
	}
	switch {
	case dib != nil:
		return parseDIB(dib)
	case files:
		return nil, errors.New("剪贴板里是文件而不是图片；请在看图软件里打开后复制图片本身")
	}
	return nil, errors.New("剪贴板里没有图片")
}
