//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

// copyFileDrop puts the file itself (CF_HDROP) on the clipboard, the form
// chat apps and Explorer accept for animated images.
func copyFileDrop(owner uintptr, path string) error {
	name, err := windows.UTF16FromString(path)
	if err != nil {
		return err
	}
	data := make([]byte, 20+len(name)*2+2) // DROPFILES, path, list terminator
	data[0], data[16] = 20, 1              // pFiles offset, fWide
	for i, v := range name {
		data[20+i*2], data[21+i*2] = byte(v), byte(v>>8)
	}
	handle := call(kernel32, "GlobalAlloc", 2, len(data))
	if handle == 0 {
		return errors.New("无法分配剪贴板内存")
	}
	address := call(kernel32, "GlobalLock", handle)
	if address != 0 {
		err = windows.WriteProcessMemory(windows.CurrentProcess(), address, &data[0], uintptr(len(data)), nil)
		call(kernel32, "GlobalUnlock", handle)
	}
	if address == 0 || err != nil {
		call(kernel32, "GlobalFree", handle)
		return errors.Join(errors.New("无法写入剪贴板内存"), err)
	}
	if call(user32, "OpenClipboard", owner) == 0 {
		call(kernel32, "GlobalFree", handle)
		return errors.New("剪贴板正被其他应用占用，请重试")
	}
	defer call(user32, "CloseClipboard")
	call(user32, "EmptyClipboard")
	if call(user32, "SetClipboardData", 15, handle) == 0 {
		call(kernel32, "GlobalFree", handle)
		return errors.New("写入剪贴板失败，请重试")
	}
	return nil
}

// FOLDERID_Videos honors relocated and OneDrive-backed Videos folders.
func recordingDirectory() (string, error) {
	dir, err := knownFolder(windows.GUID{Data1: 0x18989B1D, Data2: 0x99B5, Data3: 0x455B, Data4: [8]byte{0x84, 0x1C, 0xAB, 0x7C, 0x74, 0xE4, 0xDD, 0xFC}})
	if err != nil {
		return "", errors.New("无法定位系统视频文件夹")
	}
	return filepath.Join(dir, "FluentLauncher"), nil
}
