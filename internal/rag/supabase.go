package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/streamcoreai/server/internal/config"
)

// supabaseClient implements Client using Supabase's pgvector support via
// a Postgres RPC function. This avoids needing a direct Postgres connection
// — it calls a Supabase Edge Function or database function over HTTP.
//
// Expected setup in Supabase:
//
//	CREATE OR REPLACE FUNCTION match_documents(
//	    query_embedding vector(1536),
//	    match_count int DEFAULT 3
//	)
//	RETURNS TABLE (content text, similarity float)
//	LANGUAGE plpgsql AS $$
//	BEGIN
//	    RETURN QUERY
//	    SELECT d.content, 1 - (d.embedding <=> query_embedding) AS similarity
//	    FROM documents d
//	    ORDER BY d.embedding <=> query_embedding
//	    LIMIT match_count;
//	END;
//	$$;
type supabaseClient struct {
	url      string // Supabase project URL (e.g. https://xxx.supabase.co)
	apiKey   string // Supabase anon or service_role key
	function string // RPC function name
	embedder *embeddingClient
	topK     int
	client   *http.Client
}

func NewSupabaseClient(cfg *config.Config) (Client, error) {
	if cfg.Supabase.URL == "" {
		return nil, fmt.Errorf("supabase rag requires [supabase] url to be set")
	}
	if cfg.Supabase.APIKey == "" {
		return nil, fmt.Errorf("supabase rag requires [supabase] api_key to be set")
	}

	fn := cfg.Supabase.Function
	if fn == "" {
		fn = "match_documents"
	}

	topK := cfg.RAG.TopK
	if topK == 0 {
		topK = 3
	}

	return &supabaseClient{
		url:      cfg.Supabase.URL,
		apiKey:   cfg.Supabase.APIKey,
		function: fn,
		embedder: newEmbeddingClient(cfg.OpenAI.APIKey, cfg.RAG.EmbeddingModel),
		topK:     topK,
		client:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type supabaseRPCRequest struct {
	QueryEmbedding []float32 `json:"query_embedding"`
	MatchCount     int       `json:"match_count"`
}

type supabaseRPCResult struct {
	Content    string  `json:"content"`
	Similarity float64 `json:"similarity"`
}

func (c *supabaseClient) Search(ctx context.Context, query string, topK int) ([]string, error) {
	if topK == 0 {
		topK = c.topK
	}

	embedding, err := c.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	body, err := json.Marshal(supabaseRPCRequest{
		QueryEmbedding: embedding,
		MatchCount:     topK,
	})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/rest/v1/rpc/%s", c.url, c.function)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", c.apiKey)
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("supabase rpc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("supabase rpc returned %d: %s", resp.StatusCode, string(respBody))
	}

	var results []supabaseRPCResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode supabase response: %w", err)
	}

	chunks := make([]string, 0, len(results))
	for _, r := range results {
		chunks = append(chunks, r.Content)
	}

	return chunks, nil
}
