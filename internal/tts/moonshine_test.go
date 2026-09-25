package tts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeMoonshineTTS records what the client asked for and replies with body.
type fakeMoonshineTTS struct {
	server *httptest.Server
	path   string
	query  url.Values
	body   string
	header string
}

func newFakeMoonshineTTS(t *testing.T, handler http.HandlerFunc) *fakeMoonshineTTS {
	t.Helper()

	f := &fakeMoonshineTTS{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.path = r.URL.Path
		f.query = r.URL.Query()
		f.body = string(body)
		f.header = r.Header.Get("Content-Type")
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func writePCM(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte{0x01, 0x02, 0x03, 0x04})
}

// The sidecar mirrors Deepgram's /v1/speak, so the request has to be the
// shape that endpoint takes: text in the body, audio format in the query.
func TestMoonshineRequestMatchesDeepgramSpeak(t *testing.T) {
	f := newFakeMoonshineTTS(t, writePCM)

	client := NewMoonshineClient(f.server.URL, "kokoro_af_bella")
	if _, err := client.Synthesize(context.Background(), "hello there"); err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	if f.path != "/v1/speak" {
		t.Errorf("path = %q, want /v1/speak", f.path)
	}
	if f.body != "hello there" {
		t.Errorf("body = %q, want the raw text", f.body)
	}
	if f.header != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", f.header)
	}
	for param, want := range map[string]string{
		"voice":       "kokoro_af_bella",
		"encoding":    "linear16",
		"sample_rate": "16000",
		"container":   "none",
	} {
		if got := f.query.Get(param); got != want {
			t.Errorf("%s = %q, want %q", param, got, want)
		}
	}
}

func TestMoonshineDefaults(t *testing.T) {
	f := newFakeMoonshineTTS(t, writePCM)

	// A trailing slash on the configured URL must not produce //v1/speak.
	client := NewMoonshineClient(f.server.URL+"/", "")
	if _, err := client.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	if f.path != "/v1/speak" {
		t.Errorf("path = %q, want /v1/speak", f.path)
	}
	if got := f.query.Get("voice"); got != defaultMoonshineVoice {
		t.Errorf("voice = %q, want the default %q", got, defaultMoonshineVoice)
	}
}

// Playback starts on the first chunk, so the client must hand bytes over as
// they arrive rather than after the sidecar has finished the sentence.
func TestMoonshineStreamsChunksAsTheyArrive(t *testing.T) {
	released := make(chan struct{})

	f := newFakeMoonshineTTS(t, func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server cannot flush")
			return
		}
		w.Write([]byte{0x01, 0x02})
		flusher.Flush()
		<-released
		w.Write([]byte{0x03, 0x04})
		flusher.Flush()
	})

	client := NewMoonshineClient(f.server.URL, "")
	ch, err := client.SynthesizeStream(context.Background(), "two chunks")
	if err != nil {
		t.Fatalf("SynthesizeStream failed: %v", err)
	}

	first := waitChunk(t, ch)
	if string(first.PCM) != "\x01\x02" {
		t.Errorf("first chunk = % x, want 01 02", first.PCM)
	}

	close(released)

	second := waitChunk(t, ch)
	if string(second.PCM) != "\x03\x04" {
		t.Errorf("second chunk = % x, want 03 04", second.PCM)
	}
}

// A sidecar that has not downloaded the voice answers 400 with the reason.
// Swallowing it would leave the caller with silence and no explanation.
func TestMoonshineSurfacesErrorBody(t *testing.T) {
	f := newFakeMoonshineTTS(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"unknown voice 'nope'"}`, http.StatusBadRequest)
	})

	client := NewMoonshineClient(f.server.URL, "nope")

	_, err := client.Synthesize(context.Background(), "hi")
	if err == nil {
		t.Fatal("Synthesize succeeded on a 400, want an error")
	}
	if !strings.Contains(err.Error(), "unknown voice") {
		t.Errorf("error = %v, want the sidecar's reason in it", err)
	}

	if _, err := client.SynthesizeStream(context.Background(), "hi"); err == nil {
		t.Fatal("SynthesizeStream succeeded on a 400, want an error")
	}
}

func waitChunk(t *testing.T, ch <-chan StreamChunk) StreamChunk {
	t.Helper()
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before a chunk arrived")
		}
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		return chunk
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a chunk")
		return StreamChunk{}
	}
}
