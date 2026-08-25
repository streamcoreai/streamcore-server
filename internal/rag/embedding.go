package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// embeddingClient calls the OpenAI embeddings API to convert text into vectors.
type embeddingClient struct {
	apiKey string
	model  string
	dims   int // expected output width, 0 for models not in modelDimensions
	client *http.Client
}

func newEmbeddingClient(apiKey, model string) *embeddingClient {
	if model == "" {
		model = defaultEmbeddingModel
	}
	dims, _ := dimensionsFor(model)
	return &embeddingClient{
		apiKey: apiKey,
		model:  model,
		dims:   dims,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

type embeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns the embedding vector for the given text.
func (e *embeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embeddingRequest{
		Model: e.model,
		Input: text,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("embedding API returned no data")
	}

	vec := result.Data[0].Embedding
	if e.dims > 0 && len(vec) != e.dims {
		return nil, fmt.Errorf("embedding model %q returned %d dimensions, expected %d", e.model, len(vec), e.dims)
	}

	return vec, nil
}
