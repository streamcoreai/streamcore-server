package rag

import "context"

// Timing collects per-search stage durations in milliseconds. A caller opts in
// by putting a *Timing into the search context via WithTiming; the active
// Client populates it. Absent collector = zero cost, so this never affects the
// hot path unless telemetry is explicitly requested.
type Timing struct {
	EmbedMs int64 // embedding provider round-trip (the public-internet hop)
	QueryMs int64 // vector store query (pgvector SQL or Supabase RPC)
}

type timingKey struct{}

// WithTiming returns a context carrying t. The RAG client records embedding and
// vector-store stage timings into t during Search.
func WithTiming(ctx context.Context, t *Timing) context.Context {
	return context.WithValue(ctx, timingKey{}, t)
}

// timingFrom returns the collector in ctx, or nil if none was attached.
func timingFrom(ctx context.Context) *Timing {
	t, _ := ctx.Value(timingKey{}).(*Timing)
	return t
}
