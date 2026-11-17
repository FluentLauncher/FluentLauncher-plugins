//go:build windows

package main

import (
	"errors"
	"image"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	mfplat      = windows.NewLazySystemDLL("mfplat.dll")
	mfreadwrite = windows.NewLazySystemDLL("mfreadwrite.dll")
)

// Media Foundation attribute keys and formats (mfapi.h, mfreadwrite.h).
var (
	mfMajorType        = mustGUID("{48EBA18E-F8C9-4687-BF11-0A74C9F96A8F}")
	mfSubtype          = mustGUID("{F7E34C9A-42E8-4714-B74B-CB29D72C35E5}")
	mfAvgBitrate       = mustGUID("{20332624-FB0D-4D9E-BD0D-CBF6786C102E}")
	mfInterlaceMode    = mustGUID("{E2724BB8-E676-4806-B4B2-A8D6EFB44CCD}")
	mfFrameSize        = mustGUID("{1652C33D-D6B2-4012-B834-72030849A37D}")
	mfFrameRate        = mustGUID("{C459A2E8-3D2C-4E44-B132-FEE5156C7BB0}")
	mfPixelAspectRatio = mustGUID("{C6376A1E-8D0A-4027-BE45-6D9A0AD39BB6}")
	mfDefaultStride    = mustGUID("{644B4E48-1E02-4516-B0EB-C01CA9D49AC6}")
	mfAudioChannels    = mustGUID("{37E48BF5-645E-4C5B-89DE-ADA9E29B696A}")
	mfAudioSampleRate  = mustGUID("{5FAEEAE7-0290-4C31-9E8A-C534F68D9DBA}")
	mfAudioAvgBytes    = mustGUID("{1AAB75C8-CFEF-451C-AB95-AC034B8E1731}")
	mfAudioBlockAlign  = mustGUID("{322DE230-9EEB-43BD-AB7A-FF412251541D}")
	mfAudioBits        = mustGUID("{F2DEB57F-40FA-4764-AA33-ED4F2D1FF669}")
	mfHardware         = mustGUID("{A634A91C-822B-41B9-A494-4DE4643612B0}") // MF_READWRITE_ENABLE_HARDWARE_TRANSFORMS
	mfNoThrottling     = mustGUID("{08B845D8-2B74-4AFE-9D53-BE16D2D5AE4F}") // MF_SINK_WRITER_DISABLE_THROTTLING
	mfContainerType    = mustGUID("{150FF23F-4ABC-478B-AC4F-E1916FBA1CCA}") // MF_TRANSCODE_CONTAINERTYPE
	mfContainerMPEG4   = mustGUID("{DC6CD05D-B9D0-40EF-BD35-FA622C1AB28A}")
	mfTypeVideo        = mustGUID("{73646976-0000-0010-8000-00AA00389B71}")
	mfTypeAudio        = mustGUID("{73647561-0000-0010-8000-00AA00389B71}")
	mfVideoH264        = mustGUID("{34363248-0000-0010-8000-00AA00389B71}")
	mfVideoRGB32       = mustGUID("{00000016-0000-0010-8000-00AA00389B71}")
	mfAudioAAC         = mustGUID("{00001610-0000-0010-8000-00AA00389B71}")
	mfAudioPCM         = mustGUID("{00000001-0000-0010-8000-00AA00389B71}")
)

// IMFAttributes vtable slots, shared by media types and samples.
const (
	mfSetUINT32 = 21
	mfSetUINT64 = 22
	mfSetGUID   = 24
)

// mp4Writer encodes H.264 video and optional AAC audio with the encoders
// that ship with Windows. It lives on the recording thread.
type mp4Writer struct {
	writer       uintptr
	video, audio uint32
	hasAudio     bool
	size         image.Point
	frame        time.Duration
	path         string
	started      bool
}

