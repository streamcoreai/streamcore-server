package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/streamcoreai/server/internal/config"
)

// pgvectorClient implements Client using PostgreSQL with the pgvector extension.
type pgvectorClient struct {
	pool      *pgxpool.Pool
	embedder  *embeddingClient
	table     string
	topK      int
}

// NewPgvectorClient creates a RAG client backed by pgvector.
// Expected table schema:
//
//	CREATE TABLE documents (
//	    id SERIAL PRIMARY KEY,
//	    content TEXT NOT NULL,
//	    embedding vector(1536)
//	);
func NewPgvectorClient(cfg *config.Config) (Client, error) {
	pool, err := pgxpool.New(context.Background(), cfg.Pgvector.ConnectionString)
	if err != nil {
		return nil, fmt.Errorf("pgvector connect: %w", err)
	}

	table := cfg.Pgvector.Table
	if table == "" {
		table = "documents"
	}

	topK := cfg.RAG.TopK
	if topK == 0 {
		topK = 3
	}

	return &pgvectorClient{
		pool:     pool,
		embedder: newEmbeddingClient(cfg.OpenAI.APIKey, cfg.RAG.EmbeddingModel),
		table:    table,
		topK:     topK,
	}, nil
}

func (c *pgvectorClient) Search(ctx context.Context, query string, topK int) ([]string, error) {
	if topK == 0 {
		topK = c.topK
	}

	embedding, err := c.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Format embedding as pgvector literal: [0.1,0.2,...]
	vecLiteral := formatVector(embedding)

	sql := fmt.Sprintf(
		`SELECT content FROM %s ORDER BY embedding <=> $1::vector LIMIT $2`,
		c.table,
	)

	rows, err := c.pool.Query(ctx, sql, vecLiteral, topK)
	if err != nil {
		return nil, fmt.Errorf("pgvector query: %w", err)
	}
	defer rows.Close()

	var chunks []string
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		chunks = append(chunks, content)
	}

	return chunks, rows.Err()
}

func formatVector(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
