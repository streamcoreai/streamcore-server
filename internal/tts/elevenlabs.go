package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
)

const (
	elevenlabsAPIURL = "https://api.elevenlabs.io/v1/text-to-speech"
	// Rachel — clear, natural voice (ElevenLabs default)
	defaultElevenLabsVoiceID = "21m00Tcm4TlvDq8ikWAM"
	defaultElevenLabsModel   = "eleven_turbo_v2_5"
)

type elevenlabsClient struct {
	apiKey     string
	voiceID    string
	model      string
	httpClient *http.Client
}

// NewElevenLabsClient creates an ElevenLabs TTS client.
// voiceID defaults to Rachel if empty. model defaults to eleven_turbo_v2_5.
func NewElevenLabsClient(apiKey, voiceID, model string) Client {
	if voiceID == "" {
		voiceID = defaultElevenLabsVoiceID
	}
	if model == "" {
		model = defaultElevenLabsModel
	}
	return &elevenlabsClient{
		apiKey:     apiKey,
		voiceID:    voiceID,
		model:      model,
		httpClient: newPooledHTTPClient(),
	}
}

type elevenlabsRequest struct {
	Text          string                  `json:"text"`
	ModelID       string                  `json:"model_id"`
	VoiceSettings elevenlabsVoiceSettings `json:"voice_settings"`
}

type elevenlabsVoiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
}

// buildRequest composes the synthesis request. vc carries optional per-utterance
// delivery hints; the zero value leaves the voice at its configured defaults.
func (c *elevenlabsClient) buildRequest(ctx context.Context, text string, vc VoiceControls) (*http.Request, error) {
	settings := elevenlabsVoiceSettings{
		Stability:       0.5,
		SimilarityBoost: 0.75,
	}
	// ElevenLabs has no speed parameter on this endpoint, so tone is mapped to
	// stability instead: calmer tones get a steadier read, livelier ones more
	// variation. Clamped to the API's valid range.
	if vc.Speed > 0 {
		settings.Stability = math.Min(0.9, math.Max(0.15, 0.5+(1.0-vc.Speed)))
	}

	body := elevenlabsRequest{
		Text:          text,
		ModelID:       c.model,
		VoiceSettings: settings,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs marshal request: %w", err)
	}

	u := fmt.Sprintf("%s/%s?output_format=pcm_16000", elevenlabsAPIURL, c.voiceID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs create request: %w", err)
	}

	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

func (c *elevenlabsClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return c.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

func (c *elevenlabsClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text, vc)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("elevenlabs error %d: %s", resp.StatusCode, string(respBody))
	}
	return streamHTTPResponse(ctx, resp), nil
}

func (c *elevenlabsClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text, VoiceControls{})
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("elevenlabs error %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs read response: %w", err)
	}

	// ElevenLabs pcm_16000 returns 16kHz mono PCM — matches our pipeline directly.

	log.Printf("[tts:elevenlabs] synthesized %d bytes for %d chars of text", len(data), len(text))
	return data, nil
}
