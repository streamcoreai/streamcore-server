package pipeline

import (
	"sync"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/audio"
)

func TestRealtimeAudioQueueRecutsChunksIntoFrames(t *testing.T) {
	q := newRealtimeAudioQueue()

	// Provider chunk boundaries do not align to frames: 500 samples is one
	// full 320-sample frame plus a 180-sample tail.
	q.push(make([]int16, 500))

	chunk, ok := q.next()
	if !ok {
		t.Fatal("next() returned closed on a queue with a full frame available")
	}
	if len(chunk.Frame) != audio.FrameSize {
		t.Fatalf("frame size = %d, want %d", len(chunk.Frame), audio.FrameSize)
	}
	if chunk.EndOfResponse {
		t.Error("EndOfResponse set mid-response")
	}

	// The 180-sample tail must be held, not padded, until more audio arrives
	// or the response ends.
	q.push(make([]int16, 140)) // 180 + 140 = exactly one more frame
	chunk, ok = q.next()
	if !ok || len(chunk.Frame) != audio.FrameSize {
		t.Fatalf("tail was not carried into the next chunk: ok=%v len=%d", ok, len(chunk.Frame))
	}
}

func TestRealtimeAudioQueuePreservesSampleOrder(t *testing.T) {
	q := newRealtimeAudioQueue()

	// Two chunks that together span more than one frame, with a recognisable
	// ramp so re-cutting cannot silently reorder or drop samples.
	total := audio.FrameSize + 100
	ramp := make([]int16, total)
	for i := range ramp {
		ramp[i] = int16(i)
	}
	q.push(ramp[:200])
	q.push(ramp[200:])
	q.endResponse()

	var got []int16
	for {
		chunk, ok := q.next()
		if !ok {
			break
		}
		got = append(got, chunk.Frame...)
		if chunk.EndOfResponse {
			break
		}
	}

	// Two frames out: the full ramp plus silence padding on the tail.
	if len(got) != 2*audio.FrameSize {
		t.Fatalf("emitted %d samples, want %d", len(got), 2*audio.FrameSize)
	}
	for i := 0; i < total; i++ {
		if got[i] != int16(i) {
			t.Fatalf("sample %d = %d, want %d", i, got[i], i)
		}
	}
	for i := total; i < len(got); i++ {
		if got[i] != 0 {
			t.Fatalf("tail padding at %d = %d, want silence", i, got[i])
		}
	}
}

func TestRealtimeAudioQueueEndResponsePadsTail(t *testing.T) {
	q := newRealtimeAudioQueue()

	q.push(make([]int16, 100)) // well under a frame
	q.endResponse()

	chunk, ok := q.next()
	if !ok {
		t.Fatal("next() returned closed")
	}
	if len(chunk.Frame) != audio.FrameSize {
		t.Fatalf("tail frame not padded: len = %d, want %d", len(chunk.Frame), audio.FrameSize)
	}
	if !chunk.EndOfResponse {
		t.Error("EndOfResponse not set on the final frame of a response")
	}
}

func TestRealtimeAudioQueueEndResponseWithNoTail(t *testing.T) {
	q := newRealtimeAudioQueue()
	q.endResponse()

	chunk, ok := q.next()
	if !ok {
		t.Fatal("next() returned closed")
	}
	if chunk.Frame != nil {
		t.Errorf("emitted a frame for an empty response: len = %d", len(chunk.Frame))
	}
	if !chunk.EndOfResponse {
		t.Error("EndOfResponse not reported when a response ends with no audio")
	}
}

// Barge-in correctness: audio the provider sent before it noticed the
// interruption must not play.
func TestRealtimeAudioQueueDiscardDropsBufferedAudio(t *testing.T) {
	q := newRealtimeAudioQueue()

	q.push(make([]int16, 10*audio.FrameSize))
	q.discard()

	// Nothing buffered should survive; end the response so next() has a
	// reason to return rather than blocking forever.
	q.endResponse()
	chunk, ok := q.next()
	if !ok {
		t.Fatal("next() returned closed")
	}
	if chunk.Frame != nil {
		t.Errorf("discarded audio still played: %d samples", len(chunk.Frame))
	}
	if !chunk.EndOfResponse {
		t.Error("EndOfResponse not reported after discard")
	}
}

// A pending endResponse must not survive a barge-in, or the interrupted turn
// would report completion after the caller already took over.
func TestRealtimeAudioQueueDiscardClearsPendingEndResponse(t *testing.T) {
	q := newRealtimeAudioQueue()

	q.endResponse()
	q.discard()

	done := make(chan realtimeChunk, 1)
	go func() {
		chunk, _ := q.next()
		done <- chunk
	}()

	// next() should still be blocked. Unblock it with real audio and confirm
	// what comes back is the audio, not a stale end-of-response.
	q.push(make([]int16, audio.FrameSize))
	chunk := <-done
	if chunk.EndOfResponse {
		t.Error("stale endResponse survived a discard")
	}
}

func TestRealtimeAudioQueueNextBlocksUntilFrameReady(t *testing.T) {
	q := newRealtimeAudioQueue()

	started := make(chan struct{})
	done := make(chan bool, 1)
	go func() {
		close(started)
		_, ok := q.next()
		done <- ok
	}()

	<-started
	select {
	case <-done:
		t.Fatal("next() returned before any audio was pushed")
	default:
	}

	q.push(make([]int16, audio.FrameSize))
	if ok := <-done; !ok {
		t.Error("next() reported closed after a push")
	}
}

func TestRealtimeAudioQueueCloseUnblocksWaiter(t *testing.T) {
	q := newRealtimeAudioQueue()

	done := make(chan bool, 1)
	go func() {
		_, ok := q.next()
		done <- ok
	}()

	q.close()
	if ok := <-done; ok {
		t.Error("next() did not report closed after close()")
	}
}

// The queue is written by the provider read goroutine and read by the
// outbound pump, with barge-in landing from a third. Run under -race.
func TestRealtimeAudioQueueConcurrentAccess(t *testing.T) {
	q := newRealtimeAudioQueue()

	// Writers are waited on separately from the consumer, and close() is what
	// bounds the test.
	//
	// The consumer asks for a fixed number of frames, but the queue never
	// promises that many will materialise: discard() clears the buffer *and*
	// any pending endResponse (see
	// TestRealtimeAudioQueueDiscardClearsPendingEndResponse). If a discard()
	// lands after the last push, the consumer sits in next() with an empty
	// buffer, flush cleared and the queue still open — blocked until close().
	// Waiting on the consumer in the same group as the writers therefore hangs
	// until the test binary's timeout, roughly one run in fifty.
	var writers sync.WaitGroup
	writers.Add(3)

	go func() {
		defer writers.Done()
		for i := 0; i < 200; i++ {
			q.push(make([]int16, 64))
		}
		q.endResponse()
	}()

	go func() {
		defer writers.Done()
		for i := 0; i < 50; i++ {
			q.discard()
		}
	}()

	go func() {
		defer writers.Done()
		for i := 0; i < 400; i++ {
			q.push(make([]int16, audio.FrameSize))
		}
	}()

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for i := 0; i < 100; i++ {
			if _, ok := q.next(); !ok {
				return
			}
		}
	}()

	writers.Wait()
	q.close()
	<-consumerDone
}
