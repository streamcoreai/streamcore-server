package tts

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	defaultMiMoBaseURL = "https://api.xiaomimimo.com/v1"
	defaultMiMoModel   = "mimo-v2.5-tts"
	// mimo_default is the neutral house voice; the named Chinese voices
	// (茉莉, 冰糖, 苏打, 白桦 …) and the English ones (Mia, Chloe, Milo, Dean)
	// are selected by passing their name verbatim as voice_id.
	defaultMiMoVoice = "mimo_default"
)

type mimoClient struct {
	apiKey     string
	voice      string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewMiMoClient creates a Xiaomi MiMo TTS client. Empty voice, model and
// baseURL fall back to the package defaults.
func NewMiMoClient(apiKey, voice, model, baseURL string) Client {
	if voice == "" {
		voice = defaultMiMoVoice
	}
	if model == "" {
		model = defaultMiMoModel
	}
	if baseURL == "" {
		baseURL = defaultMiMoBaseURL
	}
	return &mimoClient{
		apiKey:     apiKey,
		voice:      voice,
		model:      model,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: newPooledHTTPClient(),
	}
}

type mimoAudioSetting struct {
	Format string `json:"format"`
	Voice  string `json:"voice"`
}

type mimoMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type mimoRequest struct {
	Model    string           `json:"model"`
	Messages []mimoMessage    `json:"messages"`
	Audio    mimoAudioSetting `json:"audio"`
	Stream   bool             `json:"stream"`
}

// mimoAudio is the audio payload, base64-encoded, shared by the one-shot
// message and the streaming delta.
type mimoAudio struct {
	Data string `json:"data"`
}

type mimoResponse struct {
	Choices []struct {
		Message struct {
			Audio mimoAudio `json:"audio"`
		} `json:"message"`
		Delta struct {
			Audio mimoAudio `json:"audio"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (r *mimoResponse) err() error {
	if r.Error != nil {
		return fmt.Errorf("mimo error %s: %s", r.Error.Code, r.Error.Message)
	}
	return nil
}

// buildRequest composes a synthesis call.
//
// Two things about this API are worth knowing before reading the body: it is
// shaped like a chat completion rather than a speech endpoint, and the text to
// speak goes in an *assistant* message — putting it in a user message asks the
// model to reply to the text instead of reading it. Authentication is an
// `api-key` header, not the usual bearer token.
func (c *mimoClient) buildRequest(ctx context.Context, text string, stream bool) (*http.Request, error) {
	// "pcm" returns headerless little-endian linear16. Requesting a sample
	// rate has no effect — the API ignores it and always returns 24 kHz (a
	// wav request for 16000 comes back stamped 24000), so the rate conversion
	// happens here rather than server-side.
	body := mimoRequest{
		Model:    c.model,
		Messages: []mimoMessage{{Role: "assistant", Content: text}},
		Audio:    mimoAudioSetting{Format: "pcm", Voice: c.voice},
		Stream:   stream,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("mimo marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("mimo create request: %w", err)
	}
	req.Header.Set("api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *mimoClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text, false)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mimo request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("mimo error %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed mimoResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("mimo parse response: %w", err)
	}
	if err := parsed.err(); err != nil {
		return nil, err
	}
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("mimo response carried no choices")
	}

	raw, err := base64.StdEncoding.DecodeString(parsed.Choices[0].Message.Audio.Data)
	if err != nil {
		return nil, fmt.Errorf("mimo decode audio: %w", err)
	}

	pcm := resample24kTo16k(raw)
	log.Printf("[tts:mimo] synthesized %d bytes for %d chars of text", len(pcm), len(text))
	return pcm, nil
}

func (c *mimoClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text, true)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mimo request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, fmt.Errorf("mimo error %d: %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamChunk, 8)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		c.pumpSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// pumpSSE decodes the event stream into 16 kHz PCM chunks.
//
// Each event carries a slice of the utterance, so the 24 kHz to 16 kHz
// conversion runs through a streaming resampler that keeps its filter state
// across events — resampling each event independently would zero-pad at every
// boundary and click once per chunk.
func (c *mimoClient) pumpSSE(ctx context.Context, body io.Reader, ch chan<- StreamChunk) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var rs streamResampler24to16

	emit := func(chunk StreamChunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
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
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}

		var ev mimoResponse
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			log.Printf("[tts:mimo] skipping unparsable event: %v", err)
			continue
		}
		if err := ev.err(); err != nil {
			emit(StreamChunk{Err: err})
			return
		}
		if len(ev.Choices) == 0 || ev.Choices[0].Delta.Audio.Data == "" {
			continue
		}

		raw, err := base64.StdEncoding.DecodeString(ev.Choices[0].Delta.Audio.Data)
		if err != nil {
			emit(StreamChunk{Err: fmt.Errorf("mimo decode audio: %w", err)})
			return
		}
		if out := rs.Write(raw); len(out) > 0 {
			if !emit(StreamChunk{PCM: out}) {
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		emit(StreamChunk{Err: fmt.Errorf("mimo stream read: %w", err)})
		return
	}

	// The tail the resampler was holding back for filter context.
	if out := rs.Flush(); len(out) > 0 {
		emit(StreamChunk{PCM: out})
	}
}
