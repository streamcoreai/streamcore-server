package pipeline

import (
	"sync"

	"github.com/streamcoreai/streamcore-server/internal/audio"
)

// realtimeChunk is one unit of work for the outbound pump: a frame of agent
// audio, the end of a response, or both.
type realtimeChunk struct {
	// Frame is a full-size PCM frame, or nil on an end-of-response marker
	// that carried no trailing audio.
	Frame []int16
	// EndOfResponse marks the agent's turn as finished, so the pump can wait
	// for playback to drain before reporting "listening".
	EndOfResponse bool
}

// realtimeAudioQueue buffers agent audio between the provider's read
// goroutine and the outbound sender.
//
// It exists because those two run at different speeds. A speech-to-speech
// model emits a whole response in a burst, well ahead of real time, while the
// sender plays it out at 20ms per frame. Pushing directly onto the rate-
// limited outbound channel would block the read goroutine — and since audio
// and control events share one ordered WebSocket stream, a barge-in event
// queued behind a long response would not be read until that response had
// finished playing. Buffering here keeps the read goroutine free, so
// interruptions are handled the moment they arrive.
//
// The buffer is unbounded by design: it holds at most one model response,
// which is bounded by the model, and dropping audio to cap it would corrupt
// playback.
type realtimeAudioQueue struct {
	mu   sync.Mutex
	cond *sync.Cond

	// pending is a flat sample buffer. Provider chunk boundaries do not
	// align to frames, so samples are accumulated and re-cut on the way out.
	pending []int16
	// flush asks for the sub-frame tail to be padded and emitted, ending the
	// current response.
	flush  bool
	closed bool
}

func newRealtimeAudioQueue() *realtimeAudioQueue {
	q := &realtimeAudioQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// push appends provider audio. It never blocks.
func (q *realtimeAudioQueue) push(samples []int16) {
	if len(samples) == 0 {
		return
	}
	q.mu.Lock()
	q.pending = append(q.pending, samples...)
	q.mu.Unlock()
	q.cond.Signal()
}

// discard drops all buffered audio, for when the caller barges in. Audio the
// provider already sent is stale the moment it stops generating.
func (q *realtimeAudioQueue) discard() {
	q.mu.Lock()
	q.pending = nil
	q.flush = false
	q.mu.Unlock()
	q.cond.Signal()
}

// endResponse marks the agent's turn complete. Any sub-frame tail is padded
// and emitted rather than held back waiting for audio that will not come.
func (q *realtimeAudioQueue) endResponse() {
	q.mu.Lock()
	q.flush = true
	q.mu.Unlock()
	q.cond.Signal()
}

// close unblocks any waiter and stops the queue permanently.
func (q *realtimeAudioQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast()
}

// next blocks until a frame is ready, a response ends, or the queue closes.
// It returns ok=false once closed.
func (q *realtimeAudioQueue) next() (realtimeChunk, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for {
		if q.closed {
			return realtimeChunk{}, false
		}

		if len(q.pending) >= audio.FrameSize {
			frame := make([]int16, audio.FrameSize)
			copy(frame, q.pending[:audio.FrameSize])
			q.pending = q.pending[audio.FrameSize:]
			return realtimeChunk{Frame: frame}, true
		}

		if q.flush {
			q.flush = false
			if len(q.pending) == 0 {
				return realtimeChunk{EndOfResponse: true}, true
			}
			// Pad the tail with silence. Only the very end of a turn gets
			// padding, so any discontinuity lands where the agent stops
			// talking rather than on every chunk boundary.
			frame := make([]int16, audio.FrameSize)
			copy(frame, q.pending)
			q.pending = nil
			return realtimeChunk{Frame: frame, EndOfResponse: true}, true
		}

		q.cond.Wait()
	}
}
