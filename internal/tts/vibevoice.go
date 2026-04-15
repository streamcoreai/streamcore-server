package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

// vibevoiceTTSClient connects to the VibeVoice TTS HTTP server
// (external/vibeVoice/vibeVoiceTTS) and implements the TTS Client interface.
type vibevoiceTTSClient struct {
	baseURL    string
	voice      string
	httpClient *http.Client
}

// NewVibeVoiceClient creates a client that talks to the VibeVoice TTS server.
// baseURL is the HTTP address (e.g. http://127.0.0.1:8300).
// voice defaults to "en-Emma_woman" if empty.
func NewVibeVoiceClient(baseURL, voice string) Client {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8300"
	}
	if voice == "" {
		voice = "en-Emma_woman"
	}
	return &vibevoiceTTSClient{
		baseURL:    baseURL,
		voice:      voice,
		httpClient: &http.Client{},
	}
}

type vibevoiceSynthRequest struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

// Synthesize sends text to the VibeVoice TTS server and returns raw PCM
// (linear16, 16 kHz, mono) suitable for the pipeline.
func (c *vibevoiceTTSClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	body := vibevoiceSynthRequest{
		Text:  text,
		Voice: c.voice,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vibevoice tts marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/synthesize", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("vibevoice tts create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vibevoice tts request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("vibevoice tts error %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vibevoice tts read response: %w", err)
	}

	log.Printf("[tts:vibevoice] synthesized %d bytes for %d chars of text", len(data), len(text))
	return data, nil
}
