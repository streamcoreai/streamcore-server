package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRAGClient struct {
	calls   atomic.Int32
	results []string
}

func (f *fakeRAGClient) Search(ctx context.Context, query string, topK int) ([]string, error) {
	f.calls.Add(1)
	return f.results, nil
}

func TestRAGPrefetchHitOnMatchingQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &fakeRAGClient{results: []string{"chunk one"}}
	p := &Pipeline{ctx: ctx, ragClient: fake, transcriptLog: &TranscriptLog{}}

	const query = "what are your opening hours on the weekend"
	p.startRAGPrefetch(query)

	st := p.takeRAGPrefetch(p.ragSearchQuery(query))
	if st == nil {
		t.Fatal("expected prefetch hit for matching query")
	}
	select {
	case <-st.done:
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch never completed")
	}
	if st.err != nil || len(st.results) != 1 {
		t.Fatalf("prefetch results = %v err = %v", st.results, st.err)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Errorf("search calls = %d, want 1", got)
	}
	// Slot is one-shot.
	if p.takeRAGPrefetch(query) != nil {
		t.Error("prefetch slot should be cleared after take")
	}
}

func TestRAGPrefetchDiscardedOnQueryChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &fakeRAGClient{}
	p := &Pipeline{ctx: ctx, ragClient: fake, transcriptLog: &TranscriptLog{}}

	p.startRAGPrefetch("what are your opening hours")
	// The turn merged more text — the final query differs.
	if st := p.takeRAGPrefetch("what are your opening hours on public holidays"); st != nil {
		t.Error("expected stale prefetch to be discarded on query mismatch")
	}
}

func TestRAGPrefetchSkipsWhenGateSkips(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fake := &fakeRAGClient{}
	p := &Pipeline{ctx: ctx, ragClient: fake, transcriptLog: &TranscriptLog{}}

	// A bare affirmation is gated out by shouldSkipRAG — no prefetch, no
	// embedding spend.
	p.startRAGPrefetch("yeah sure")
	time.Sleep(50 * time.Millisecond)
	if got := fake.calls.Load(); got != 0 {
		t.Errorf("search calls = %d, want 0 for gated utterance", got)
	}
}
