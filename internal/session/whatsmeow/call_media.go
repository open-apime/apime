package whatsmeow

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"

	"github.com/purpshell/meowcaller"

	"github.com/open-apime/apime/internal/voip"
)

// MediaBridge links one call's media to a consumer: the peer's audio and video come
// out through OnAudio and OnVideo, and what the consumer sends goes into the call.
// Attaching a new bridge to the same call replaces the previous one.
type MediaBridge struct {
	call *meowcaller.Call
	out  *pcmSource
	once sync.Once
}

// AttachMedia hands the call's media to the given callbacks: audio as PCM16LE mono at
// 16 kHz in 60 ms frames, video as Annex-B H.264 access units, and the peer's camera
// rotation in quarter turns clockwise.
func (m *Manager) AttachMedia(instanceID, callID string, onAudio, onVideo func([]byte), onOrientation func(int)) (voip.MediaBridge, error) {
	tc, err := m.lookupCall(instanceID, callID)
	if err != nil {
		return nil, err
	}
	b := &MediaBridge{call: tc.call, out: &pcmSource{}}
	tc.call.Receive(meowcaller.SinkFunc(func(frame []float32) {
		onAudio(floatToPCM16(frame))
	}))
	tc.call.ReceiveVideo(&videoSink{onVideo: onVideo, onOrientation: onOrientation})
	tc.call.Play(b.out)
	return b, nil
}

// SendAudio queues PCM16LE mono 16 kHz audio to play into the call, in any chunk size.
func (b *MediaBridge) SendAudio(pcm []byte) {
	b.out.push(pcm16ToFloat(pcm))
}

// SendVideo sends one Annex-B H.264 access unit into the call.
func (b *MediaBridge) SendVideo(au []byte) error {
	if len(au) == 0 {
		return errors.New("access unit vazio")
	}
	return b.call.SendVideo(au)
}

// SetOrientation takes the clockwise turns that display this side's video upright,
// the same unit the bridge reports for the peer. WhatsApp signals the device rotation
// instead, which turns the other way (checked live: echoing 3 back showed it sideways).
func (b *MediaBridge) SetOrientation(quarterTurns int) error {
	return b.call.SetVideoOrientation((4 - quarterTurns%4) % 4)
}

// videoSink forwards the peer's video and the rotation found in its frames.
type videoSink struct {
	onVideo       func([]byte)
	onOrientation func(int)
}

func (s *videoSink) WriteVideo(au []byte) error { s.onVideo(au); return nil }
func (s *videoSink) Close() error               { return nil }
func (s *videoSink) SetOrientation(o int)       { s.onOrientation(o) }

// Close detaches the bridge; the call goes on, silent, until it is hung up.
func (b *MediaBridge) Close() {
	b.once.Do(func() {
		b.call.Receive(nil)
		b.call.ReceiveVideo(nil)
		_ = b.out.Close()
	})
}

// pcmSource feeds queued audio to the call one frame at a time, and silence when the
// queue runs dry, so the call never sees an end of stream while the bridge is open.
type pcmSource struct {
	mu     sync.Mutex
	buf    []float32
	closed bool
}

// maxQueued caps buffered audio at 2 s: a consumer that sends faster than real time
// would otherwise grow the delay without bound.
const maxQueued = 2 * meowcaller.SampleRate

func (s *pcmSource) push(samples []float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, samples...)
	if over := len(s.buf) - maxQueued; over > 0 {
		s.buf = s.buf[over:]
	}
}

func (s *pcmSource) ReadFrame() ([]float32, error) {
	frame := make([]float32, meowcaller.FrameSamples)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, voip.ErrMediaClosed
	}
	n := copy(frame, s.buf)
	s.buf = s.buf[n:]
	return frame, nil
}

func (s *pcmSource) Close() error {
	s.mu.Lock()
	s.closed = true
	s.buf = nil
	s.mu.Unlock()
	return nil
}

func floatToPCM16(frame []float32) []byte {
	out := make([]byte, len(frame)*2)
	for i, v := range frame {
		v = float32(math.Max(-1, math.Min(1, float64(v))))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(v*math.MaxInt16)))
	}
	return out
}

func pcm16ToFloat(pcm []byte) []float32 {
	out := make([]float32, len(pcm)/2)
	for i := range out {
		out[i] = float32(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / math.MaxInt16
	}
	return out
}
