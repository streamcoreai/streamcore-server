package tts

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type deepgramClient struct {
	apiKey     string
	httpClient *http.Client
}

// NewDeepgramClient creates a Deepgram Aura TTS client.
func NewDeepgramClient(apiKey string) Client {
	return &deepgramClient{
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

func (c *deepgramClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	params := url.Values{}
	params.Set("model", "aura-asteria-en")
	params.Set("encoding", "linear16")
	params.Set("sample_rate", "16000")
	params.Set("container", "none")

	u := fmt.Sprintf("https://api.deepgram.com/v1/speak?%s", params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Token %s", c.apiKey))
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("tts error %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read tts response: %w", err)
	}

	log.Printf("[tts:deepgram] synthesized %d bytes for %d chars of text", len(data), len(text))
	return data, nil
}