func newMP4Writer(path string, size image.Point, fps int, audio bool) (*mp4Writer, error) {
	if size.X < 2 || size.Y < 2 || size.X%2 != 0 || size.Y%2 != 0 {
		return nil, errors.New("MP4 画面尺寸必须是偶数")
	}
	if mfplat.Load() != nil || mfreadwrite.Load() != nil {
		return nil, errors.New("此系统缺少媒体功能包，无法录制 MP4；请改用 GIF，或在 可选功能 中安装媒体功能包")
	}
	if hr := int32(call(mfplat, "MFStartup", 0x00020070, 0)); hr < 0 {
		return nil, hresult("无法启动媒体编码", hr)
	}
	w := &mp4Writer{size: size, frame: time.Second / time.Duration(fps), path: path, hasAudio: audio, started: true}
	// Some hardware encoders reject small or unusual sizes; retry in software.
	err := w.open(fps, true)
	if err != nil {
		w.release()
		_ = os.Remove(path)
		err = w.open(fps, false)
	}
	if err != nil {
		w.abort()
		return nil, err
	}
	return w, nil
}

// mediaType builds an IMFMediaType from GUID, UINT32 and UINT64 attributes.
func mediaType(guids map[*windows.GUID]*windows.GUID, values map[*windows.GUID]uint32, wide map[*windows.GUID]uint64) (uintptr, error) {
	var t uintptr
	hr := int32(call(mfplat, "MFCreateMediaType", ptr(&t)))
	for key, value := range guids {
		if hr >= 0 {
			hr = comCall(t, mfSetGUID, ptr(key), ptr(value))
		}
	}
	for key, value := range values {
		if hr >= 0 {
			hr = comCall(t, mfSetUINT32, ptr(key), value)
		}
	}
	for key, value := range wide {
		if hr >= 0 {
			hr = comCall(t, mfSetUINT64, ptr(key), uintptr(value))
		}
	}
	if hr < 0 {
		comRelease(t)
		return 0, hresult("无法描述媒体格式", hr)
	}
	return t, nil
}

func (w *mp4Writer) open(fps int, hardware bool) error {
	var attributes uintptr
	if hr := int32(call(mfplat, "MFCreateAttributes", ptr(&attributes), 3)); hr < 0 {
		return hresult("无法创建编码设置", hr)
	}
	defer comRelease(attributes)
	if hardware {
		comCall(attributes, mfSetUINT32, ptr(&mfHardware), uint32(1))
	}
	comCall(attributes, mfSetUINT32, ptr(&mfNoThrottling), uint32(1))
	// The unfinished file has no .mp4 extension; name the container instead.
	comCall(attributes, mfSetGUID, ptr(&mfContainerType), ptr(&mfContainerMPEG4))
	if hr := int32(call(mfreadwrite, "MFCreateSinkWriterFromURL", ptr(u16(w.path)), 0, attributes, ptr(&w.writer))); hr < 0 {
		return hresult("无法创建 MP4 文件", hr)
	}
	pack := func(high, low int) uint64 { return uint64(high)<<32 | uint64(low) }
	bitrate := uint32(max(1_000_000, min(20_000_000, w.size.X*w.size.Y*fps/10)))
	frame := map[*windows.GUID]uint64{&mfFrameSize: pack(w.size.X, w.size.Y), &mfFrameRate: pack(fps, 1), &mfPixelAspectRatio: pack(1, 1)}
	add := func(output, input uintptr, index *uint32) error {
		defer comRelease(output)
		defer comRelease(input)
		if hr := comCall(w.writer, 3, output, ptr(index)); hr < 0 { // AddStream
			return hresult("系统编码器不支持此格式", hr)
		}
		if hr := comCall(w.writer, 4, *index, input, 0); hr < 0 { // SetInputMediaType
			return hresult("系统编码器不接受录制的画面或声音", hr)
		}
		return nil
	}
	output, err := mediaType(map[*windows.GUID]*windows.GUID{&mfMajorType: &mfTypeVideo, &mfSubtype: &mfVideoH264},
		map[*windows.GUID]uint32{&mfAvgBitrate: bitrate, &mfInterlaceMode: 2}, frame)
	if err != nil {
		return err
	}
	// A positive stride declares top-down rows, the order frames are captured in.
	input, err := mediaType(map[*windows.GUID]*windows.GUID{&mfMajorType: &mfTypeVideo, &mfSubtype: &mfVideoRGB32},
		map[*windows.GUID]uint32{&mfInterlaceMode: 2, &mfDefaultStride: uint32(w.size.X * 4)}, frame)
	if err != nil {
		comRelease(output)
		return err
	}
	if err := add(output, input, &w.video); err != nil {
		return err
	}
	if w.hasAudio {
		pcm := map[*windows.GUID]uint32{&mfAudioBits: 16, &mfAudioSampleRate: audioRate, &mfAudioChannels: 2}
		output, err := mediaType(map[*windows.GUID]*windows.GUID{&mfMajorType: &mfTypeAudio, &mfSubtype: &mfAudioAAC},
			map[*windows.GUID]uint32{&mfAudioBits: 16, &mfAudioSampleRate: audioRate, &mfAudioChannels: 2, &mfAudioAvgBytes: 20000}, nil)
		if err != nil {
			return err
		}
		pcm[&mfAudioBlockAlign], pcm[&mfAudioAvgBytes] = 4, audioRate*4
		input, err := mediaType(map[*windows.GUID]*windows.GUID{&mfMajorType: &mfTypeAudio, &mfSubtype: &mfAudioPCM}, pcm, nil)
		if err != nil {
			comRelease(output)
			return err
		}
		if err := add(output, input, &w.audio); err != nil {
			return err
		}
	}
	if hr := comCall(w.writer, 5); hr < 0 { // BeginWriting
		return hresult("无法开始写入 MP4", hr)
	}
	return nil
}

