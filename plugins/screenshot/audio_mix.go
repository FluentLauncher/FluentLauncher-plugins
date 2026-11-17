package main

// Recorded audio is 48 kHz, 16-bit, interleaved stereo. One frame is one
// left/right pair.
const (
	audioRate = 48000
	// The mixer stays this far behind the video clock, so the normal jitter
	// of capture packets is not mistaken for silence.
	audioCushion = audioRate / 20
)

// audioMixer turns capture packets that arrive in bursts, or not at all
// while nothing plays, into one continuous track on the recording clock.
type audioMixer struct {
	queues  [][]int16
	volumes []int // percent per source
	written int64 // frames handed out so far
	sum     []int32
	out     []int16
}

func newAudioMixer(volumes []int) *audioMixer {
	return &audioMixer{queues: make([][]int16, len(volumes)), volumes: volumes}
}

func (m *audioMixer) push(source int, pcm []int16) {
	m.queues[source] = append(m.queues[source], pcm...)
}

// reset drops everything captured while the recording was paused.
func (m *audioMixer) reset() {
	for i := range m.queues {
		m.queues[i] = m.queues[i][:0]
	}
}

// mix returns the frames from written up to target. A source that has too
// little data was silent: its samples are the most recent ones, so the
// silence goes first. The result is valid until the next call.
func (m *audioMixer) mix(target int64) []int16 {
	n := int(target - m.written)
	if n <= 0 {
		return nil
	}
	if cap(m.sum) < n*2 {
		m.sum, m.out = make([]int32, n*2), make([]int16, n*2)
	}
	sum, out := m.sum[:n*2], m.out[:n*2]
	clear(sum)
	for i, queue := range m.queues {
		take := min(len(queue)/2, n) * 2
		offset := n*2 - take
		for j, v := range queue[:take] {
			sum[offset+j] += int32(v) * int32(m.volumes[i]) / 100
		}
		rest := queue[take:]
		// A capture clock running fast must not push the sound ever later.
		if len(rest) > audioRate/2 {
			rest = rest[len(rest)-audioCushion*2:]
		}
		m.queues[i] = append(queue[:0], rest...)
	}
	for i, v := range sum {
		out[i] = int16(max(-32768, min(32767, v)))
	}
	m.written = target
	return out
}
