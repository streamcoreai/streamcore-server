package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
)

const (
	speechifyAPIURL = "https://api.speechify.ai/v1/audio/speech"
	// Geffen — Simba 3.2 curated voice (Speechify docs example)
	defaultSpeechifyVoiceID = "geffen_32"
	defaultSpeechifyModel   = "simba-3.2"
)

type speechifyClient struct {
	apiKey     string
	voiceID    string
	model      string
	httpClient *http.Client
}

// NewSpeechifyClient creates a Speechify Simba TTS client.
// voiceID defaults to Geffen if empty. model defaults to simba-3.2.
func NewSpeechifyClient(apiKey, voiceID, model string) Client {
	if voiceID == "" {
		voiceID = defaultSpeechifyVoiceID
	}
	if model == "" {
		model = defaultSpeechifyModel
	}
	return &speechifyClient{
		apiKey:     apiKey,
		voiceID:    voiceID,
		model:      model,
		httpClient: &http.Client{},
	}
}

type speechifyRequest struct {
	Input        string `json:"input"`
	VoiceID      string `json:"voice_id"`
	Model        string `json:"model"`
	AudioFormat  string `json:"audio_format"`
	OutputFormat string `json:"output_format"`
}

// speechifyResponse — unlike Cartesia/ElevenLabs, Speechify returns the audio
// base64-encoded inside a JSON envelope rather than as raw bytes.
type speechifyResponse struct {
	AudioData   string `json:"audio_data"`
	AudioFormat string `json:"audio_format"`
}

func (c *speechifyClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	// The API silently ignores pcm_16000 for the simba-3.x models and returns
	// their native 24kHz instead (only legacy simba-english honors it), so
	// request pcm_24000 — honored by every model — and downsample to 16kHz.
	body := speechifyRequest{
		Input:        text,
		VoiceID:      c.voiceID,
		Model:        c.model,
		AudioFormat:  "pcm",
		OutputFormat: "pcm_24000",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("speechify marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, speechifyAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("speechify create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("speechify request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("speechify error %d: %s", resp.StatusCode, string(respBody))
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("speechify read response: %w", err)
	}

	var parsed speechifyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("speechify parse response: %w", err)
	}

	data, err := base64.StdEncoding.DecodeString(parsed.AudioData)
	if err != nil {
		return nil, fmt.Errorf("speechify decode audio: %w", err)
	}

	pcm := downsample24kTo16k(data)

	log.Printf("[tts:speechify] synthesized %d bytes for %d chars of text", len(pcm), len(text))
	return pcm, nil
}

// downsample24kTo16k converts s16le mono PCM from 24kHz to 16kHz, emitting two
// samples per three: the first unchanged, the second the average of the
// remaining pair. Up to two trailing samples (<0.1ms) are dropped.
func downsample24kTo16k(in []byte) []byte {
	samples := len(in) / 2
	out := make([]byte, 0, (samples*2/3+1)*2)
	for i := 0; i+2 < samples; i += 3 {
		s0 := int16(uint16(in[2*i]) | uint16(in[2*i+1])<<8)
		s1 := int16(uint16(in[2*i+2]) | uint16(in[2*i+3])<<8)
		s2 := int16(uint16(in[2*i+4]) | uint16(in[2*i+5])<<8)
		mid := int16((int32(s1) + int32(s2)) / 2)
		out = append(out,
			byte(uint16(s0)), byte(uint16(s0)>>8),
			byte(uint16(mid)), byte(uint16(mid)>>8))
	}
	return out
}
