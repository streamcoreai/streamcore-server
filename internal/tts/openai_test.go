package tts

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOpenAI stands in for the OpenAI /audio/speech endpoint. It records the
// request it was given and replies with whatever the test scripted, so the
// adapter can be exercised without network access or an API key.
type fakeOpenAI struct {
	server *httptest.Server

	// gotBody and gotHeader capture the last request for assertions.
	gotBody   map[string]any
	gotHeader http.Header
	gotPath   string

	// status is what the handler sends back; parts are the audio fragments
	// written one at a time (a single fragment is the one-shot case).
	status int
	parts  [][]byte
}

func newFakeOpenAI(t *testing.T, parts ...[]byte) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{status: http.StatusOK, parts: parts}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.gotBody = map[string]any{}
		json.Unmarshal(body, &f.gotBody)
		f.gotHeader = r.Header.Clone()
		f.gotPath = r.URL.Path

		if f.status != http.StatusOK {
			w.WriteHeader(f.status)
			io.WriteString(w, `{"error":{"message":"model tts-9 does not exist","type":"invalid_request_error"}}`)
			return
		}
		for _, part := range parts {
			w.Write(part)
			w.(http.Flusher).Flush()
			// Give the client a moment to see each fragment as its own
			// read, which is what makes "multiple chunks" observable.
			time.Sleep(10 * time.Millisecond)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenAI) client(voice, model string) Client {
	return NewOpenAIClient("test-key", voice, model, f.server.URL)
}

// The request shape is the contract: bearer auth, the speech endpoint, and
// pcm — asking for wav or mp3 would prepend a container the pipeline would
// play as a burst of noise.
func TestOpenAISendsSpeechRequest(t *testing.T) {
	f := newFakeOpenAI(t, pcm24kBytes(2400))

	if _, err := f.client("nova", "tts-1").Synthesize(context.Background(), "hello"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if got := f.gotHeader.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", got)
	}
	if !strings.HasSuffix(f.gotPath, "/audio/speech") {
		t.Errorf("path = %q, want it to end in /audio/speech", f.gotPath)
	}
	for k, want := range map[string]string{
		"model":           "tts-1",
		"voice":           "nova",
		"input":           "hello",
		"response_format": "pcm",
	} {
		if got := f.gotBody[k]; got != want {
			t.Errorf("body[%s] = %v, want %v", k, got, want)
		}
	}
}

// OpenAI's pcm format is fixed at 24 kHz; the pipeline runs at 16 kHz.
// Handing its audio straight through would play every utterance 1.5x too
// slow.
func TestOpenAIResamplesTo16k(t *testing.T) {
	const in24k = 2400 // 100 ms at 24 kHz
	f := newFakeOpenAI(t, pcm24kBytes(in24k))

	pcm, err := f.client("", "").Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	want := in24k * 2 / 3
	if got := len(pcm) / 2; got != want {
		t.Errorf("got %d samples, want %d (16 kHz for %d samples at 24 kHz)", got, want, in24k)
	}
}

// Streaming has to deliver the same audio as one shot — that is the whole
// premise of starting playback before synthesis finishes.
func TestOpenAIStreamMatchesOneShot(t *testing.T) {
	const total = 9600
	full := pcm24kBytes(total)

	oneShot := newFakeOpenAI(t, full)
	want, err := oneShot.client("", "").Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Same audio, delivered in four fragments.
	var parts [][]byte
	for i := 0; i < 4; i++ {
		parts = append(parts, full[i*len(full)/4:(i+1)*len(full)/4])
	}
	streamed := newFakeOpenAI(t, parts...)
	ch, err := streamed.client("", "").SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	got, err := drain(t, ch)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("streamed %d bytes, one-shot produced %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("streamed audio diverges from one-shot at byte %d", i)
		}
	}
}

// A long utterance must arrive as several chunks, not one buffered blob —
// chunked streaming is what lets playback start before synthesis finishes.
func TestOpenAIStreamEmitsIncrementally(t *testing.T) {
	const total = 9600 // 400 ms at 24 kHz
	full := pcm24kBytes(total)

	var parts [][]byte
	for i := 0; i < 4; i++ {
		parts = append(parts, full[i*len(full)/4:(i+1)*len(full)/4])
	}
	f := newFakeOpenAI(t, parts...)

	ch, err := f.client("", "").SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	chunks := 0
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("stream: %v", chunk.Err)
		}
		chunks++
	}
	if chunks < 2 {
		t.Errorf("got %d chunk, want several for a long utterance", chunks)
	}
}

// Cancellation has to stop the stream: on barge-in the pipeline cancels the
// context and expects the channel to close rather than keep producing audio
// for a response the caller already interrupted.
func TestOpenAIStreamStopsOnCancel(t *testing.T) {
	var parts [][]byte
	for i := 0; i < 8; i++ {
		parts = append(parts, pcm24kBytes(2400))
	}
	f := newFakeOpenAI(t, parts...)

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.client("", "").SynthesizeStream(ctx, "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	cancel()

	// Draining must terminate; without the ctx check it would block or run on.
	for range ch {
	}
}

// A non-200 has to carry the body: the status alone does not say whether the
// model name was wrong, the key was rejected, or the account is out of
// credit. Both entry points must surface it.
func TestOpenAINon200IncludesBody(t *testing.T) {
	f := newFakeOpenAI(t, nil)
	f.status = http.StatusBadRequest

	if _, err := f.client("", "tts-9").Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("Synthesize reported no error, want the 400 surfaced")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want it to include the response body", err)
	}

	if _, err := f.client("", "tts-9").SynthesizeStream(context.Background(), "hi"); err == nil {
		t.Fatal("SynthesizeStream reported no error, want the 400 surfaced")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("error = %v, want it to include the response body", err)
	}
}

// Empty voice and model fall back to the defaults rather than sending "" and
// having the provider reject the request.
func TestOpenAIDefaultsVoiceAndModel(t *testing.T) {
	f := newFakeOpenAI(t, pcm24kBytes(2400))

	if _, err := f.client("", "").Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if f.gotBody["voice"] != defaultOpenAITTSVoice {
		t.Errorf("voice = %v, want %v", f.gotBody["voice"], defaultOpenAITTSVoice)
	}
	if f.gotBody["model"] != defaultOpenAITTSModel {
		t.Errorf("model = %v, want %v", f.gotBody["model"], defaultOpenAITTSModel)
	}
}

// A trailing slash on base_url must not produce a doubled separator in the
// request path — most gateways answer /v1//audio/speech with a 404 rather
// than a redirect.
func TestOpenAIBaseURLTrimsTrailingSlash(t *testing.T) {
	f := newFakeOpenAI(t, pcm24kBytes(2400))

	c := NewOpenAIClient("test-key", "", "", f.server.URL+"/")
	if _, err := c.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if strings.Contains(f.gotPath, "//") {
		t.Errorf("path = %q, want no doubled separator", f.gotPath)
	}
}
