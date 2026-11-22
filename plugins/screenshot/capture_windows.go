//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func captureScreen(bounds image.Rectangle) (*image.RGBA, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if bounds.Empty() {
		return nil, errors.New("显示器区域为空")
	}
	dc := call(user32, "GetDC", 0)
	if dc == 0 {
		return nil, errors.New("无法读取桌面")
	}
	defer call(user32, "ReleaseDC", 0, dc)
	mem := call(gdi32, "CreateCompatibleDC", dc)
	defer call(gdi32, "DeleteDC", mem)
	header := bitmapHeader{Size: 40, Width: int32(bounds.Dx()), Height: -int32(bounds.Dy()), Planes: 1, Bits: 32}
	var address uintptr
	bmp := call(gdi32, "CreateDIBSection", dc, ptr(&header), 0, ptr(&address), 0, 0)
	if bmp == 0 {
		return nil, errors.New("无法分配桌面快照")
	}
	defer call(gdi32, "DeleteObject", bmp)
	old := call(gdi32, "SelectObject", mem, bmp)
	defer call(gdi32, "SelectObject", mem, old)
	// CAPTUREBLT includes existing layered shadow windows in the composite.
	if call(gdi32, "BitBlt", mem, 0, 0, bounds.Dx(), bounds.Dy(), dc, coord(bounds.Min.X), coord(bounds.Min.Y), srccopy|0x40000000) == 0 {
		return nil, errors.New("桌面捕获失败；受保护内容可能无法截图")
	}
	call(gdi32, "GdiFlush")
	img := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), address, &img.Pix[0], uintptr(len(img.Pix)), nil); err != nil {
		return nil, err
	}
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+2], img.Pix[i+3] = img.Pix[i+2], img.Pix[i], 255
	}
	return img, nil
}

func encodePNG(img *image.RGBA) ([]byte, error) {
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	return buf.Bytes(), err
}

func copyImage(owner uintptr, img *image.RGBA) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	encoded, err := encodePNG(img)
	if err != nil {
		return err
	}
	format := call(user32, "RegisterClipboardFormatW", ptr(u16("PNG")))
	if format == 0 {
		return errors.New("无法注册 PNG 剪贴板格式")
	}
	// Allocate both formats before taking ownership of the user's clipboard.
	pngHandle, err := clipboardMemory(encoded)
	if err != nil {
		return err
	}
	defer func() {
		if pngHandle != 0 {
			call(kernel32, "GlobalFree", pngHandle)
		}
	}()
	dibHandle, err := clipboardMemory(makeDIB(img))
	if err != nil {
		return err
	}
	defer func() {
		if dibHandle != 0 {
			call(kernel32, "GlobalFree", dibHandle)
		}
	}()
	if !openClipboard(owner) {
		return errors.New("剪贴板正被其他应用占用，请重试复制或保存图片")
	}
	defer call(user32, "CloseClipboard")
	if call(user32, "EmptyClipboard") == 0 {
		return errors.New("无法更新剪贴板")
	}
	if call(user32, "SetClipboardData", cfDIB, dibHandle) == 0 {
		return errors.New("写入剪贴板失败，请重试")
	}
	dibHandle = 0 // ownership transferred to Windows
	if call(user32, "SetClipboardData", format, pngHandle) != 0 {
		pngHandle = 0
	}
	return nil // CF_DIB is sufficient if PNG registration was accepted but data was not.
}

// FOLDERID_Pictures honors relocated and OneDrive-backed Pictures folders.
func screenshotDirectory() (string, error) {
	dir, err := knownFolder(windows.GUID{Data1: 0x33E28130, Data2: 0x4E1E, Data3: 0x4676, Data4: [8]byte{0x83, 0x5A, 0x98, 0x39, 0x5C, 0x3B, 0xC3, 0xBB}})
	if err != nil {
		return "", errors.New("无法定位系统图片文件夹")
	}
	return filepath.Join(dir, "FluentLauncher"), nil
}

