package stt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// collectMoonshineFrames routes frames through a callback wired to a channel,
// the way the read loop does, so the routing can be exercised without a socket.
func collectMoonshineFrames(t *testing.T, frames ...string) []TranscriptResult {
	t.Helper()

	var got []TranscriptResult
	cb := &deepgramCallback{onResult: func(r TranscriptResult) {
		got = append(got, r)
	}}

	for _, frame := range frames {
		if err := routeMoonshineSTTFrame([]byte(frame), cb); err != nil {
			t.Fatalf("frame %q rejected: %v", frame, err)
		}
	}
	return got
}

// The sidecar completes a line only at the end of an utterance, so its final
// frame carries both is_final and speech_final. That is the combination the
// Deepgram callback treats as a finished turn.
func TestMoonshineFinalFrameEmitsOneFinal(t *testing.T) {
	frame := `{"type":"Results","channel_index":[0,1],"start":0.5,"duration":1.8,` +
		`"is_final":true,"speech_final":true,` +
		`"channel":{"alternatives":[{"transcript":"what is the weather today"}]}}`

	got := collectMoonshineFrames(t, frame)

	if len(got) != 1 {
		t.Fatalf("emitted %d results, want 1: %+v", len(got), got)
	}
	if !got[0].IsFinal {
		t.Error("result reported partial, want final")
	}
	if got[0].Text != "what is the weather today" {
		t.Errorf("text = %q, want the full line", got[0].Text)
	}
}

// Moonshine reports no confidence, so the field is absent from every frame.
// Zero is what the pipeline reads as "unknown"; anything else would be a
// number the model never produced.
func TestMoonshineMissingConfidenceIsUnknown(t *testing.T) {
	frame := `{"type":"Results","is_final":true,"speech_final":true,` +
		`"channel":{"alternatives":[{"transcript":"hello"}]}}`

	got := collectMoonshineFrames(t, frame)

	if len(got) != 1 {
		t.Fatalf("emitted %d results, want 1", len(got))
	}
	if got[0].Confidence != 0 {
		t.Errorf("confidence = %v, want 0 (absent maps to unknown)", got[0].Confidence)
	}
}

// Partials are what barge-in classifies a short burst with; losing them
// degrades interruption to VAD alone.
func TestMoonshineInterimFrameEmitsPartial(t *testing.T) {
	frame := `{"type":"Results","is_final":false,"speech_final":false,` +
		`"channel":{"alternatives":[{"transcript":"what is the"}]}}`

	got := collectMoonshineFrames(t, frame)

	if len(got) != 1 {
		t.Fatalf("emitted %d results, want 1", len(got))
	}
	if got[0].IsFinal {
		t.Error("interim frame reported final, want partial")
	}
	if got[0].Text != "what is the" {
		t.Errorf("text = %q, want %q", got[0].Text, "what is the")
	}
}

// Routing the frames through the Deepgram callback is the whole point of the
// wire format: a final that never carries speech_final still has to reach the
// caller, flushed by UtteranceEnd rather than sitting in the accumulator.
func TestMoonshineUtteranceEndFlushesBufferedFinal(t *testing.T) {
	got := collectMoonshineFrames(t,
		`{"type":"Results","is_final":true,"speech_final":false,`+
			`"channel":{"alternatives":[{"transcript":"book a table"}]}}`,
		`{"type":"UtteranceEnd","channel":[0,1],"last_word_end":2.3}`,
	)

	var finals []TranscriptResult
	for _, r := range got {
		if r.IsFinal {
			finals = append(finals, r)
		}
	}
	if len(finals) != 1 {
		t.Fatalf("emitted %d finals, want 1: %+v", len(finals), got)
	}
	if finals[0].Text != "book a table" {
		t.Errorf("flushed text = %q, want %q", finals[0].Text, "book a table")
	}
}

// Metadata and SpeechStarted carry nothing the pipeline consumes. Dropping
// them quietly is what the Deepgram SDK does; treating them as errors would
// fill the log on every session.
func TestMoonshineNonTranscriptFramesEmitNothing(t *testing.T) {
	got := collectMoonshineFrames(t,
		`{"type":"SpeechStarted","channel":[0,1],"timestamp":0.5}`,
		`{"type":"Metadata","request_id":"abc","channels":1,"duration":4.2}`,
		`{"type":"Something-New"}`,
	)

	if len(got) != 0 {
		t.Fatalf("emitted %d results, want none: %+v", len(got), got)
	}
}

func TestMoonshineRejectsMalformedJSON(t *testing.T) {
	cb := &deepgramCallback{onResult: func(TranscriptResult) {}}

	if err := routeMoonshineSTTFrame([]byte("not json"), cb); err == nil {
		t.Fatal("malformed frame accepted, want an error")
	}
}

// fakeMoonshineSTT stands in for the sidecar: it upgrades the connection,
// writes a scripted set of frames, and keeps the socket open so the client's
// read loop is exercised end to end.
func newFakeMoonshineSTT(t *testing.T, frames ...string) string {
	t.Helper()

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Wait for audio before answering, so the test covers the send path too.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		for _, frame := range frames {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				return
			}
		}
		// Hold the connection until the client tears it down.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func TestMoonshineClientStreamsPartialThenFinal(t *testing.T) {
	results := make(chan TranscriptResult, 8)

	url := newFakeMoonshineSTT(t,
		`{"type":"SpeechStarted","channel":[0,1],"timestamp":0.1}`,
		`{"type":"Results","is_final":false,"speech_final":false,`+
			`"channel":{"alternatives":[{"transcript":"turn on"}]}}`,
		`{"type":"Results","is_final":true,"speech_final":true,`+
			`"channel":{"alternatives":[{"transcript":"turn on the lights"}]}}`,
	)

	client, err := NewMoonshineClient(context.Background(), url, func(r TranscriptResult) {
		results <- r
	})
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	defer client.Close()

	if err := client.SendAudio(make([]byte, 640)); err != nil {
		t.Fatalf("SendAudio failed: %v", err)
	}

	partial := waitResult(t, results)
	if partial.IsFinal || partial.Text != "turn on" {
		t.Errorf("first result = %+v, want the partial %q", partial, "turn on")
	}

	final := waitResult(t, results)
	if !final.IsFinal || final.Text != "turn on the lights" {
		t.Errorf("second result = %+v, want the final %q", final, "turn on the lights")
	}
}

func TestMoonshineClientSendAfterCloseFails(t *testing.T) {
	url := newFakeMoonshineSTT(t)

	client, err := NewMoonshineClient(context.Background(), url, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}

	client.Close()
	client.Close() // idempotent: the pipeline can tear down twice on hangup

	if err := client.SendAudio(make([]byte, 640)); err == nil {
		t.Fatal("SendAudio succeeded after Close, want an error")
	}
}

func TestMoonshineClientDialFailureIsReported(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Port 1 on loopback refuses immediately rather than hanging.
	if _, err := NewMoonshineClient(ctx, "ws://127.0.0.1:1", func(TranscriptResult) {}); err == nil {
		t.Fatal("dial to a dead port succeeded, want an error")
	}
}
