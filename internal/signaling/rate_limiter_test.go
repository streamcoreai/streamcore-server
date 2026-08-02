package signaling

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestWidgetRateLimiterAllowsUnderLimit(t *testing.T) {
	l := newWidgetRateLimiter(3, time.Minute)
	for i := 0; i < 3; i++ {
		ok, retry := l.Allow("1.2.3.4")
		if !ok {
			t.Errorf("call %d: expected Allow=true, got false (retry=%d)", i+1, retry)
		}
		if retry != 0 {
			t.Errorf("call %d: expected retry=0 when allowed, got %d", i+1, retry)
		}
	}
}

func TestWidgetRateLimiterRejectsOverLimit(t *testing.T) {
	l := newWidgetRateLimiter(2, time.Minute)
	l.Allow("9.9.9.9")
	l.Allow("9.9.9.9")
	ok, retry := l.Allow("9.9.9.9")
	if ok {
		t.Errorf("expected rejection on 3rd call with limit=2")
	}
	if retry < 1 {
		t.Errorf("expected retry-after >= 1 when rejected, got %d", retry)
	}
}

func TestWidgetRateLimiterIsolatesByClient(t *testing.T) {
	// Two different IPs each get their own bucket.
	l := newWidgetRateLimiter(1, time.Minute)
	if ok, _ := l.Allow("1.1.1.1"); !ok {
		t.Errorf("expected first call from 1.1.1.1 to succeed")
	}
	if ok, _ := l.Allow("1.1.1.1"); ok {
		t.Errorf("expected second call from 1.1.1.1 to be rate-limited")
	}
	if ok, _ := l.Allow("2.2.2.2"); !ok {
		t.Errorf("different IP should have its own bucket")
	}
}

func TestWidgetRateLimiterResetsAfterWindow(t *testing.T) {
	// 50ms window so we can confirm the reset path without a slow test.
	l := newWidgetRateLimiter(1, 50*time.Millisecond)
	if ok, _ := l.Allow("3.3.3.3"); !ok {
		t.Fatalf("expected first call to succeed")
	}
	if ok, _ := l.Allow("3.3.3.3"); ok {
		t.Errorf("expected second call within window to fail")
	}
	time.Sleep(80 * time.Millisecond)
	if ok, _ := l.Allow("3.3.3.3"); !ok {
		t.Errorf("expected call after window to succeed (window reset)")
	}
}

func TestWidgetRateLimiterDefaults(t *testing.T) {
	// Negative or zero values should fall back to defaults rather than
	// produce a limiter that always rejects.
	l := newWidgetRateLimiter(0, 0)
	for i := 0; i < 50; i++ {
		ok, _ := l.Allow("4.4.4.4")
		if !ok {
			t.Errorf("default limit should comfortably allow 50 calls; failed at %d", i)
			return
		}
	}
}

func TestWidgetRateLimiterBlankClientBucketed(t *testing.T) {
	// Empty client identifier all bucket together as "_unknown" so a flood
	// of caller-IP-stripped requests still gets throttled.
	l := newWidgetRateLimiter(2, time.Minute)
	l.Allow("")
	l.Allow("")
	if ok, _ := l.Allow(""); ok {
		t.Errorf("expected blank-client bucket to enforce limit")
	}
}

func TestClientIPPrefersXForwardedFor(t *testing.T) {
	cases := []struct {
		name       string
		xff        string
		realIP     string
		remoteAddr string
		want       string
	}{
		{
			name:       "xff first hop",
			xff:        "203.0.113.5, 10.0.0.1",
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.5",
		},
		{
			name:       "xff single",
			xff:        "203.0.113.7",
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.7",
		},
		{
			name:       "x-real-ip when no xff",
			realIP:     "203.0.113.9",
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.9",
		},
		{
			name:       "remoteaddr fallback",
			remoteAddr: "198.51.100.42:8080",
			want:       "198.51.100.42",
		},
		{
			name:       "ipv6 remoteaddr",
			remoteAddr: "[2001:db8::1]:443",
			want:       "2001:db8::1",
		},
		{
			name:       "remoteaddr without port",
			remoteAddr: "198.51.100.99",
			want:       "198.51.100.99",
		},
		{
			name:       "xff with whitespace stripped",
			xff:        "   203.0.113.42   , proxy",
			remoteAddr: "10.0.0.1:54321",
			want:       "203.0.113.42",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			req.RemoteAddr = tc.remoteAddr
			got := clientIP(req)
			if got != tc.want {
				t.Errorf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