func knownFolder(id windows.GUID) (string, error) {
	var address uintptr
	if int32(call(shell32, "SHGetKnownFolderPath", ptr(&id), 0, 0, ptr(&address))) < 0 {
		return "", errors.New("无法定位系统文件夹")
	}
	defer call(ole32, "CoTaskMemFree", address)
	// Read native memory without converting an integer into a Go pointer.
	buf := make([]uint16, 32768)
	for i := range buf {
		if err := windows.ReadProcessMemory(windows.CurrentProcess(), address+uintptr(i*2), (*byte)(unsafe.Pointer(&buf[i])), 2, nil); err != nil {
			return "", err
		}
		if buf[i] == 0 {
			return windows.UTF16ToString(buf[:i]), nil
		}
	}
	return "", errors.New("系统文件夹路径过长")
}

func screenshotName(now time.Time) string {
	return "Screenshot_" + now.Format("20060102_150405.000000000") + ".png"
}

func savePNG(path string, img *image.RGBA, exclusive bool) error {
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if exclusive {
		flag = os.O_CREATE | os.O_WRONLY | os.O_EXCL
	}
	f, err := os.OpenFile(path, flag, 0600)
	if err != nil {
		return err
	}
	encodeErr := png.Encode(f, img)
	closeErr := f.Close()
	return errors.Join(encodeErr, closeErr)
}

type openFilename struct {
	Size                   uint32
	Owner, Instance        uintptr
	Filter, CustomFilter   *uint16
	MaxCustom, FilterIndex uint32
	File                   *uint16
	MaxFile                uint32
	FileTitle              *uint16
	MaxTitle               uint32
	InitialDir, Title      *uint16
	Flags                  uint32
	FileOffset, Extension  uint16
	DefaultExt             *uint16
	Custom, Hook           uintptr
	Template               *uint16
	Reserved               uintptr
	ReservedDWORD, FlagsEx uint32
}

func chooseSavePath(owner uintptr) (string, error) {
	dir, err := screenshotDirectory()
	if err != nil {
		return "", err
	}
	return chooseSave(owner, "保存截图", screenshotName(time.Now()), dir, "PNG", "png")
}

// chooseSave returns "" when the user cancels the dialog.
func chooseSave(owner uintptr, title, name, dir, kind, extension string) (string, error) {
	file := make([]uint16, 32768)
	copy(file, windows.StringToUTF16(name))
	filter := append(windows.StringToUTF16(kind), windows.StringToUTF16("*."+extension)...)
	filter = append(filter, 0)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	opts := openFilename{Size: uint32(unsafe.Sizeof(openFilename{})), Owner: owner,
		Filter: &filter[0], FilterIndex: 1, File: &file[0], MaxFile: uint32(len(file)), InitialDir: u16(dir),
		Title: u16(title), DefaultExt: u16(extension), Flags: 0x00000002 | 0x00000800 | 0x00080000 | 0x00000008} // overwrite, path exists, explorer, no chdir
	if call(comdlg32, "GetSaveFileNameW", ptr(&opts)) == 0 {
		if code := call(comdlg32, "CommDlgExtendedError"); code != 0 {
			return "", fmt.Errorf("保存对话框失败：0x%x", code)
		}
		return "", nil
	}
	return windows.UTF16ToString(file), nil
}

type preferences struct {
	IncludePins  bool              `json:"includePins"`
	NewPinShadow bool              `json:"newPinShadow"`
	SnapWindows  bool              `json:"snapWindows"`
	SnapElements bool              `json:"snapElements"`
	Recording    recordPreferences `json:"recording"`
}

func preferencesPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "FluentLauncher", "plugin-data", "official.screenshot", "settings.json")
}
func loadPreferences() preferences {
	// Fields missing from an older file keep their defaults.
	p := preferences{IncludePins: true, NewPinShadow: true, SnapWindows: true, SnapElements: true, Recording: defaultRecordPreferences()}
	if data, err := os.ReadFile(preferencesPath()); err == nil {
		_ = json.Unmarshal(data, &p)
	}
	p.Recording = p.Recording.normalized()
	return p
}
func savePreferences(p preferences) error {
	path := preferencesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
