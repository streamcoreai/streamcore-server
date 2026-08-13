package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOpenAICompatible stands in for a third-party endpoint speaking OpenAI's
// protocol. It records the path it was called on and replies with a minimal
// streamed completion.
type fakeOpenAICompatible struct {
	server *httptest.Server

	// Guarded because the client fires a background ListModels warmup whose
	// request races the one the test makes.
	mu      sync.Mutex
	gotPath string
	gotAuth string
}

func newFakeOpenAICompatible(t *testing.T) *fakeOpenAICompatible {
	t.Helper()
	f := &fakeOpenAICompatible{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The warmup call is deliberately not recorded: it would overwrite the
		// path under assertion with /v1/models.
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"object":"list","data":[]}`)
			return
		}

		f.mu.Lock()
		f.gotPath = r.URL.Path
		f.gotAuth = r.Header.Get("Authorization")
		f.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOpenAICompatible) recorded() (path, auth string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gotPath, f.gotAuth
}

// A configured base URL has to actually redirect traffic. Without this the
// client silently keeps talking to api.openai.com, and the failure surfaces as
// an authentication error against a key that belongs to a different provider —
// which reads like a bad key rather than an ignored setting.
func TestOpenAIBaseURLRedirectsRequests(t *testing.T) {
	f := newFakeOpenAICompatible(t)

	c := NewOpenAIClient("test-key", "deepseek-chat", "sys", f.server.URL+"/v1")
	if _, err := c.Chat(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	path, auth := f.recorded()
	if path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", path)
	}
	if auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the key forwarded", auth)
	}
}

// A trailing slash in the configured URL must not produce a doubled separator:
// "https://host/v1//chat/completions" is a 404 on most gateways.
func TestOpenAIBaseURLTrimsTrailingSlash(t *testing.T) {
	f := newFakeOpenAICompatible(t)

	c := NewOpenAIClient("k", "m", "sys", f.server.URL+"/v1/")
	if _, err := c.Chat(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	path, _ := f.recorded()
	if strings.Contains(path, "//") {
		t.Errorf("request path = %q, want no doubled slash", path)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("request path = %q, want /v1/chat/completions", path)
	}
}

// An unset or blank base URL must resolve to "", which is what leaves the SDK
// pointing at OpenAI — existing deployments are untouched by this option.
func TestResolveOpenAIBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"   ":                          "",
		"https://api.deepseek.com/v1":  "https://api.deepseek.com/v1",
		"https://api.deepseek.com/v1/": "https://api.deepseek.com/v1",
		"https://api.moonshot.cn/v1//": "https://api.moonshot.cn/v1",
		"  https://host/v1  ":          "https://host/v1",
	}
	for in, want := range cases {
		if got := resolveOpenAIBaseURL(in); got != want {
			t.Errorf("resolveOpenAIBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
