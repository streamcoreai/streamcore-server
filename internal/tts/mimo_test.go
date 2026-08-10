package tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeMiMo stands in for the MiMo endpoint. It records the request it was
// given and replies with whatever the test scripted, so the adapter can be
// exercised without network access or an API key.
type fakeMiMo struct {
	server *httptest.Server

	// gotBody and gotHeader capture the last request for assertions.
	gotBody   map[string]any
	gotHeader http.Header
	gotPath   string

	// status and reply are what the handler sends back.
	status int
	reply  string
}

func newFakeMiMo(t *testing.T, reply string) *fakeMiMo {
	t.Helper()
	f := &fakeMiMo{status: http.StatusOK, reply: reply}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.gotBody = map[string]any{}
		json.Unmarshal(body, &f.gotBody)
		f.gotHeader = r.Header.Clone()
		f.gotPath = r.URL.Path

		w.WriteHeader(f.status)
		io.WriteString(w, f.reply)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMiMo) client(voice string) Client {
	return NewMiMoClient("test-key", voice, "", f.server.URL)
}

// pcm24kBytes renders n samples of silence-adjacent PCM so decoded audio has a
// predictable length without the tests caring about its content.
func pcm24kBytes(n int) []byte {
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		out[2*i] = byte(i)
		out[2*i+1] = 0
	}
	return out
}

func oneShotReply(pcm []byte) string {
	return fmt.Sprintf(`{"choices":[{"message":{"audio":{"data":%q}}}]}`,
		base64.StdEncoding.EncodeToString(pcm))
}

func sseEvent(pcm []byte) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"audio\":{\"data\":%q}}}]}\n\n",
		base64.StdEncoding.EncodeToString(pcm))
}

func drain(t *testing.T, ch <-chan StreamChunk) ([]byte, error) {
	t.Helper()
	var pcm []byte
	for chunk := range ch {
		if chunk.Err != nil {
			return pcm, chunk.Err
		}
		pcm = append(pcm, chunk.PCM...)
	}
	return pcm, nil
}

