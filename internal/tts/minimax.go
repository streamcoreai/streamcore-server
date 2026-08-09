package tts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	// defaultMiniMaxBaseURL is the global endpoint. Deployments in mainland
	// China use https://api.minimaxi.com/v1 instead, and api-uw.minimax.io
	// trades region for a lower time-to-first-audio; both are reachable by
	// setting base_url.
	defaultMiniMaxBaseURL = "https://api.minimax.io/v1"
	// speech-2.6-turbo is the low-latency tier — the right default on a live
	// audio path. The -hd models sound better but add hundreds of ms.
	defaultMiniMaxModel   = "speech-2.6-turbo"
	defaultMiniMaxVoiceID = "English_Graceful_Lady"

	// MiniMax emits PCM at whatever rate we ask for, and 16 kHz mono is
	// exactly what the pipeline runs at — so unlike the 24 kHz-only
	// providers there is no resampling stage here.
	minimaxSampleRate = 16000

	// maxMiniMaxEventBytes caps a single SSE line. The terminal event repeats
	// the whole utterance as hex, so the ceiling has to cover it: at 16 kHz
	// s16 mono that is 64 KB of line per second of audio, making this roughly
	// two minutes — far beyond the sentence-sized chunks the pipeline sends.
	maxMiniMaxEventBytes = 8 * 1024 * 1024
)

