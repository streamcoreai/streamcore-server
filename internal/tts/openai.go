package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	defaultOpenAITTSBaseURL = "https://api.openai.com/v1"
	// defaultOpenAITTSModel is the current-generation low-cost speech model;
	// tts-1 and tts-1-hd remain available for the older quality/latency
	// trade-off.
	defaultOpenAITTSModel = "gpt-4o-mini-tts"
	// defaultOpenAITTSVoice is the same default the API applies when no
	// voice is sent; the full catalogue (ash, ballad, coral, echo, fable,
	// nova, onyx, sage, shimmer, verse, marin, cedar) is at
	// https://platform.openai.com/docs/guides/text-to-speech.
	defaultOpenAITTSVoice = "alloy"
)

type openaiClient struct {
	apiKey     string
	voice      string
	model      string
	baseURL    string
	httpClient *http.Client
}

// NewOpenAIClient creates an OpenAI TTS client. Empty voice, model and
// baseURL fall back to the package defaults; a non-empty baseURL targets
// any endpoint speaking OpenAI's protocol.
func NewOpenAIClient(apiKey, voice, model, baseURL string) Client {
	if voice == "" {
		voice = defaultOpenAITTSVoice
	}
	if model == "" {
		model = defaultOpenAITTSModel
	}
	if baseURL == "" {
		baseURL = defaultOpenAITTSBaseURL
	}
	return &openaiClient{
		apiKey:     apiKey,
		voice:      voice,
		model:      model,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: newPooledHTTPClient(),
	}
}

// openaiRequest is the /audio/speech body. The endpoint always streams the
// answer as chunked raw audio — there is no streaming flag — so one request
// shape serves both the one-shot and the streaming path.
type openaiRequest struct {
	Model          string `json:"model"`
	Voice          string `json:"voice"`
	Input          string `json:"input"`
	ResponseFormat string `json:"response_format"`
}

func (c *openaiClient) buildRequest(ctx context.Context, text string) (*http.Request, error) {
	// "pcm" returns headerless little-endian linear16, always at 24 kHz —
	// the API offers no sample-rate parameter — so the conversion to the
	// pipeline's 16 kHz happens here rather than server-side.
	body := openaiRequest{
		Model:          c.model,
		Voice:          c.voice,
		Input:          text,
		ResponseFormat: "pcm",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openai marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/audio/speech", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("openai create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// SynthesizeStream reads the response body as it arrives. OpenAI returns
// chunked raw PCM, so bytes are playable the moment they land — once
// resampled from the wire's 24 kHz to the pipeline's 16 kHz.
func (c *openaiClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("openai error %d: %s", resp.StatusCode, string(respBody))
	}
	return streamResampledHTTPResponse(ctx, resp), nil
}

func (c *openaiClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("openai error %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai read response: %w", err)
	}

	pcm := resample24kTo16k(data)
	log.Printf("[tts:openai] synthesized %d bytes for %d chars of text", len(pcm), len(text))
	return pcm, nil
}
