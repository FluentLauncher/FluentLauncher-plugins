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

var combase = windows.NewLazySystemDLL("combase.dll")

// Windows Runtime interfaces used for text recognition. Methods start at
// vtable slot 6, after IInspectable; the slots below follow the metadata order.
var (
	iidOcrEngineStatics      = mustGUID("{5BFFA85A-3384-3540-9940-699120D428A8}")
	iidSoftwareBitmapStatics = mustGUID("{DF0385DB-672F-4A9D-806E-C2442F343E86}")
	iidBufferFactory         = mustGUID("{71AF914D-C10F-484B-BC50-14BC623B3A27}")
	iidBufferByteAccess      = mustGUID("{905A0FEF-BC53-11DF-8C49-001E4FC686DA}") // IUnknown based
	iidAsyncInfo             = mustGUID("{00000036-0000-0000-C000-000000000046}")
)

const (
	ocrMaxSide     = 10000 // OcrEngine.MaxImageDimension
	ocrPixelFormat = 87    // BitmapPixelFormat.Bgra8
	ocrTimeout     = 20 * time.Second
	// Selections up to about one 2560×1600 screen are enlarged before recognition.
	ocrEnlargePixels = 4 << 20
)

var errNoOCRLanguage = errors.New("系统没有可用的文字识别语言，请在 设置 → 时间和语言 → 语言和区域 中为常用语言安装「光学字符识别」功能")

func activationFactory(class string, iid *windows.GUID) (uintptr, error) {
	name := windows.StringToUTF16(class)
	var id, factory uintptr
	if hr := int32(call(combase, "WindowsCreateString", ptr(&name[0]), len(name)-1, ptr(&id))); hr < 0 {
		return 0, hresult("无法使用系统文字识别", hr)
	}
	defer call(combase, "WindowsDeleteString", id)
	if hr := int32(call(combase, "RoGetActivationFactory", id, ptr(iid), ptr(&factory))); hr < 0 {
		return 0, hresult("无法使用系统文字识别", hr)
	}
	return factory, nil
}

// hstringText copies and releases a string returned by the runtime.
func hstringText(value uintptr) string {
	if value == 0 {
		return ""
	}
	defer call(combase, "WindowsDeleteString", value)
	var length uint32
	address := call(combase, "WindowsGetStringRawBuffer", value, ptr(&length))
	if address == 0 || length == 0 {
		return ""
	}
	text := make([]uint16, length)
	if windows.ReadProcessMemory(windows.CurrentProcess(), address, (*byte)(unsafe.Pointer(&text[0])), uintptr(length)*2, nil) != nil {
		return ""
	}
	return windows.UTF16ToString(text)
}

// ocrBitmap copies the pixels into a SoftwareBitmap the recognizer accepts.
func ocrBitmap(img *image.RGBA) (uintptr, error) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	pixels := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride : y*img.Stride+w*4]
		dst := pixels[y*w*4:]
		for x := 0; x < w*4; x += 4 {
			dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 255
		}
	}
	factory, err := activationFactory("Windows.Storage.Streams.Buffer", &iidBufferFactory)
	if err != nil {
		return 0, err
	}
	defer comRelease(factory)
	var buffer, access, address, statics, bitmap uintptr
	if hr := comCall(factory, 6, uint32(len(pixels)), ptr(&buffer)); hr < 0 {
		return 0, hresult("无法准备识别图片", hr)
	}
	defer comRelease(buffer)
	if hr := comCall(buffer, 0, ptr(&iidBufferByteAccess), ptr(&access)); hr < 0 {
		return 0, hresult("无法准备识别图片", hr)
	}
	defer comRelease(access)
	if hr := comCall(access, 3, ptr(&address)); hr < 0 || address == 0 {
		return 0, hresult("无法准备识别图片", hr)
	}
	if err := windows.WriteProcessMemory(windows.CurrentProcess(), address, &pixels[0], uintptr(len(pixels)), nil); err != nil {
		return 0, err
	}
	// An IBuffer starts with length 0; the copy below reads only Length bytes.
	if hr := comCall(buffer, 8, uint32(len(pixels))); hr < 0 {
		return 0, hresult("无法准备识别图片", hr)
	}
	statics, err = activationFactory("Windows.Graphics.Imaging.SoftwareBitmap", &iidSoftwareBitmapStatics)
	if err != nil {
		return 0, err
	}
	defer comRelease(statics)
	if hr := comCall(statics, 9, buffer, ocrPixelFormat, w, h, ptr(&bitmap)); hr < 0 || bitmap == 0 {
		return 0, hresult("无法准备识别图片", hr)
	}
	return bitmap, nil
}

