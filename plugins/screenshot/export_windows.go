//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Exports are user-requested materializations, not screenshot history. Keep
// them until shutdown because viewers and drop targets may open the path later.
func (p *pinWindow) exportFile(drag bool) {
	if p.busy {
		return
	}
	if p.d.tempDir == "" {
		dir, err := os.MkdirTemp("", "FluentLauncher-screenshot-")
		if err != nil {
			p.d.report(p.hwnd, err)
			return
		}
		p.d.tempDir = dir
	}
	path := filepath.Join(p.d.tempDir, screenshotName(time.Now()))
	img := p.rendered
	p.busy = true
	p.d.active.Add(1)
	go func() {
		err := savePNG(path, img, true)
		_ = p.d.post(func() {
			defer p.d.active.Add(-1)
			defer func() { p.busy = false }()
			if p.d.pins[p.hwnd] != p {
				return
			}
			if err != nil {
				p.d.report(p.hwnd, err)
				return
			}
			if drag {
				if call(user32, "GetKeyState", 1)&0x8000 == 0 {
					return
				}
				p.d.report(p.hwnd, dragFile(path))
			} else if call(shell32, "ShellExecuteW", p.hwnd, ptr(u16("open")), ptr(u16(path)), 0, 0, 1) <= 32 {
				p.d.report(p.hwnd, errors.New("无法使用默认应用打开图片，请使用另存为"))
			}
		})
	}()
}

var iidDataObject = windows.GUID{Data1: 0x10e, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var iidDropSource = windows.GUID{Data1: 0x121, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var iidUnknown = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

func nativeRead[T any](address uintptr, value *T) error {
	return windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(value)), unsafe.Sizeof(*value), nil)
}

func comRelease(object uintptr) {
	if object == 0 {
		return
	}
	var table, method uintptr
	if nativeRead(object, &table) == nil && nativeRead(table+2*unsafe.Sizeof(uintptr(0)), &method) == nil {
		callAddress(method, object)
	}
}

type dropSource struct{ Vtable *[5]uintptr }

var dropVtable = [5]uintptr{
	windows.NewCallback(func(this, iid, out uintptr) uintptr {
		if out == 0 {
			return 0x80004003
		}
		var id windows.GUID
		if nativeRead(iid, &id) != nil || (id != iidDropSource && id != iidUnknown) {
			return 0x80004002
		}
		if windows.WriteProcessMemory(windows.CurrentProcess(), out, (*byte)(unsafe.Pointer(&this)), unsafe.Sizeof(this), nil) != nil {
			return 0x80004003
		}
		return 0
	}),
	windows.NewCallback(func(uintptr) uintptr { return 2 }),
	windows.NewCallback(func(uintptr) uintptr { return 1 }),
	windows.NewCallback(func(this, escape, keys uintptr) uintptr {
		if escape != 0 {
			return 0x40101
		} // DRAGDROP_S_CANCEL
		if keys&1 == 0 {
			return 0x40100
		} // DRAGDROP_S_DROP
		return 0
	}),
	windows.NewCallback(func(this, effect uintptr) uintptr { return 0x40102 }), // default cursors
}

func shellDataObject(path string) (uintptr, error) {
	var full uintptr
	if hr := int32(call(shell32, "SHParseDisplayName", ptr(u16(path)), 0, ptr(&full), 0, 0)); hr < 0 {
		return 0, fmt.Errorf("无法准备拖动文件：0x%x", uint32(hr))
	}
	defer call(ole32, "CoTaskMemFree", full)
	parent := call(shell32, "ILClone", full) // ILCloneFull is a header alias, not a DLL export.
	if parent == 0 {
		return 0, errors.New("无法创建拖动路径")
	}
	defer call(ole32, "CoTaskMemFree", parent)
	call(shell32, "ILRemoveLastID", parent)
	child := call(shell32, "ILFindLastID", full)
	var data uintptr
	if hr := int32(call(shell32, "SHCreateDataObject", parent, 1, ptr(&child), 0, ptr(&iidDataObject), ptr(&data))); hr < 0 {
		return 0, fmt.Errorf("无法创建文件拖动数据：0x%x", uint32(hr))
	}
	return data, nil
}

func dragFile(path string) error {
	data, err := shellDataObject(path)
	if err != nil {
		return err
	}
	defer comRelease(data)
	source := dropSource{Vtable: &dropVtable}
	var effect uint32
	hr := int32(call(ole32, "DoDragDrop", data, ptr(&source), 1, ptr(&effect))) // DROPEFFECT_COPY
	runtime.KeepAlive(source)
	if hr < 0 {
		return fmt.Errorf("拖动图片失败：0x%x", uint32(hr))
	}
	return nil // cancellation is not an error
}
