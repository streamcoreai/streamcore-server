package tts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Close has to get through both wrappers to the Cartesia socket, and a client
// closed while a reply is still unwinding must not dial a new one.
func TestCloseReachesCartesiaSocketThroughWrappers(t *testing.T) {
	opened := make(chan struct{}, 4)
	gone := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		opened <- struct{}{}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				gone <- struct{}{}
				return
			}
		}
	}))
	defer server.Close()

	ws := NewCartesiaWSClient("key", "voice", "ws"+strings.TrimPrefix(server.URL, "http")).(*cartesiaWSClient)
	client := &sanitizingClient{inner: newLimitedClient(ws, newConcurrencyLimiter(1))}
	if err := ws.ensureConnected(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	<-opened

	Close(client)
	select {
	case <-gone:
	case <-time.After(2 * time.Second):
		t.Fatal("Cartesia socket still open after Close")
	}

	if err := ws.ensureConnected(context.Background()); err == nil {
		t.Error("closed client dialled again")
	}
	select {
	case <-opened:
		t.Error("closed client opened a second socket")
	case <-time.After(100 * time.Millisecond):
	}

	// Providers that hold nothing open are untouched.
	Close(&sanitizingClient{inner: &fakeStreamClient{}})
	Close(nil)
}

// fakeStreamClient is a controllable TTS Client for exercising the limiter
// decorator. Each SynthesizeStream returns a channel the test closes via the
// returned release func, letting tests hold a generation "open" for as long as
// they want.
type fakeStreamClient struct {
	startErr error
}

func (f *fakeStreamClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	return []byte(text), f.startErr
}

func (f *fakeStreamClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	ch := make(chan StreamChunk, 1)
	ch <- StreamChunk{PCM: []byte(text)}
	// Caller closes via context cancel or by draining; for these tests we
	// leave it open and let ctx drive completion where needed. To represent a
	// finished generation we close immediately after the single chunk unless
	// the test keeps ctx alive. Simplicity: close right away.
	close(ch)
	return ch, nil
}

func TestConcurrencyLimiterBlocksAtLimit(t *testing.T) {
	lim := newConcurrencyLimiter(2)
	ctx := context.Background()

	if err := lim.acquire(ctx); err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if err := lim.acquire(ctx); err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if got := lim.InUse(); got != 2 {
		t.Fatalf("InUse = %d, want 2", got)
	}

	// Third acquire must block until a slot frees.
	acquired := make(chan struct{})
	go func() {
		_ = lim.acquire(ctx)
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("third acquire returned while at limit; expected it to block")
	case <-time.After(50 * time.Millisecond):
	}

	if got := lim.Waiting(); got != 1 {
		t.Fatalf("Waiting = %d, want 1", got)
	}

	lim.release()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("third acquire did not unblock after release")
	}
}

func TestConcurrencyLimiterContextCancelWhileQueued(t *testing.T) {
	lim := newConcurrencyLimiter(1)
	if err := lim.acquire(context.Background()); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- lim.acquire(ctx) }()

	// Let the goroutine reach the queued state, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued acquire err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled acquire did not return")
	}

	if got := lim.InUse(); got != 1 {
		t.Fatalf("InUse = %d after cancel, want 1 (cancelled waiter must not take a slot)", got)
	}
}

func TestLimitedClientReleasesWhenStreamCloses(t *testing.T) {
	lim := newConcurrencyLimiter(1)
	client := newLimitedClient(&fakeStreamClient{}, lim)
	ctx := context.Background()

	stream, err := client.SynthesizeStream(ctx, "hello")
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}
	// Drain the stream — once it closes, the slot must be released.
	for range stream {
	}

	// Poll briefly: release happens in the relay goroutine after close.
	deadline := time.After(time.Second)
	for lim.InUse() != 0 {
		select {
		case <-deadline:
			t.Fatalf("slot not released after stream drained; InUse = %d", lim.InUse())
		case <-time.After(5 * time.Millisecond):
		}
	}

	// A fresh acquire should now succeed immediately.
	if err := lim.acquire(ctx); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestLimitedClientReleasesOnStartError(t *testing.T) {
	lim := newConcurrencyLimiter(1)
	client := newLimitedClient(&fakeStreamClient{startErr: errors.New("boom")}, lim)

	if _, err := client.SynthesizeStream(context.Background(), "x"); err == nil {
		t.Fatal("expected error from failed start")
	}
	if got := lim.InUse(); got != 0 {
		t.Fatalf("InUse = %d after start error, want 0 (slot must be released)", got)
	}
}

func TestLimitedClientSynthesizeReleases(t *testing.T) {
	lim := newConcurrencyLimiter(1)
	client := newLimitedClient(&fakeStreamClient{}, lim)

	if _, err := client.Synthesize(context.Background(), "hi"); err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if got := lim.InUse(); got != 0 {
		t.Fatalf("InUse = %d after Synthesize, want 0", got)
	}
}
