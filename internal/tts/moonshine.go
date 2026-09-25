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

// defaultMoonshineVoice is the catalog voice the sidecar loads when none is
// configured. The prefix picks the vocoder: kokoro_, piper_, or zipvoice_.
const defaultMoonshineVoice = "kokoro_af_heart"

const defaultMoonshineTTSURL = "http://127.0.0.1:8310"

// moonshineTTSClient connects to the Moonshine TTS HTTP server
// (external/moonshine/moonshineTts) and implements the TTS Client interface.
//
// The sidecar mirrors Deepgram's /v1/speak: text in the body, encoding and
// sample rate as query parameters, raw headerless PCM written back as it is
// synthesized. So this streams the response the same way the Deepgram client
// does, and the first clause plays while the rest is still being generated.
type moonshineTTSClient struct {
	baseURL    string
	voice      string
	httpClient *http.Client
}

// NewMoonshineClient creates a client that talks to the Moonshine TTS server.
// baseURL is the HTTP address (e.g. http://127.0.0.1:8310); an empty voice
// falls back to defaultMoonshineVoice.
func NewMoonshineClient(baseURL, voice string) Client {
	if baseURL == "" {
		baseURL = defaultMoonshineTTSURL
	}
	if voice == "" {
		voice = defaultMoonshineVoice
	}
	return &moonshineTTSClient{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		voice:      voice,
		httpClient: newPooledHTTPClient(),
	}
}

func (c *moonshineTTSClient) buildRequest(ctx context.Context, text string) (*http.Request, error) {
	params := url.Values{}
	params.Set("voice", c.voice)
	params.Set("encoding", "linear16")
	params.Set("sample_rate", "16000")
	params.Set("container", "none")

	u := fmt.Sprintf("%s/v1/speak?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("moonshine tts create request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
	return req, nil
}

// SynthesizeStream reads the response body as it arrives. The sidecar writes
// one chunk per piece of the reply, so bytes are playable the moment they land.
func (c *moonshineTTSClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moonshine tts request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("moonshine tts error %d: %s", resp.StatusCode, string(body))
	}
	return streamHTTPResponse(ctx, resp), nil
}

func (c *moonshineTTSClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	req, err := c.buildRequest(ctx, text)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("moonshine tts request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("moonshine tts error %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("moonshine tts read response: %w", err)
	}

	log.Printf("[tts:moonshine] synthesized %d bytes for %d chars of text", len(data), len(text))
	return data, nil
}
