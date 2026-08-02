package signaling

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// widgetRateLimiter is a small fixed-window per-IP rate limiter for the
// public /widget-config endpoint. The endpoint is unauthenticated and
// served from a tenant-resolver cache, so each request is cheap — but a
// determined attacker could still consume CPU / bandwidth without one.
//
// In-memory only. For multi-replica deployments where strict global
// limits matter, swap to a Redis-backed bucket; for single-instance the
// per-process counter is sufficient.
type widgetRateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	counts map[string]*rateCount
}

type rateCount struct {
	count   int
	resetAt time.Time
}

const (
	// Defaults tuned to be invisible to legitimate users (one widget load
	// usually fires one /widget-config request per page) while throttling
	// abusive bursts to a manageable rate.
	defaultWidgetRateLimit  = 120
	defaultWidgetRateWindow = time.Minute
	// Opportunistic GC threshold — purge expired entries when the map
	// grows past this size to keep memory bounded under sustained load.
	rateLimiterGCThreshold = 8192
)

func newWidgetRateLimiter(limit int, window time.Duration) *widgetRateLimiter {
	if limit <= 0 {
		limit = defaultWidgetRateLimit
	}
	if window <= 0 {
		window = defaultWidgetRateWindow
	}
	return &widgetRateLimiter{
		limit:  limit,
		window: window,
		counts: make(map[string]*rateCount),
	}
}

// Allow reports whether a request from the given client may proceed and,
// if not, how many seconds the client should wait before retrying. The
// limiter resets the counter once the window elapses.
func (l *widgetRateLimiter) Allow(client string) (bool, int) {
	if client == "" {
		client = "_unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	c, ok := l.counts[client]
	if !ok || now.After(c.resetAt) {
		l.counts[client] = &rateCount{count: 1, resetAt: now.Add(l.window)}
		l.maybeGC(now)
		return true, 0
	}
	if c.count >= l.limit {
		retryAfter := int(c.resetAt.Sub(now).Seconds())
		if retryAfter < 1 {
			retryAfter = 1
		}
		return false, retryAfter
	}
	c.count++
	return true, 0
}

// maybeGC purges expired entries when the map is large. Caller must hold
// the lock. Cheap amortised cost since we only walk the map after it
// crosses the threshold.
func (l *widgetRateLimiter) maybeGC(now time.Time) {
	if len(l.counts) < rateLimiterGCThreshold {
		return
	}
	for k, v := range l.counts {
		if now.After(v.resetAt) {
			delete(l.counts, k)
		}
	}
}

// clientIP extracts the most-trusted client IP from the request:
//  1. The first hop in X-Forwarded-For (set by the load balancer / CDN
//     before forwarding to the origin).
//  2. RemoteAddr's host portion as a fallback for direct connections.
//
// Production deployments must terminate inbound traffic at a trusted edge
// — otherwise the X-Forwarded-For header is attacker-controlled.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if real := r.Header.Get("X-Real-IP"); real != "" {
		return strings.TrimSpace(real)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
