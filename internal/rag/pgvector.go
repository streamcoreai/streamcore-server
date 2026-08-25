package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

// pgvectorClient implements Client using PostgreSQL with the pgvector extension.
type pgvectorClient struct {
	pool     *pgxpool.Pool
	embedder *embeddingClient
	table    string
	topK     int
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

	if err := verifyStore(pool, table, cfg.RAG.EmbeddingModel); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgvector: %w", err)
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

// verifyStore refuses to start against a store whose vectors cannot be
// compared with the ones this server will produce. Both halves matter: the
// declared width, which pgvector keeps straight in atttypmod (-1 for a bare
// `vector` column, which takes anything), and the model recorded on the rows,
// which is the only way to catch two 1536-wide models being mixed.
func verifyStore(pool *pgxpool.Pool, table, model string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cols, err := describeColumns(ctx, pool, table)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("table %q is missing or has no embedding column: run `streamcore-cli setup` to create it, then ingest", table)
	}

	typmod, ok := cols["embedding"]
	if !ok {
		return fmt.Errorf("table %q has no embedding column: run `streamcore-cli setup` to create it, then ingest", table)
	}
	if want, known := dimensionsFor(model); known && typmod > 0 && int(typmod) != want {
		return dimensionMismatch(table, model, want, int(typmod))
	}

	if _, ok := cols["embedding_model"]; !ok {
		return missingModelColumn(table)
	}
	return verifyRowModels(ctx, pool, table, model)
}

func describeColumns(ctx context.Context, pool *pgxpool.Pool, table string) (map[string]int32, error) {
	rows, err := pool.Query(ctx,
		`SELECT attname, atttypmod FROM pg_attribute
		 WHERE attrelid = to_regclass($1) AND attname IN ('embedding', 'embedding_model')
		   AND attnum > 0 AND NOT attisdropped`, table)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()

	cols := make(map[string]int32, 2)
	for rows.Next() {
		var name string
		var typmod int32
		if err := rows.Scan(&name, &typmod); err != nil {
			return nil, fmt.Errorf("inspect %s: %w", table, err)
		}
		cols[name] = typmod
	}
	return cols, rows.Err()
}

// verifyRowModels looks for one row that disagrees with the configured model.
// IS DISTINCT FROM rather than <> so a NULL counts as a disagreement, and the
// query stops at the first offender — which on a store built entirely with the
// wrong model is the first row it reads.
func verifyRowModels(ctx context.Context, pool *pgxpool.Pool, table, model string) error {
	want := normalizeModel(model)

	var got *string
	err := pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT embedding_model FROM %s WHERE embedding_model IS DISTINCT FROM $1 LIMIT 1`, table),
		want).Scan(&got)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // empty table, or every row agrees
	}
	if err != nil {
		return fmt.Errorf("inspect %s.embedding_model: %w", table, err)
	}

	found := ""
	if got != nil {
		found = *got
	}
	return modelMismatch(table, want, found)
}

func formatVector(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
