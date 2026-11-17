//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"unsafe"

	"golang.org/x/sys/windows"
)

// comCall invokes a COM method by its vtable index and returns the HRESULT.
func comCall(object uintptr, index int, args ...any) int32 {
	var table, method uintptr
	if object == 0 || nativeRead(object, &table) != nil || nativeRead(table+uintptr(index)*unsafe.Sizeof(table), &method) != nil {
		return -2147467261 // E_POINTER
	}
	return int32(callAddress(method, append([]any{object}, args...)...))
}

func mustGUID(value string) windows.GUID {
	id, err := windows.GUIDFromString(value)
	if err != nil {
		panic(err)
	}
	return id
}

func hresult(action string, hr int32) error {
	return fmt.Errorf("%s：0x%08x", action, uint32(hr))
}

var (
	clsidDeviceEnumerator = mustGUID("{BCDE0395-E52F-467C-8E3D-C4579291692E}")
	iidDeviceEnumerator   = mustGUID("{A95664D2-9614-4F35-A746-DE8DB63617E6}")
	iidAudioClient        = mustGUID("{1CB9AD4C-DBFA-4C32-B178-C2F568A703B2}")
	iidAudioCaptureClient = mustGUID("{C8ADBD64-E71E-48A0-A4DE-185C395CD317}")
)

// audioSource appends the frames captured since the last call. It is used
// on the recording thread only, which has initialized COM.
type audioSource interface {
	read(dst []int16) []int16
	close()
}

type waveFormat struct {
	Tag, Channels  uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	ExtraSize      uint16
}

type wasapiCapture struct {
	client, capture uintptr
	name            string
	failed          bool
}

// openWASAPI captures the default microphone, or what the default speakers
// play (loopback). Windows converts to the recording format in shared mode.
func openWASAPI(loopback bool) (audioSource, error) {
	name, flow, flags := "麦克风", 1, uint32(0x80000000|0x08000000) // eCapture; AUTOCONVERTPCM | SRC_DEFAULT_QUALITY
	if loopback {
		name, flow, flags = "系统声音", 0, flags|0x00020000 // eRender; LOOPBACK
	}
	var enumerator, device uintptr
	if hr := int32(call(ole32, "CoCreateInstance", ptr(&clsidDeviceEnumerator), 0, 23, ptr(&iidDeviceEnumerator), ptr(&enumerator))); hr < 0 {
		return nil, hresult("无法访问音频设备", hr)
	}
	defer comRelease(enumerator)
	if hr := comCall(enumerator, 4, flow, 0, ptr(&device)); hr < 0 { // GetDefaultAudioEndpoint(flow, eConsole)
		return nil, fmt.Errorf("没有可用的%s设备，请在声音菜单中关闭它后重试", name)
	}
	defer comRelease(device)
	c := &wasapiCapture{name: name}
	if hr := comCall(device, 3, ptr(&iidAudioClient), 23, 0, ptr(&c.client)); hr < 0 { // Activate
		return nil, hresult("无法打开"+name, hr)
	}
	format := waveFormat{Tag: 1, Channels: 2, SamplesPerSec: audioRate, AvgBytesPerSec: audioRate * 4, BlockAlign: 4, BitsPerSample: 16}
	// Initialize(shared, flags, one second of buffer, default period, format, no session)
	if hr := comCall(c.client, 3, 0, flags, 10_000_000, 0, ptr(&format), 0); hr < 0 {
		c.close()
		if uint32(hr) == 0x80070005 {
			return nil, errors.New("系统拒绝访问麦克风：请在 设置 → 隐私和安全性 → 麦克风 中允许桌面应用访问，或在声音菜单中关闭麦克风")
		}
		return nil, hresult("无法采集"+name, hr)
	}
	if hr := comCall(c.client, 14, ptr(&iidAudioCaptureClient), ptr(&c.capture)); hr < 0 { // GetService
		c.close()
		return nil, hresult("无法采集"+name, hr)
	}
	if hr := comCall(c.client, 10); hr < 0 { // Start
		c.close()
		return nil, hresult("无法开始采集"+name, hr)
	}
	return c, nil
}

func (c *wasapiCapture) read(dst []int16) []int16 {
	for !c.failed {
		var frames, flags uint32
		var data uintptr
		hr := comCall(c.capture, 5, ptr(&frames)) // GetNextPacketSize
		if hr >= 0 && frames == 0 {
			break
		}
		if hr >= 0 {
			hr = comCall(c.capture, 3, ptr(&data), ptr(&frames), ptr(&flags), 0, 0) // GetBuffer
		}
		if hr < 0 {
			// An unplugged or reconfigured device: the rest of the track is silent.
			c.failed = true
			log.Print(hresult(c.name+"采集中断", hr))
			break
		}
		if hr != 0 || frames == 0 { // AUDCLNT_S_BUFFER_EMPTY
			break
		}
		start := len(dst)
		dst = append(dst, make([]int16, frames*2)...)
		if flags&2 == 0 { // not AUDCLNT_BUFFERFLAGS_SILENT
			_ = windows.ReadProcessMemory(windows.CurrentProcess(), data, (*byte)(unsafe.Pointer(&dst[start])), uintptr(frames)*4, nil)
		}
		comCall(c.capture, 4, frames) // ReleaseBuffer
	}
	return dst
}

func (c *wasapiCapture) close() {
	if c.client != 0 {
		comCall(c.client, 11) // Stop
	}
	comRelease(c.capture)
	comRelease(c.client)
	c.capture, c.client = 0, 0
}