// write hands one block of bytes with its time span (100 ns units) to a stream.
func (w *mp4Writer) write(stream uint32, data unsafe.Pointer, length int, at, duration time.Duration) error {
	var buffer, sample, address uintptr
	hr := int32(call(mfplat, "MFCreateMemoryBuffer", length, ptr(&buffer)))
	if hr < 0 {
		return hresult("无法分配编码缓冲", hr)
	}
	defer comRelease(buffer)
	if hr = comCall(buffer, 3, ptr(&address), 0, 0); hr >= 0 { // Lock
		err := windows.WriteProcessMemory(windows.CurrentProcess(), address, (*byte)(data), uintptr(length), nil)
		comCall(buffer, 4) // Unlock
		if err != nil {
			return err
		}
		hr = comCall(buffer, 6, length) // SetCurrentLength
	}
	if hr >= 0 {
		hr = int32(call(mfplat, "MFCreateSample", ptr(&sample)))
	}
	if hr < 0 {
		return hresult("无法准备编码数据", hr)
	}
	defer comRelease(sample)
	if hr = comCall(sample, 42, buffer); hr >= 0 { // AddBuffer
		hr = comCall(sample, 36, uintptr(at/100)) // SetSampleTime
	}
	if hr >= 0 {
		hr = comCall(sample, 38, uintptr(duration/100)) // SetSampleDuration
	}
	if hr >= 0 {
		hr = comCall(w.writer, 6, stream, sample) // WriteSample
	}
	if hr < 0 {
		return hresult("MP4 编码失败", hr)
	}
	return nil
}

func (w *mp4Writer) addFrame(bgra []byte, stride int, at time.Duration) error {
	if stride != w.size.X*4 || len(bgra) < stride*w.size.Y {
		return errors.New("录制帧尺寸不匹配")
	}
	return w.write(w.video, unsafe.Pointer(&bgra[0]), stride*w.size.Y, at, w.frame)
}

func (w *mp4Writer) addAudio(pcm []int16, at time.Duration) error {
	if !w.hasAudio || len(pcm) == 0 {
		return nil
	}
	return w.write(w.audio, unsafe.Pointer(&pcm[0]), len(pcm)*2, at, time.Duration(len(pcm)/2)*time.Second/audioRate)
}

// bytes reports what has reached the disk so far.
func (w *mp4Writer) bytes() int64 {
	if info, err := os.Stat(w.path); err == nil {
		return info.Size()
	}
	return 0
}

// close completes the file; without it an MP4 cannot be played.
func (w *mp4Writer) close(time.Duration) error {
	hr := comCall(w.writer, 11) // Finalize
	w.abort()
	if hr < 0 {
		return hresult("无法完成 MP4 文件", hr)
	}
	return nil
}

func (w *mp4Writer) release() {
	comRelease(w.writer)
	w.writer = 0
}

func (w *mp4Writer) abort() {
	w.release()
	if w.started {
		w.started = false
		call(mfplat, "MFShutdown")
	}
}
