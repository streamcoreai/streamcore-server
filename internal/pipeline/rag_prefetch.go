package pipeline

import (
	"context"
	"log"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/rag"
)

// ragPrefetchTimeout bounds a speculative retrieval so an abandoned prefetch
// can never hold resources past its useful life.
const ragPrefetchTimeout = 10 * time.Second

// ragPrefetchState is one speculative retrieval started while the
// turn-merge debounce window is still running. The merge window (200-800ms)
// and the embedding+vector-search round-trip (~150-300ms) are both pure
// waiting on the critical path — overlapping them hides retrieval entirely
// on most turns.
type ragPrefetchState struct {
	query   string
	done    chan struct{}
	results []string
	err     error
	timing  rag.Timing
}

// startRAGPrefetch kicks off retrieval for the pending (not yet flushed)
// turn text. Called from the turn buffer when the first final of a turn
// arrives. If a later final merges into the turn, the query won't match at
// take time and respond() falls back to a live search — the speculative
// result is simply discarded.
func (p *Pipeline) startRAGPrefetch(pendingText string) {
	if p == nil || p.ragClient == nil || p.ragDisabled.Load() {
		return
	}
	if skip, _ := shouldSkipRAG(p, pendingText); skip {
		return
	}
	st := &ragPrefetchState{
		query: p.ragSearchQuery(pendingText),
		done:  make(chan struct{}),
	}
	p.ragPrefetchMu.Lock()
	p.ragPrefetch = st
	p.ragPrefetchMu.Unlock()

	go func() {
		defer p.recoverKeepAlive("ragPrefetch")
		defer close(st.done)
		// Call-scoped context, not turn-scoped: the prefetch outlives the
		// merge window by design and is awaited (or discarded) later.
		ctx, cancel := context.WithTimeout(p.ctx, ragPrefetchTimeout)
		defer cancel()
		ctx = rag.WithTiming(ctx, &st.timing)
		st.results, st.err = p.ragClient.Search(ctx, st.query, 0)
	}()
}

// takeRAGPrefetch claims the pending prefetch if its query matches what this
// turn would search for. Always clears the slot — a mismatched (stale)
// prefetch is discarded so it can't leak into a later turn.
func (p *Pipeline) takeRAGPrefetch(query string) *ragPrefetchState {
	if p == nil {
		return nil
	}
	p.ragPrefetchMu.Lock()
	st := p.ragPrefetch
	p.ragPrefetch = nil
	p.ragPrefetchMu.Unlock()
	if st == nil {
		return nil
	}
	if st.query != query {
		log.Printf("[agent] RAG prefetch discarded — turn text changed during merge window")
		return nil
	}
	return st
}

// await blocks until the speculative search finishes and returns its result.
// The prefetch goroutine has its own timeout, so this only waits out the
// remainder of a search already in flight; ctx cancellation (barge-in, hangup)
// abandons the wait without disturbing that goroutine.
func (st *ragPrefetchState) await(ctx context.Context) ([]string, error) {
	select {
	case <-st.done:
		return st.results, st.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
