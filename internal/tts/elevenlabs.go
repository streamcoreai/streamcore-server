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
		httpClient: &http.Client{},
	}
}

type elevenlabsRequest struct {
	Text          string                   `json:"text"`
	ModelID       string                   `json:"model_id"`
	VoiceSettings elevenlabsVoiceSettings  `json:"voice_settings"`
}

type elevenlabsVoiceSettings struct {
	Stability       float64 `json:"stability"`
	SimilarityBoost float64 `json:"similarity_boost"`
}

func (c *elevenlabsClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	body := elevenlabsRequest{
		Text:    text,
		ModelID: c.model,
		VoiceSettings: elevenlabsVoiceSettings{
			Stability:       0.5,
			SimilarityBoost: 0.75,
		},
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
