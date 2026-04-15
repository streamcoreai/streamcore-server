package rag

import (
	"context"
	"fmt"

	"github.com/streamcoreai/server/internal/config"
)

// Client is the interface for RAG providers. Implementations retrieve
// relevant context chunks for a given query using vector similarity search.
type Client interface {
	Search(ctx context.Context, query string, topK int) ([]string, error)
}

// NewClient returns a RAG client for the configured provider, or nil if
// RAG is disabled (no provider set).
func NewClient(cfg *config.Config) (Client, error) {
	switch cfg.RAG.Provider {
	case "pgvector":
		return NewPgvectorClient(cfg)
	case "supabase":
		return NewSupabaseClient(cfg)
	case "", "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown rag provider %q (supported: pgvector, supabase)", cfg.RAG.Provider)
	}
}