// ocrEngine prefers the user's languages and falls back to any installed one.
func ocrEngine() (uintptr, error) {
	statics, err := activationFactory("Windows.Media.Ocr.OcrEngine", &iidOcrEngineStatics)
	if err != nil {
		return 0, err
	}
	defer comRelease(statics)
	var engine, languages, language uintptr
	// A missing language pack is a success with a null engine.
	if hr := comCall(statics, 10, ptr(&engine)); hr >= 0 && engine != 0 {
		return engine, nil
	}
	if hr := comCall(statics, 7, ptr(&languages)); hr < 0 || languages == 0 {
		return 0, errNoOCRLanguage
	}
	defer comRelease(languages)
	if hr := comCall(languages, 6, uint32(0), ptr(&language)); hr < 0 || language == 0 {
		return 0, errNoOCRLanguage
	}
	defer comRelease(language)
	if hr := comCall(statics, 9, language, ptr(&engine)); hr < 0 || engine == 0 {
		return 0, errNoOCRLanguage
	}
	return engine, nil
}

// awaitAsync polls an IAsyncOperation instead of implementing a completion
// delegate, then returns its result (slot 8 of every IAsyncOperation<T>).
func awaitAsync(operation uintptr) (uintptr, error) {
	var info, result uintptr
	if hr := comCall(operation, 0, ptr(&iidAsyncInfo), ptr(&info)); hr < 0 {
		return 0, hresult("文字识别失败", hr)
	}
	defer comRelease(info)
	defer comCall(info, 10) // Close
	deadline := time.Now().Add(ocrTimeout)
	for {
		var status int32
		if hr := comCall(info, 7, ptr(&status)); hr < 0 {
			return 0, hresult("文字识别失败", hr)
		}
		switch status {
		case 1: // Completed
			if hr := comCall(operation, 8, ptr(&result)); hr < 0 || result == 0 {
				return 0, hresult("文字识别失败", hr)
			}
			return result, nil
		case 2:
			return 0, errors.New("文字识别已取消")
		case 3:
			var code int32
			comCall(info, 8, ptr(&code))
			return 0, hresult("文字识别失败", code)
		}
		if time.Now().After(deadline) {
			comCall(info, 9) // Cancel
			return 0, errors.New("文字识别超时，请缩小选区后重试")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// recognizeText runs the recognizer that ships with Windows on the calling
// goroutine. It must not be called on the GUI thread: it blocks, and that
// thread is a single-threaded apartment.
func recognizeText(img *image.RGBA) (string, error) {
	if img.Rect.Dx() < 8 || img.Rect.Dy() < 8 {
		return "", errors.New("选区太小，无法识别文字")
	}
	if img.Rect.Dx() > ocrMaxSide || img.Rect.Dy() > ocrMaxSide {
		return "", errors.New("选区超过文字识别支持的 10000 像素，请缩小选区")
	}
	// Twice the size reads screen text best: measured with 12 and 18 pixel UI
	// text, larger factors start to split words. Big selections stay as they are.
	if w, h := img.Rect.Dx(), img.Rect.Dy(); w*2 <= ocrMaxSide && h*2 <= ocrMaxSide && w*h <= ocrEnlargePixels {
		img = enlarge(img, 2)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if combase.Load() != nil {
		return "", errors.New("此系统不支持文字识别")
	}
	// S_FALSE: already initialized. RPC_E_CHANGED_MODE: the thread is usable as it is.
	if hr := int32(call(combase, "RoInitialize", 1)); hr >= 0 {
		defer call(combase, "RoUninitialize")
	} else if uint32(hr) != 0x80010106 {
		return "", hresult("无法使用系统文字识别", hr)
	}
	engine, err := ocrEngine()
	if err != nil {
		return "", err
	}
	defer comRelease(engine)
	bitmap, err := ocrBitmap(img)
	if err != nil {
		return "", err
	}
	defer comRelease(bitmap)
	var operation, lines uintptr
	if hr := comCall(engine, 6, bitmap, ptr(&operation)); hr < 0 || operation == 0 {
		return "", hresult("文字识别失败", hr)
	}
	defer comRelease(operation)
	result, err := awaitAsync(operation)
	if err != nil {
		return "", err
	}
	defer comRelease(result)
	if hr := comCall(result, 6, ptr(&lines)); hr < 0 || lines == 0 {
		return "", hresult("文字识别失败", hr)
	}
	defer comRelease(lines)
	var count uint32
	comCall(lines, 7, ptr(&count))
	text := make([]string, 0, count)
	for i := uint32(0); i < count; i++ {
		var line, value uintptr
		if comCall(lines, 6, i, ptr(&line)) < 0 || line == 0 {
			continue
		}
		if comCall(line, 7, ptr(&value)) >= 0 {
			text = append(text, hstringText(value))
		}
		comRelease(line)
	}
	joined := joinOCRLines(text)
	if joined == "" {
		return "", errors.New("没有识别到文字，请调整选区后重试")
	}
	return joined, nil
}
