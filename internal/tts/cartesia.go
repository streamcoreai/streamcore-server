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
	cartesiaAPIURL     = "https://api.cartesia.ai/tts/bytes"
	cartesiaAPIVersion = "2025-04-16"
	// Katie - stable voice for voice agents (Cartesia docs)
	defaultCartesiaVoiceID = "f786b574-daa5-4673-aa0c-cbe3e8534c02"
)

type cartesiaClient struct {
	apiKey string
	voiceID string
	httpClient *http.Client
}

// NewCartesiaClient creates a Cartesia Sonic TTS client.
// voiceID defaults to Katie if empty.
func NewCartesiaClient(apiKey, voiceID string) Client {
	if voiceID == "" {
		voiceID = defaultCartesiaVoiceID
	}
	return &cartesiaClient{
		apiKey:     apiKey,
		voiceID:    voiceID,
		httpClient: &http.Client{},
	}
}

type cartesiaRequest struct {
	ModelID      string                 `json:"model_id"`
	Transcript   string                 `json:"transcript"`
	Voice        cartesiaVoice          `json:"voice"`
	OutputFormat cartesiaOutputFormat   `json:"output_format"`
}

type cartesiaVoice struct {
	Mode string `json:"mode"`
	ID   string `json:"id"`
}

type cartesiaOutputFormat struct {
	Container  string `json:"container"`
	Encoding   string `json:"encoding"`
	SampleRate int    `json:"sample_rate"`
}

func (c *cartesiaClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	body := cartesiaRequest{
		ModelID:    "sonic-3",
		Transcript:  text,
		Voice: cartesiaVoice{
			Mode: "id",
			ID:   c.voiceID,
		},
		OutputFormat: cartesiaOutputFormat{
			Container:  "raw",
			Encoding:   "pcm_s16le",
			SampleRate: 16000,
		},
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("cartesia marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cartesiaAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("cartesia create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cartesia-Version", cartesiaAPIVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cartesia request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cartesia error %d: %s", resp.StatusCode, string(respBody))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("cartesia read response: %w", err)
	}

	log.Printf("[tts:cartesia] synthesized %d bytes for %d chars of text", len(data), len(text))
	return data, nil
}
