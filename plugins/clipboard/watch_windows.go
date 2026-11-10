//go:build windows

package main

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const cfUnicodeText = 13

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procGlobalLock                 = kernel32.NewProc("GlobalLock")
	procGlobalUnlock               = kernel32.NewProc("GlobalUnlock")
	procGlobalSize                 = kernel32.NewProc("GlobalSize")
	procGetCurrentProcess          = kernel32.NewProc("GetCurrentProcess")
	procReadProcessMemory          = kernel32.NewProc("ReadProcessMemory")
)

// sysN calls a procedure and keeps its first result; the error return is
// ignored on purpose — every call site treats 0 as failure.
func sysN(proc *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := syscall.SyscallN(proc.Addr(), args...)
	return r
}

// StartWatch polls the clipboard sequence number and pushes every new text
// version to push. It never reports failure: a machine without a usable
// clipboard just yields an empty history.
func StartWatch(push func(string)) {
	go func() {
		var last uint32
		for {
			time.Sleep(400 * time.Millisecond)
			seq := uint32(sysN(procGetClipboardSequenceNumber))
			if seq == last {
				continue
			}
			last = seq
			if text := readClipboardText(); text != "" {
				push(text)
			}
		}
	}()
}

// readClipboardText returns the clipboard's Unicode text, or "" when the
// clipboard holds something else or any step of the dance fails. The locked
// block is copied out with ReadProcessMemory instead of converting its
// address to a Go pointer, which vet refuses to bless.
func readClipboardText() string {
	if sysN(procOpenClipboard, 0) == 0 {
		return ""
	}
	defer sysN(procCloseClipboard)
	handle := sysN(procGetClipboardData, cfUnicodeText)
	if handle == 0 {
		return ""
	}
	locked := sysN(procGlobalLock, handle)
	if locked == 0 {
		return ""
	}
	defer sysN(procGlobalUnlock, handle)
	size := sysN(procGlobalSize, handle)
	if size < 2 {
		return ""
	}
	buf := make([]uint16, size/2)
	var read uintptr
	ok := sysN(procReadProcessMemory, sysN(procGetCurrentProcess), locked,
		uintptr(unsafe.Pointer(&buf[0])), size, uintptr(unsafe.Pointer(&read)))
	if ok == 0 || read < 2 {
		return ""
	}
	return windows.UTF16ToString(buf[:read/2])
}