// The text to speak goes in an assistant message. Sending it as a user message
// asks the model to reply to the text instead of reading it aloud, which comes
// back as audio of the wrong words rather than as an error.
func TestMiMoSendsTextAsAssistantMessage(t *testing.T) {
	f := newFakeMiMo(t, oneShotReply(pcm24kBytes(2400)))

	if _, err := f.client("茉莉").Synthesize(context.Background(), "你好"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	msgs, ok := f.gotBody["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Fatalf("got messages %v, want exactly one", f.gotBody["messages"])
	}
	m := msgs[0].(map[string]any)
	if m["role"] != "assistant" {
		t.Errorf("role = %v, want assistant", m["role"])
	}
	if m["content"] != "你好" {
		t.Errorf("content = %v, want 你好", m["content"])
	}
}

// MiMo authenticates with an api-key header. A bearer token — the shape every
// other provider here uses — is silently unauthenticated.
func TestMiMoAuthenticatesWithAPIKeyHeader(t *testing.T) {
	f := newFakeMiMo(t, oneShotReply(pcm24kBytes(2400)))

	if _, err := f.client("").Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	if got := f.gotHeader.Get("api-key"); got != "test-key" {
		t.Errorf("api-key header = %q, want test-key", got)
	}
	if got := f.gotHeader.Get("Authorization"); got != "" {
		t.Errorf("Authorization header = %q, want it unset", got)
	}
	if !strings.HasSuffix(f.gotPath, "/chat/completions") {
		t.Errorf("path = %q, want it to end in /chat/completions", f.gotPath)
	}
}

// Requesting pcm keeps the response headerless. Asking for wav would prepend a
// RIFF header that the pipeline would play as a burst of noise.
func TestMiMoRequestsHeaderlessPCM(t *testing.T) {
	f := newFakeMiMo(t, oneShotReply(pcm24kBytes(2400)))

	if _, err := f.client("茉莉").Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	audio, ok := f.gotBody["audio"].(map[string]any)
	if !ok {
		t.Fatalf("no audio setting in request: %v", f.gotBody)
	}
	if audio["format"] != "pcm" {
		t.Errorf("format = %v, want pcm", audio["format"])
	}
	if audio["voice"] != "茉莉" {
		t.Errorf("voice = %v, want 茉莉", audio["voice"])
	}
}

// The provider returns 24 kHz; the pipeline runs at 16 kHz. Handing its audio
// straight through would play every utterance 1.5x too slow.
func TestMiMoResamplesTo16k(t *testing.T) {
	const in24k = 2400 // 100 ms at 24 kHz
	f := newFakeMiMo(t, oneShotReply(pcm24kBytes(in24k)))

	pcm, err := f.client("").Synthesize(context.Background(), "hi")
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
func TestMiMoStreamMatchesOneShot(t *testing.T) {
	const total = 2400
	full := pcm24kBytes(total)

	oneShot := newFakeMiMo(t, oneShotReply(full))
	want, err := oneShot.client("").Synthesize(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Same audio, cut into four events plus the terminator.
	var sse strings.Builder
	for i := 0; i < 4; i++ {
		sse.WriteString(sseEvent(full[i*len(full)/4 : (i+1)*len(full)/4]))
	}
	sse.WriteString("data: [DONE]\n\n")

	streamed := newFakeMiMo(t, sse.String())
	ch, err := streamed.client("").SynthesizeStream(context.Background(), "hi")
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

// MiMo reports quota and auth failures in an error object. Ignoring it would
// turn "insufficient balance" into an utterance of silence, which reads
// downstream as the agent simply having nothing to say.
func TestMiMoSurfacesAPIErrorInStream(t *testing.T) {
	body := `data: {"error":{"code":"402","message":"Insufficient account balance","type":"insufficient_balance"}}` + "\n\n"
	f := newFakeMiMo(t, body)

	ch, err := f.client("").SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	if _, err := drain(t, ch); err == nil {
		t.Fatal("stream reported no error, want the 402 surfaced")
	} else if !strings.Contains(err.Error(), "Insufficient account balance") {
		t.Errorf("error = %v, want it to carry the provider message", err)
	}
}

func TestMiMoSurfacesAPIErrorOneShot(t *testing.T) {
	f := newFakeMiMo(t, `{"error":{"code":"402","message":"Insufficient account balance"}}`)

	if _, err := f.client("").Synthesize(context.Background(), "hi"); err == nil {
		t.Fatal("Synthesize reported no error, want the 402 surfaced")
	}
}

// A non-200 has to carry the body: the status alone does not say whether the
// model name was wrong, the key was rejected, or the account is out of credit.
func TestMiMoNon200IncludesBody(t *testing.T) {
	f := newFakeMiMo(t, `{"error":{"code":"400","message":"Unsupported model mimo-v2-tts"}}`)
	f.status = http.StatusBadRequest

	_, err := f.client("").Synthesize(context.Background(), "hi")
	if err == nil {
		t.Fatal("Synthesize reported no error, want the 400 surfaced")
	}
	if !strings.Contains(err.Error(), "Unsupported model") {
		t.Errorf("error = %v, want it to include the response body", err)
	}
}

// One malformed event must not kill the utterance — the stream recovers on the
// next one, so the caller hears a gap rather than silence.
func TestMiMoSkipsMalformedEvent(t *testing.T) {
	pcm := pcm24kBytes(1200)
	body := "data: {not json\n\n" + sseEvent(pcm) + "data: [DONE]\n\n"
	f := newFakeMiMo(t, body)

	ch, err := f.client("").SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	got, err := drain(t, ch)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("got no audio, want the event after the malformed one to survive")
	}
}

// Events after [DONE] are not part of the utterance. Reading past it would
// append whatever the connection carried next to the audio just played.
func TestMiMoStopsAtDone(t *testing.T) {
	pcm := pcm24kBytes(1200)
	body := sseEvent(pcm) + "data: [DONE]\n\n" + sseEvent(pcm24kBytes(9600))
	f := newFakeMiMo(t, body)

	ch, err := f.client("").SynthesizeStream(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	got, err := drain(t, ch)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	want := 1200 * 2 / 3
	if samples := len(got) / 2; samples != want {
		t.Errorf("got %d samples, want %d — audio after [DONE] should be ignored", samples, want)
	}
}

// Cancellation has to stop the stream: on barge-in the pipeline cancels the
// context and expects the channel to close rather than keep producing audio
// for a response the caller already interrupted.
func TestMiMoStreamStopsOnCancel(t *testing.T) {
	var sse strings.Builder
	for i := 0; i < 8; i++ {
		sse.WriteString(sseEvent(pcm24kBytes(2400)))
	}
	sse.WriteString("data: [DONE]\n\n")
	f := newFakeMiMo(t, sse.String())

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := f.client("").SynthesizeStream(ctx, "hi")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	cancel()

	// Draining must terminate; without the ctx check it would block or run on.
	for range ch {
	}
}

// An empty voice falls back to the house voice rather than sending "" and
// having the provider reject the request.
func TestMiMoDefaultsVoiceAndModel(t *testing.T) {
	f := newFakeMiMo(t, oneShotReply(pcm24kBytes(2400)))

	if _, err := f.client("").Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	audio := f.gotBody["audio"].(map[string]any)
	if audio["voice"] != defaultMiMoVoice {
		t.Errorf("voice = %v, want %v", audio["voice"], defaultMiMoVoice)
	}
	if f.gotBody["model"] != defaultMiMoModel {
		t.Errorf("model = %v, want %v", f.gotBody["model"], defaultMiMoModel)
	}
}
