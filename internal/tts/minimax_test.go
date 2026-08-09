package tts

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

// collect runs pumpSSE over a canned event stream and returns the audio it
// emitted plus the first error it reported.
func collect(t *testing.T, ctx context.Context, stream string) ([]byte, error) {
	t.Helper()

	c := &minimaxClient{}
	ch := make(chan StreamChunk, 16)
	go func() {
		defer close(ch)
		c.pumpSSE(ctx, strings.NewReader(stream), ch)
	}()

	var pcm []byte
	for chunk := range ch {
		if chunk.Err != nil {
			return pcm, chunk.Err
		}
		if len(chunk.PCM)%2 != 0 {
			t.Errorf("emitted odd-length chunk of %d bytes", len(chunk.PCM))
		}
		pcm = append(pcm, chunk.PCM...)
	}
	return pcm, nil
}

func event(audio string, status int) string {
	return `data: {"data":{"audio":"` + audio + `","status":` + strconv.Itoa(status) + `},"base_resp":{"status_code":0,"status_msg":"success"}}` + "\n"
}

// The terminal event repeats the whole utterance. Emitting it would play
// everything twice, so only the incremental events count.
func TestPumpSSESkipsAggregatedTerminalEvent(t *testing.T) {
	stream := event("0102", 1) +
		event("0304", 1) +
		event("01020304", 2)

	got, err := collect(t, context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "01020304"; hex.EncodeToString(got) != want {
		t.Errorf("audio = %s, want %s", hex.EncodeToString(got), want)
	}
}

// MiniMax reports auth and quota failures with HTTP 200 and a non-zero
// base_resp.status_code — the trap this provider has to catch, or a failed
// call becomes silent audio.
func TestPumpSSESurfacesBaseRespError(t *testing.T) {
	stream := `data: {"data":{"audio":"","status":1},"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}` + "\n"

	_, err := collect(t, context.Background(), stream)
	if err == nil {
		t.Fatal("expected an error for a non-zero base_resp status_code")
	}
	if !strings.Contains(err.Error(), "1008") || !strings.Contains(err.Error(), "insufficient balance") {
		t.Errorf("error = %v, want it to carry the provider code and message", err)
	}
}

// One bad event must not kill a live utterance.
func TestPumpSSESkipsMalformedEvent(t *testing.T) {
	stream := event("0102", 1) +
		"data: {not json\n" +
		event("0304", 1) +
		event("", 2)

	got, err := collect(t, context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "01020304"; hex.EncodeToString(got) != want {
		t.Errorf("audio = %s, want %s", hex.EncodeToString(got), want)
	}
}

// Events carrying an odd number of bytes must not lose the boundary sample:
// the trailing byte belongs to a sample the next event completes.
func TestPumpSSEAlignsAcrossOddEvents(t *testing.T) {
	stream := event("010203", 1) +
		event("040506", 1) +
		event("", 2)

	got, err := collect(t, context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "010203040506"; hex.EncodeToString(got) != want {
		t.Errorf("audio = %s, want %s — a boundary sample was dropped", hex.EncodeToString(got), want)
	}
}

// Non-audio SSE framing (comments, blank lines, [DONE]) is ignored.
func TestPumpSSEIgnoresNonAudioFraming(t *testing.T) {
	stream := "\n" +
		": keep-alive\n" +
		event("0102", 1) +
		"data: [DONE]\n" +
		"\n"

	got, err := collect(t, context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "0102"; hex.EncodeToString(got) != want {
		t.Errorf("audio = %s, want %s", hex.EncodeToString(got), want)
	}
}

// A stream that ends without audio must close the channel rather than hang or
// report a phantom error; the caller logs the silence.
func TestPumpSSESilentStreamClosesCleanly(t *testing.T) {
	got, err := collect(t, context.Background(), event("", 2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("audio = %d bytes, want none", len(got))
	}
}

// Bad hex is a hard failure: the utterance cannot be recovered from it.
func TestPumpSSEReportsBadHex(t *testing.T) {
	_, err := collect(t, context.Background(), event("zzzz", 1))
	if err == nil {
		t.Fatal("expected an error for undecodable hex audio")
	}
	if !strings.Contains(err.Error(), "decode audio") {
		t.Errorf("error = %v, want it to name the decode step", err)
	}
}

func TestPumpSSEStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := collect(t, ctx, event("0102", 1)+event("", 2))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("emitted %d bytes after cancellation, want none", len(got))
	}
}

func TestMiniMaxClientDefaults(t *testing.T) {
	c := NewMiniMaxClient("key", "", "", "").(*minimaxClient)
	if c.voiceID != defaultMiniMaxVoiceID || c.model != defaultMiniMaxModel || c.baseURL != defaultMiniMaxBaseURL {
		t.Errorf("defaults not applied: voice=%q model=%q base=%q", c.voiceID, c.model, c.baseURL)
	}

	// A trailing slash on a configured host must not produce a double slash
	// in the request path.
	c = NewMiniMaxClient("key", "v", "m", "https://api.minimaxi.com/v1/").(*minimaxClient)
	if c.baseURL != "https://api.minimaxi.com/v1" {
		t.Errorf("baseURL = %q, want the trailing slash trimmed", c.baseURL)
	}
}

// Delivery tags reach MiniMax as its emotion enum, and speed stays inside the
// range the API accepts.
func TestMiniMaxVoiceControls(t *testing.T) {
	c := NewMiniMaxClient("key", "", "", "").(*minimaxClient)

	for tag, want := range map[string]string{
		"warm":       "happy",
		"excited":    "happy",
		"calm":       "calm",
		"empathetic": "calm",
	} {
		if got := minimaxEmotions[tag]; got != want {
			t.Errorf("tag %q maps to %q, want %q", tag, got, want)
		}
	}

	// Tags with no emotion still act through speed.
	if _, mapped := minimaxEmotions["slow"]; mapped {
		t.Error("slow should have no emotion mapping")
	}

	// An out-of-range speed must be clamped rather than sent through to a 400
	// mid-call, and the tone must arrive as MiniMax's emotion enum.
	req, err := c.buildRequest(context.Background(), "hello", true, VoiceControls{Speed: 9, Tone: "warm"})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if !strings.HasSuffix(req.URL.Path, "/t2a_v2") {
		t.Errorf("path = %q, want it to end in /t2a_v2", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer key" {
		t.Errorf("Authorization = %q", got)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var sent minimaxRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if sent.VoiceSetting.Speed != 2.0 {
		t.Errorf("speed = %v, want it clamped to 2.0", sent.VoiceSetting.Speed)
	}
	if sent.VoiceSetting.Emotion != "happy" {
		t.Errorf("emotion = %q, want happy", sent.VoiceSetting.Emotion)
	}
	if !sent.Stream || sent.OutputFormat != "hex" {
		t.Errorf("stream=%v output_format=%q, want true/hex", sent.Stream, sent.OutputFormat)
	}
	// 16 kHz mono is what keeps this provider off the resampler.
	if sent.AudioSetting.SampleRate != minimaxSampleRate || sent.AudioSetting.Channel != 1 {
		t.Errorf("audio_setting = %+v, want 16 kHz mono", sent.AudioSetting)
	}

	// An untagged sentence must not pin an emotion or speed on the voice.
	req, err = c.buildRequest(context.Background(), "hello", true, VoiceControls{})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	body, _ = io.ReadAll(req.Body)
	sent = minimaxRequest{}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if sent.VoiceSetting.Speed != 0 || sent.VoiceSetting.Emotion != "" {
		t.Errorf("zero controls produced %+v, want provider defaults", sent.VoiceSetting)
	}
}
