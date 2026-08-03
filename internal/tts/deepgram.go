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

// defaultDeepgramTTSModel is Deepgram's recommended general-purpose Aura-2
// voice. Voices are named [family]-[voice]-[language]; see
// https://developers.deepgram.com/docs/tts-models for the full list.
const defaultDeepgramTTSModel = "aura-2-thalia-en"

type deepgramClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewDeepgramClient creates a Deepgram Aura TTS client. An empty model falls
// back to defaultDeepgramTTSModel.
func NewDeepgramClient(apiKey, model string) Client {
	if model == "" {
		model = defaultDeepgramTTSModel
	}
	return &deepgramClient{
		apiKey:     apiKey,
		model:      model,
		httpClient: newPooledHTTPClient(),
	}
}

func (c *deepgramClient) buildRequest(ctx context.Context, text string) (*http.Request, error) {
	params := url.Values{}
	params.Set("model", c.model)
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
	return req, nil
}

// SynthesizeStream reads the response body as it arrives. Deepgram returns
// raw headerless PCM, so bytes are playable the moment they land.
func (c *deepgramClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("tts error %d: %s", resp.StatusCode, string(body))
	}
	return streamHTTPResponse(ctx, resp), nil
}

func (c *deepgramClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}

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