type minimaxClient struct {
	apiKey     string
	voiceID    string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewMiniMaxClient creates a MiniMax T2A v2 TTS client. Empty voiceID, model
// and baseURL fall back to the package defaults.
func NewMiniMaxClient(apiKey, voiceID, model, baseURL string) Client {
	if voiceID == "" {
		voiceID = defaultMiniMaxVoiceID
	}
	if model == "" {
		model = defaultMiniMaxModel
	}
	if baseURL == "" {
		baseURL = defaultMiniMaxBaseURL
	}
	return &minimaxClient{
		apiKey:     apiKey,
		voiceID:    voiceID,
		model:      model,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: newPooledHTTPClient(),
	}
}

type minimaxVoiceSetting struct {
	VoiceID string  `json:"voice_id"`
	Speed   float64 `json:"speed,omitempty"`
	Emotion string  `json:"emotion,omitempty"`
}

type minimaxAudioSetting struct {
	SampleRate int    `json:"sample_rate"`
	Format     string `json:"format"`
	Channel    int    `json:"channel"`
}

type minimaxRequest struct {
	Model        string              `json:"model"`
	Text         string              `json:"text"`
	Stream       bool                `json:"stream"`
	OutputFormat string              `json:"output_format"`
	VoiceSetting minimaxVoiceSetting `json:"voice_setting"`
	AudioSetting minimaxAudioSetting `json:"audio_setting"`
}

// minimaxResponse covers both the one-shot body and a single streamed event —
// they share a shape, differing only in data.status.
type minimaxResponse struct {
	Data struct {
		// Audio is hex-encoded, not base64: two ASCII characters per byte.
		Audio string `json:"audio"`
		// Status is 1 for an incremental chunk and 2 for the final event.
		Status int `json:"status"`
	} `json:"data"`
	BaseResp struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// err returns the provider-level error carried in base_resp, if any. MiniMax
// reports failures with HTTP 200 and a non-zero status_code, so checking the
// HTTP status alone silently turns an auth or quota failure into empty audio.
func (r *minimaxResponse) err() error {
	if r.BaseResp.StatusCode != 0 {
		return fmt.Errorf("minimax error %d: %s", r.BaseResp.StatusCode, r.BaseResp.StatusMsg)
	}
	return nil
}

// minimaxEmotions maps the pipeline's delivery tags onto MiniMax's emotion
// enum (happy, sad, angry, fearful, disgusted, surprised, calm, fluent,
// neutral). The mapping is deliberately conservative: "empathetic" marks
// apologies and bad news, where MiniMax's "sad" overshoots into sounding
// upset itself, so it lands on "calm" alongside the calm tag. Tags absent
// here still take effect through Speed.
var minimaxEmotions = map[string]string{
	"warm":       "happy",
	"excited":    "happy",
	"calm":       "calm",
	"empathetic": "calm",
}

func (c *minimaxClient) buildRequest(ctx context.Context, text string, stream bool, vc VoiceControls) (*http.Request, error) {
	voice := minimaxVoiceSetting{VoiceID: c.voiceID}
	if vc.Speed > 0 {
		// MiniMax accepts 0.5-2.0; the tag presets sit well inside that, but
		// clamp so a future preset cannot produce a 400 mid-call.
		voice.Speed = clampFloat(vc.Speed, 0.5, 2.0)
	}
	if e, ok := minimaxEmotions[vc.Tone]; ok {
		voice.Emotion = e
	}

	body := minimaxRequest{
		Model:        c.model,
		Text:         text,
		Stream:       stream,
		OutputFormat: "hex",
		VoiceSetting: voice,
		AudioSetting: minimaxAudioSetting{
			SampleRate: minimaxSampleRate,
			Format:     "pcm",
			Channel:    1,
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("minimax marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/t2a_v2", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("minimax create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *minimaxClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text, false, VoiceControls{})
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax request: %w", err)
	}
	// Registered before the drain so it runs after it (defers are LIFO).
	defer resp.Body.Close()
	defer drainForReuse(ctx, resp.Body)

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("minimax error %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed minimaxResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("minimax parse response: %w", err)
	}
	if err := parsed.err(); err != nil {
		return nil, err
	}
	// A 200 with base_resp.status_code 0 and no audio should not read as a
	// successful synthesis; returning empty PCM here would surface as an
	// unexplained silent utterance rather than a failure the caller can log.
	if parsed.Data.Audio == "" {
		return nil, fmt.Errorf("minimax: response contained no audio")
	}

	pcm, err := hex.DecodeString(parsed.Data.Audio)
	if err != nil {
		return nil, fmt.Errorf("minimax decode audio: %w", err)
	}

	log.Printf("[tts:minimax] synthesized %d bytes for %d chars of text", len(pcm), len(text))
	return pcm, nil
}

func (c *minimaxClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return c.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

// SynthesizeStreamWithControls is SynthesizeStream with per-utterance voice
// controls applied to voice_setting.
func (c *minimaxClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text, true, vc)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("minimax request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("minimax error %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamChunk, 8)
	go func() {
		// Defers run LIFO, and the order matters on the audio path: close ch
		// first so the agent moves on to the next sentence immediately, then
		// tidy the connection behind it. pumpSSE stops at the terminal event
		// rather than at EOF, so without the drain every sentence would
		// abandon its connection and pay a fresh TLS handshake on the next.
		defer resp.Body.Close()
		defer drainForReuse(ctx, resp.Body)
		defer close(ch)
		c.pumpSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// pumpSSE decodes MiniMax's text/event-stream into PCM chunks.
//
// Unlike the providers that stream raw PCM straight down the response body,
// each event here is a JSON envelope whose audio is hex-encoded, so the
// generic pumpPCMStream cannot be reused — but the int16 alignment it does
// across chunk boundaries still applies, via the shared pcmAligner.
func (c *minimaxClient) pumpSSE(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(body)
	// A single event carries a whole synthesized segment as hex (two bytes of
	// line per byte of audio), which overruns the 64 KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), maxMiniMaxEventBytes)

	var (
		align   pcmAligner
		emitted int
	)

	emit := func(chunk StreamChunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// warnIfSilent fires when a stream ends without ever yielding audio. The
	// caller has no other signal — no error was raised and the channel simply
	// closes — so the sentence would play as an unexplained gap.
	warnIfSilent := func() {
		if emitted == 0 && ctx.Err() == nil {
			log.Printf("[tts:minimax] stream ended with no audio; sentence will be silent")
		}
	}

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var ev minimaxResponse
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			// A malformed event is not worth killing a live utterance over;
			// the stream usually recovers on the next one.
			log.Printf("[tts:minimax] skipping unparsable event: %v", err)
			continue
		}
		if err := ev.err(); err != nil {
			emit(StreamChunk{Err: err})
			return
		}

		// Status 2 is the terminal event. It repeats the whole utterance as
		// one aggregated blob, so emitting its audio would play everything a
		// second time on top of the chunks already sent. Nothing follows it,
		// so the body is left to drainForReuse rather than read further.
		if ev.Data.Status == 2 {
			warnIfSilent()
			return
		}
		if ev.Data.Audio == "" {
			continue
		}

		pcm, err := hex.DecodeString(ev.Data.Audio)
		if err != nil {
			emit(StreamChunk{Err: fmt.Errorf("minimax decode audio: %w", err)})
			return
		}
		aligned := align.take(pcm)
		if len(aligned) == 0 {
			continue
		}
		emitted += len(aligned)
		if !emit(StreamChunk{PCM: aligned}) {
			return
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		emit(StreamChunk{Err: fmt.Errorf("minimax stream read: %w", err)})
		return
	}
	warnIfSilent()
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
