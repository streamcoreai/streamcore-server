package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
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

	table := cfg.Supabase.Table
	if table == "" {
		table = "documents"
	}

	httpClient := &http.Client{Timeout: 10 * time.Second}
	if err := verifySupabaseStore(httpClient, cfg.Supabase.URL, cfg.Supabase.APIKey, table, cfg.RAG.EmbeddingModel); err != nil {
		return nil, fmt.Errorf("supabase: %w", err)
	}

	return &supabaseClient{
		url:      cfg.Supabase.URL,
		apiKey:   cfg.Supabase.APIKey,
		function: fn,
		embedder: newEmbeddingClient(cfg.OpenAI.APIKey, cfg.RAG.EmbeddingModel),
		topK:     topK,
		client:   httpClient,
	}, nil
}

// verifySupabaseStore runs the same two checks as the pgvector client, against
// the only surface PostgREST offers: rows. There is no catalog to introspect,
// so the width comes from a sampled embedding and the model check is a filter
// that asks the database for one disagreeing row. A project that is down or
// still empty leaves the store unverified rather than blocking startup; a
// mismatch, or a table predating the embedding_model column, is fatal.
func verifySupabaseStore(client *http.Client, baseURL, apiKey, table, model string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := strings.TrimSuffix(baseURL, "/") + "/rest/v1/" + url.PathEscape(table)
	want := normalizeModel(model)

	// A row whose model differs, or is null. PostgREST's neq skips nulls, so the
	// null arm has to be spelled out.
	query := fmt.Sprintf("?select=embedding,embedding_model&or=(embedding_model.neq.%s,embedding_model.is.null)&limit=1",
		url.QueryEscape(want))
	rows, err := supabaseSelect(ctx, client, base+query, apiKey)
	if err != nil {
		if isMissingColumn(err) {
			return missingModelColumn(table)
		}
		log.Printf("Warning: RAG store check skipped — %s: %v", table, err)
		return nil
	}
	if len(rows) > 0 {
		return modelMismatch(table, want, rows[0].EmbeddingModel)
	}

	// Every row agrees on the model, so any row will do for the width.
	rows, err = supabaseSelect(ctx, client, base+"?select=embedding,embedding_model&limit=1", apiKey)
	if err != nil {
		log.Printf("Warning: RAG dimension check skipped — %s: %v", table, err)
		return nil
	}
	if len(rows) == 0 {
		return nil // nothing ingested yet
	}

	wantDims, known := dimensionsFor(model)
	got, ok := vectorWidth(rows[0].Embedding)
	if !known || !ok {
		return nil
	}
	if got != wantDims {
		return dimensionMismatch(table, model, wantDims, got)
	}
	return nil
}

type supabaseRow struct {
	Embedding      json.RawMessage `json:"embedding"`
	EmbeddingModel string          `json:"embedding_model"`
}

func supabaseSelect(ctx context.Context, client *http.Client, endpoint, apiKey string) ([]supabaseRow, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", apiKey)
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var rows []supabaseRow
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return rows, nil
}

// isMissingColumn separates "your table predates embedding_model" from every
// other reason a select can fail. PostgREST reports it as 42703 in the body.
func isMissingColumn(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "42703") ||
		(strings.Contains(msg, "embedding_model") && strings.Contains(msg, "does not exist"))
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
		if strings.Contains(string(respBody), "dimensions") {
			return nil, fmt.Errorf("supabase rpc returned %d: %s — %s stores vectors of a different width than embedding_model %q produces; re-ingest with streamcore-cli or change the model back", resp.StatusCode, string(respBody), c.function, c.embedder.model)
		}
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
