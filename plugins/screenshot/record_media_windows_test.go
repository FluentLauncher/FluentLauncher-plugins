//go:build windows

package main

import (
	"image"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAudioMixer(t *testing.T) {
	m := newAudioMixer([]int{100, 50})
	if m.mix(0) != nil || m.mix(-10) != nil {
		t.Fatal("nothing to mix yet")
	}
	// Source 0 has two frames, source 1 one: the missing data is leading silence.
	m.push(0, []int16{100, -100, 200, -200})
	m.push(1, []int16{1000, 1000})
	if got := m.mix(3); len(got) != 6 || got[0] != 0 || got[2] != 100 || got[3] != -100 || got[4] != 700 || got[5] != 300 {
		t.Fatal(got)
	}
	// Loud sources clip instead of wrapping around.
	m.push(0, []int16{30000, -30000})
	m.push(1, []int16{30000, -30000})
	if got := m.mix(4); got[0] != 32767 || got[1] != -32768 || m.written != 4 {
		t.Fatal(got, m.written)
	}
	// Surplus stays queued for the next call; a paused recording drops it.
	m.push(0, []int16{1, 1, 2, 2, 3, 3})
	if got := m.mix(5); len(got) != 2 || got[0] != 1 {
		t.Fatal(got)
	}
	if got := m.mix(6); got[0] != 2 {
		t.Fatal(got)
	}
	m.reset()
	if got := m.mix(7); got[0] != 0 || got[1] != 0 {
		t.Fatal("reset kept paused audio")
	}
	// A capture clock that runs fast is cut back to the cushion.
	m.push(1, make([]int16, audioRate*2))
	m.mix(8)
	if len(m.queues[1]) != audioCushion*2 {
		t.Fatalf("backlog %d", len(m.queues[1]))
	}
}

// A 440 Hz tone that follows the wall clock, like a capture device.
type toneSource struct {
	last   time.Time
	phase  float64
	closed bool
}

func (s *toneSource) close() { s.closed = true }
func (s *toneSource) read(dst []int16) []int16 {
	now := time.Now()
	if s.last.IsZero() {
		s.last = now
		return dst
	}
	frames := int(now.Sub(s.last) * audioRate / time.Second)
	s.last = s.last.Add(time.Duration(frames) * time.Second / audioRate)
	for i := 0; i < frames; i++ {
		v := int16(8000 * math.Sin(s.phase))
		s.phase += 2 * math.Pi * 440 / audioRate
		dst = append(dst, v, v)
	}
	return dst
}

// Blue above green: a flipped or channel-swapped encode is visible.
type halvesSource struct{ out image.Point }

func (s halvesSource) size() image.Point { return s.out }
func (s halvesSource) close()            {}
func (s halvesSource) grab(dst []byte) error {
	for y := 0; y < s.out.Y; y++ {
		for x := 0; x < s.out.X; x++ {
			i := (y*s.out.X + x) * 4
			dst[i], dst[i+1], dst[i+2], dst[i+3] = 0, 0, 0, 255
			if y < s.out.Y/2 {
				dst[i] = 255
			} else {
				dst[i+1] = 255
			}
		}
	}
	return nil
}

// Opt-in: encodes a short synthetic clip with the system H.264 and AAC
// encoders and inspects it with ffprobe / ffmpeg when they are installed.
func TestNativeMP4Recording(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native recording QA")
	}
	for _, sound := range []bool{true, false} {
		dir := t.TempDir()
		size := image.Pt(320, 240)
		r := newRecorder(func() (frameSource, error) { return halvesSource{size}, nil }, dir, 30, time.Minute)
		r.extension = "mp4"
		r.encoder = func(path string, size image.Point) (recordEncoder, error) { return newMP4Writer(path, size, 30, sound) }
		tone := &toneSource{}
		if sound {
			r.audio = []func() (audioSource, error){func() (audioSource, error) { return tone, nil }}
			r.volumes = []int{100}
		}
		go r.run()
		// The system encoder needs a moment before the first frame is accepted.
		for deadline := time.Now().Add(10 * time.Second); r.elapsed.Load() == 0 && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(900 * time.Millisecond)
		if _, partial := recordedFiles(t, dir); len(partial) != 1 {
			t.Fatalf("running MP4 must be a .partial file: %v", partial)
		}
		r.paused.Store(true)
		time.Sleep(500 * time.Millisecond)
		r.paused.Store(false)
		time.Sleep(900 * time.Millisecond)
		r.finish(false)
		waitRecorder(t, r)
		if r.err != nil || !strings.HasSuffix(r.path, ".mp4") || (sound && !tone.closed) {
			t.Fatalf("mp4: %v %q", r.err, r.path)
		}
		data, err := os.ReadFile(r.path)
		if err != nil || len(data) < 1000 || string(data[4:8]) != "ftyp" {
			t.Fatalf("not an MP4 file: %v, %d bytes", err, len(data))
		}
		if finished, partial := recordedFiles(t, dir); len(finished) != 1 || len(partial) != 0 {
			t.Fatal(finished, partial)
		}
		recorded := time.Duration(r.elapsed.Load()).Seconds()
		if recorded < 1.5 || recorded > 2.2 {
			t.Fatalf("recorded %.2f s; the pause must not count", recorded)
		}
		if dir := os.Getenv("FL_SCREENSHOT_ARTIFACT_DIR"); dir != "" && sound {
			_ = os.MkdirAll(dir, 0755)
			_ = os.WriteFile(filepath.Join(dir, "record-sample.mp4"), data, 0644)
		}
		ffprobe, err := exec.LookPath("ffprobe")
		if err != nil {
			t.Log("ffprobe not installed: stream layout, duration and orientation were not checked")
			continue
		}
		out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name,width,height,sample_rate,channels,duration", "-of", "default=nw=1", r.path).CombinedOutput()
		if err != nil {
			t.Fatalf("ffprobe: %v %s", err, out)
		}
		probe := string(out)
		t.Logf("sound=%v recorded=%.2fs\n%s", sound, recorded, probe)
		for _, want := range []string{"codec_name=h264", "width=320", "height=240"} {
			if !strings.Contains(probe, want) {
				t.Fatalf("missing %s", want)
			}
		}
		if hasAudio := strings.Contains(probe, "codec_name=aac") && strings.Contains(probe, "sample_rate=48000") && strings.Contains(probe, "channels=2"); hasAudio != sound {
			t.Fatalf("audio stream present=%v, want %v", hasAudio, sound)
		}
		streams := 0
		for _, line := range strings.Split(probe, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "duration="); ok {
				seconds, err := strconv.ParseFloat(value, 64)
				if err != nil || math.Abs(seconds-recorded) > 0.3 {
					t.Fatalf("stream lasts %s s, recorded %.2f s", value, recorded)
				}
				streams++
			}
		}
		if want := map[bool]int{true: 2, false: 1}[sound]; streams != want {
			t.Fatalf("%d streams, want %d", streams, want)
		}
		ffmpeg, err := exec.LookPath("ffmpeg")
		if err != nil {
			continue
		}
		raw, err := exec.Command(ffmpeg, "-v", "error", "-i", r.path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
		if err != nil || len(raw) != 320*240*3 {
			t.Fatalf("decode: %v, %d bytes", err, len(raw))
		}
		top, bottom := raw[(20*320+160)*3:], raw[(220*320+160)*3:]
		if top[2] < 180 || top[1] > 80 || top[0] > 80 || bottom[1] < 180 || bottom[2] > 80 || bottom[0] > 80 {
			t.Fatalf("top %v bottom %v: want blue above green", top[:3], bottom[:3])
		}
		if sound {
			pcm, err := exec.Command(ffmpeg, "-v", "error", "-i", r.path, "-f", "s16le", "-ac", "1", "-").Output()
			peak := 0
			for i := 0; i+1 < len(pcm); i += 2 {
				peak = max(peak, abs(int(int16(uint16(pcm[i])|uint16(pcm[i+1])<<8))))
			}
			if err != nil || peak < 4000 || peak > 12000 {
				t.Fatalf("decoded tone peak %d (%v), want about 8000", peak, err)
			}
		}
	}
	if _, err := newMP4Writer(filepath.Join(t.TempDir(), "odd.mp4"), image.Pt(321, 240), 30, false); err == nil {
		t.Fatal("odd size accepted")
	}
}

// Opt-in: opens the real default devices. Nothing is stored; a machine
// without a microphone or with microphone access denied only logs that.
func TestNativeWASAPICapture(t *testing.T) {
	if os.Getenv("FL_SCREENSHOT_NATIVE_TEST") != "1" {
		t.Skip("opt-in native recording QA")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr := int32(call(ole32, "CoInitializeEx", 0, 0)); hr >= 0 {
		defer call(ole32, "CoUninitialize")
	}
	for _, loopback := range []bool{true, false} {
		source, err := openWASAPI(loopback)
		if err != nil {
			if loopback {
				t.Fatal(err)
			}
			t.Log("microphone unavailable:", err)
			continue
		}
		var pcm []int16
		for i := 0; i < 10; i++ {
			time.Sleep(40 * time.Millisecond)
			pcm = source.read(pcm)
		}
		source.close()
		source.close()
		t.Logf("loopback=%v captured %d frames in 400 ms", loopback, len(pcm)/2)
		if len(pcm)%2 != 0 || len(pcm)/2 > audioRate || (!loopback && len(pcm) == 0) {
			t.Fatalf("captured %d samples", len(pcm))
		}
	}
}
