package tts

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Cartesia counts TTS concurrency by the number of unique generation contexts
// active at a given moment — not by phone call and not by open WebSocket
// connection. Idle connections cost nothing, and the account is allowed up to
// 10× the concurrency limit in parallel connections (see
// docs.cartesia.ai/use-the-api/concurrency-limits-and-timeouts). Exceeding the
// active-context limit returns HTTP 429.
//
// concurrencyLimiter is a process-wide counting semaphore that gates how many
// SynthesizeStream / Synthesize calls may be generating simultaneously. When
// all slots are taken, additional requests block (queue) until one frees,
// instead of being rejected by Cartesia. Because agent speech is bursty and
// half-duplex, a handful of slots comfortably serves many concurrent calls —
// contention only occurs when more callers are literally being spoken to at
// the exact same instant than the plan allows.
type concurrencyLimiter struct {
	slots   chan struct{}
	limit   int
	inUse   atomic.Int64
	waiting atomic.Int64
}

func newConcurrencyLimiter(limit int) *concurrencyLimiter {
	return &concurrencyLimiter{
		slots: make(chan struct{}, limit),
		limit: limit,
	}
}

// acquire takes a generation slot, blocking until one is free or ctx is
// cancelled. It returns ctx.Err() if the wait is abandoned (e.g. barge-in or
// call hangup) before a slot becomes available.
func (l *concurrencyLimiter) acquire(ctx context.Context) error {
	// Fast path: a slot is immediately available.
	select {
	case l.slots <- struct{}{}:
		l.inUse.Add(1)
		return nil
	default:
	}

	// Slow path: all slots busy — queue for one.
	waiting := l.waiting.Add(1)
	defer l.waiting.Add(-1)
	start := time.Now()
	log.Printf("[tts:limit] all %d generation slots in use, queuing request (in_use=%d waiting=%d)",
		l.limit, l.inUse.Load(), waiting)

	select {
	case l.slots <- struct{}{}:
		l.inUse.Add(1)
		log.Printf("[tts:limit] slot acquired after %s queued (in_use=%d waiting=%d)",
			time.Since(start).Round(time.Millisecond), l.inUse.Load(), l.waiting.Load()-1)
		return nil
	case <-ctx.Done():
		log.Printf("[tts:limit] request abandoned after %s queued: %v",
			time.Since(start).Round(time.Millisecond), ctx.Err())
		return ctx.Err()
	}
}

// release returns a slot to the pool. It must be called exactly once for every
// successful acquire.
func (l *concurrencyLimiter) release() {
	<-l.slots
	l.inUse.Add(-1)
}

// InUse reports how many generation slots are currently held. Exposed for
// observability/health surfaces.
func (l *concurrencyLimiter) InUse() int { return int(l.inUse.Load()) }

// Waiting reports how many requests are currently queued for a slot.
func (l *concurrencyLimiter) Waiting() int { return int(l.waiting.Load()) }

// The limiter is process-global: every call builds its own tts.Client (and its
// own Cartesia WebSocket), but the concurrency ceiling applies to the whole
// account/API key, so all of them must share one semaphore.
//
// NOTE: this is in-process only. It is correct for a single voice-server task
// (the current ECS topology, asg_desired_size = 1). If the service is scaled
// to multiple instances sharing one Cartesia key, each instance would enforce
// the limit independently and the account total could be exceeded — that would
// need a distributed counter (e.g. Redis/Postgres) instead.
var (
	globalLimiterOnce sync.Once
	globalLimiter     *concurrencyLimiter
)

// sharedConcurrencyLimiter returns the process-global limiter, creating it on
// first use with the given limit. Subsequent calls return the same instance
// and ignore their limit argument (config is constant for the process).
func sharedConcurrencyLimiter(limit int) *concurrencyLimiter {
	globalLimiterOnce.Do(func() {
		globalLimiter = newConcurrencyLimiter(limit)
		log.Printf("[tts:limit] cartesia generation concurrency limited to %d slots", limit)
	})
	return globalLimiter
}

// limitedClient wraps a TTS Client so every generation passes through a
// concurrency limiter. It releases the slot when the synthesis stream
// finishes, errors, or is cancelled.
type limitedClient struct {
	inner   Client
	limiter *concurrencyLimiter
}

func newLimitedClient(inner Client, limiter *concurrencyLimiter) Client {
	return &limitedClient{inner: inner, limiter: limiter}
}

func (c *limitedClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if err := c.limiter.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.limiter.release()
	return c.inner.Synthesize(ctx, text)
}

func (c *limitedClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return c.limitedStream(ctx, func() (<-chan StreamChunk, error) {
		return c.inner.SynthesizeStream(ctx, text)
	})
}

// SynthesizeStreamWithControls forwards voice controls through the limiter
// to the wrapped provider, degrading to a plain stream when it doesn't
// support them. Slot accounting is identical to SynthesizeStream.
func (c *limitedClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	return c.limitedStream(ctx, func() (<-chan StreamChunk, error) {
		if cs, ok := c.inner.(ControllableStreamer); ok {
			return cs.SynthesizeStreamWithControls(ctx, text, vc)
		}
		return c.inner.SynthesizeStream(ctx, text)
	})
}

// limitedStream runs start under a held limiter slot and relays the
// resulting stream, releasing the slot when the stream closes.
func (c *limitedClient) limitedStream(ctx context.Context, start func() (<-chan StreamChunk, error)) (<-chan StreamChunk, error) {
	if err := c.limiter.acquire(ctx); err != nil {
		return nil, err
	}

	stream, err := start()
	if err != nil {
		c.limiter.release()
		return nil, err
	}

	// Relay the inner stream so the slot is held for exactly the lifetime of
	// the generation and released the moment it closes (completion, error, or
	// barge-in cancel). The relay also selects on ctx so a consumer that stops
	// reading after cancellation can never wedge this goroutine — guaranteeing
	// the slot is always returned.
	out := make(chan StreamChunk, 16)
	go func() {
		defer close(out)
		defer c.limiter.release()
		for {
			select {
			case chunk, ok := <-stream:
				if !ok {
					return
				}
				select {
				case out <- chunk:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}
