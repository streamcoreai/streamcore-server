package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// newTestPublicMux is the public mux with the status handlers a default config
// produces, for tests about the other routes.
func newTestPublicMux() *http.ServeMux {
	cfg := &config.Config{}
	health := newHealthHandler(cfg, time.Now(), func() int { return 0 })
	return newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil, health, versionHandler(currentBuildInfo()))
}

func TestResolveBuildInfo(t *testing.T) {
	vcs := func(settings ...string) []debug.BuildSetting {
		var out []debug.BuildSetting
		for i := 0; i < len(settings); i += 2 {
			out = append(out, debug.BuildSetting{Key: settings[i], Value: settings[i+1]})
		}
		return out
	}
	stamped := &debug.BuildInfo{
		GoVersion: "go1.25.4",
		Main:      debug.Module{Version: "v0.0.0-20261127080000-7bff4de12345"},
		Settings:  vcs("vcs.revision", "7bff4de123456789", "vcs.time", "2026-11-27T08:00:00Z", "vcs.modified", "false"),
	}

	tests := []struct {
		name                  string
		version, commit, date string
		bi                    *debug.BuildInfo
		want                  buildInfo
	}{
		{
			name:    "link-time values win over the embedded build info",
			version: "v0.1.0", commit: "abc1234", date: "2026-08-10T11:02:38Z",
			bi:   stamped,
			want: buildInfo{Version: "v0.1.0", Commit: "abc1234", Built: "2026-08-10T11:02:38Z", Go: "go1.25.4"},
		},
		{
			name: "plain go build in a checkout reports the VCS stamp",
			bi:   stamped,
			want: buildInfo{Version: "v0.0.0-20261127080000-7bff4de12345", Commit: "7bff4de123456789", Built: "2026-11-27T08:00:00Z", Go: "go1.25.4"},
		},
		{
			name: "a modified tree is marked dirty",
			bi: &debug.BuildInfo{GoVersion: "go1.25.4", Main: debug.Module{Version: "(devel)"},
				Settings: vcs("vcs.revision", "7bff4de", "vcs.modified", "true")},
			want: buildInfo{Version: "dev", Commit: "7bff4de-dirty", Built: "unknown", Go: "go1.25.4"},
		},
		{
			name: "go install of a tagged module reports the tag",
			bi:   &debug.BuildInfo{GoVersion: "go1.25.4", Main: debug.Module{Version: "v0.1.0"}},
			want: buildInfo{Version: "v0.1.0", Commit: "unknown", Built: "unknown", Go: "go1.25.4"},
		},
		{
			name: "no VCS stamp and a devel version, as in a Docker build without args",
			bi:   &debug.BuildInfo{GoVersion: "go1.25.4", Main: debug.Module{Version: "(devel)"}},
			want: buildInfo{Version: "dev", Commit: "unknown", Built: "unknown", Go: "go1.25.4"},
		},
		{
			name: "no build info at all still answers every field",
			want: buildInfo{Version: "dev", Commit: "unknown", Built: "unknown", Go: runtime.Version()},
		},
		{
			name:   "a partial stamp keeps the fallbacks for the rest",
			commit: "deadbeef",
			bi:     stamped,
			want:   buildInfo{Version: "v0.0.0-20261127080000-7bff4de12345", Commit: "deadbeef", Built: "2026-11-27T08:00:00Z", Go: "go1.25.4"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveBuildInfo(tt.version, tt.commit, tt.date, tt.bi); got != tt.want {
				t.Fatalf("resolveBuildInfo() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestVersionEndpoint(t *testing.T) {
	info := buildInfo{Version: "v0.1.0", Commit: "abc1234", Built: "2026-08-10T11:02:38Z", Go: "go1.25.4"}
	srv := httptest.NewServer(newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil,
		newHealthHandler(&config.Config{}, time.Now(), func() int { return 0 }), versionHandler(info)))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %q is not a JSON object of strings: %v", body, err)
	}
	want := map[string]string{"version": "v0.1.0", "commit": "abc1234", "built": "2026-08-10T11:02:38Z", "go": "go1.25.4"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %v, want %v", got, want)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	post, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	post.Body.Close()
	if post.StatusCode != http.StatusMethodNotAllowed || post.Header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST /version: status %d, Allow %q; want 405 with Allow: GET, HEAD", post.StatusCode, post.Header.Get("Allow"))
	}
}

func TestWantsJSON(t *testing.T) {
	tests := []struct {
		accept []string
		want   bool
	}{
		{nil, false},
		{[]string{""}, false},
		{[]string{"*/*"}, false},
		{[]string{"text/plain"}, false},
		{[]string{"text/html, */*;q=0.8"}, false},
		{[]string{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"}, false},
		{[]string{"application/*"}, false},
		{[]string{"application/jsonx"}, false},
		{[]string{"application/json-patch+json"}, false},
		{[]string{"application/json;q=0"}, false},
		{[]string{"application/json;q=0.0"}, false},
		{[]string{"application/json;q=abc"}, false},
		{[]string{"application/json;q=1.5"}, false},
		{[]string{"application/json;q=NaN"}, false},
		{[]string{"application/json;q=1;q=0"}, false},
		// Ties keep the default: axios's stock Accept header.
		{[]string{"application/json, text/plain, */*"}, false},
		{[]string{"application/json, */*"}, false},
		{[]string{"application/json;q=0.5, text/plain"}, false},
		{[]string{"application/json;q=0.5, text/*"}, false},
		{[]string{"*/*;q=0.1, application/json;q=0.1"}, false},

		{[]string{"application/json"}, true},
		{[]string{"Application/JSON"}, true},
		{[]string{"application/json; charset=utf-8"}, true},
		{[]string{"application/json;q=0.001"}, true},
		{[]string{"  application/json  "}, true},
		{[]string{"application/json, */*;q=0.8"}, true},
		{[]string{"text/plain;q=0.5, application/json"}, true},
		{[]string{"text/plain;q=0, */*, application/json;q=0.1"}, true},
		{[]string{"application/json, text/javascript, */*; q=0.01"}, true},
		{[]string{"garbage;;, application/json"}, true},
		{[]string{"text/html", "application/json"}, true},

		// A comma inside a quoted parameter does not start a new range.
		{[]string{`text/plain; ext="a, application/json"`}, false},
		{[]string{`text/plain; ext="a\", application/json"`}, false},
		{[]string{`text/plain; ext="a,b"; q=0.2, application/json; q=0.9`}, true},
		{[]string{`application/json; ext="unterminated`}, false},
		{[]string{`application/json, text/plain; ext="unterminated, */*`}, true},

		// q must be a qvalue: "0" or "1", at most three decimals, no sign or
		// exponent.
		{[]string{"application/json;q=1."}, true},
		{[]string{"application/json;q=1.000"}, true},
		{[]string{"application/json;q=0.999"}, true},
		{[]string{"application/json;q=+0.5"}, false},
		{[]string{"application/json;q=-0.5"}, false},
		{[]string{"application/json;q=1e0"}, false},
		{[]string{"application/json;q=.5"}, false},
		{[]string{"application/json;q=0.1234"}, false},
		{[]string{"application/json;q=1.0000"}, false},
		{[]string{"application/json;q=1.001"}, false},
		{[]string{"application/json;q=2"}, false},
		{[]string{"text/plain;q=0.1234, application/json;q=0.5"}, true},

		// Both bodies are UTF-8; a range asking for another charset does not
		// match either of them.
		{[]string{"application/json; charset=UTF-8"}, true},
		{[]string{"application/json; charset=iso-8859-1"}, false},
		{[]string{"text/plain; charset=iso-8859-1; q=1.0, application/json; q=0.5"}, true},
		{[]string{"text/plain; charset=utf-8, application/json; q=0.5"}, false},
		{[]string{"text/*; charset=iso-8859-1; q=1, application/json; q=0.5"}, true},
		{[]string{"*/*; charset=iso-8859-1; q=1, application/json; q=0.5"}, true},
		{[]string{"*/*; charset=iso-8859-1, application/json; charset=iso-8859-1"}, false},

		// A range naming charset=utf-8 is more specific than the bare range
		// and sets that body's q, whichever q is higher.
		{[]string{"text/plain; q=1, text/plain; charset=utf-8; q=0.1, application/json; q=0.5"}, true},
		{[]string{"application/json; q=0.9, application/json; charset=utf-8; q=0.2, text/plain; q=0.5"}, false},
		{[]string{"application/json; charset=utf-8; q=0, application/json"}, false},
		{[]string{"*/*; q=1, */*; charset=utf-8; q=0.1, application/json; q=0.5"}, true},
		{[]string{"text/*; charset=utf-8; q=0.1, */*, application/json; q=0.5"}, true},

		// mime.ParseMediaType unquotes parameter values before the qvalue
		// check.
		{[]string{`application/json; q="0.9"`}, true},
		{[]string{`application/json; q="0.9", text/plain; q="0.95"`}, false},
		{[]string{`application/json; q="1e0"`}, false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.accept, " | "), func(t *testing.T) {
			if got := wantsJSON(tt.accept); got != tt.want {
				t.Fatalf("wantsJSON(%q) = %v, want %v", tt.accept, got, tt.want)
			}
		})
	}
}

// legacyHealth is /health exactly as it was before the verbose form existed.
// The default response is pinned to it, not to a hand-written expectation.
func legacyHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

type healthResponse struct {
	status int
	header http.Header
	body   string
}

func getHealth(t *testing.T, url string, accept []string) healthResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accept {
		req.Header.Add("Accept", a)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return healthResponse{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

// For every Accept header that does not ask for JSON by name, keep the legacy
// HTTP 200 status, plain "ok" body, and Content-Type/Content-Length/Cache-Control
// headers (plus Vary: Accept): body parity protects callers that expect "ok",
// while load balancers and the deploy workflow's curl -sf check rely on HTTP
// success.
func TestHealthDefaultIsTheLegacyPlainOK(t *testing.T) {
	legacyMux := http.NewServeMux()
	legacyMux.HandleFunc("/health", legacyHealth)
	legacy := httptest.NewServer(corsMiddleware(legacyMux))
	t.Cleanup(legacy.Close)

	current := httptest.NewServer(corsMiddleware(newTestPublicMux()))
	t.Cleanup(current.Close)

	for _, accept := range [][]string{
		nil,
		{""},
		{"*/*"},
		{"text/plain"},
		{"text/html, */*;q=0.8"},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		{"application/json, text/plain, */*"},
		{"application/json;q=0"},
		{"application/*"},
		{"application/json;q=bogus"},
	} {
		t.Run(fmt.Sprintf("%q", accept), func(t *testing.T) {
			want := getHealth(t, legacy.URL, accept)
			got := getHealth(t, current.URL, accept)
			if got.status != want.status || got.body != want.body {
				t.Fatalf("got %d %q, want %d %q", got.status, got.body, want.status, want.body)
			}
			if want.body != "ok" || want.status != http.StatusOK {
				t.Fatalf("legacy baseline changed: %d %q", want.status, want.body)
			}
			for _, h := range []string{"Content-Type", "Content-Length", "Cache-Control"} {
				if got.header.Get(h) != want.header.Get(h) {
					t.Errorf("%s = %q, want %q", h, got.header.Get(h), want.header.Get(h))
				}
			}
			if got.header.Get("Vary") != "Accept" {
				t.Errorf("Vary = %q, want Accept", got.header.Get("Vary"))
			}
		})
	}
}

func TestHealthJSONReportsUptimeSessionsAndProviders(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "deepgram"
	cfg.LLM.Provider = "openai"
	cfg.TTS.Provider = "cartesia"
	started := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	h := newHealthHandler(cfg, started, func() int { return 2 })
	h.now = func() time.Time { return started.Add(time.Hour + 2*time.Minute + 3*time.Second + 400*time.Millisecond) }

	srv := httptest.NewServer(newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil, h, versionHandler(currentBuildInfo())))
	t.Cleanup(srv.Close)

	for _, accept := range [][]string{
		{"application/json"},
		{"application/json; charset=utf-8"},
		{"text/plain;q=0.5, application/json"},
		{"text/html", "application/json"},
	} {
		t.Run(fmt.Sprintf("%q", accept), func(t *testing.T) {
			got := getHealth(t, srv.URL, accept)
			if got.status != http.StatusOK {
				t.Fatalf("status = %d, want 200", got.status)
			}
			if ct := got.header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", ct)
			}
			if got.header.Get("Vary") != "Accept" || got.header.Get("Cache-Control") != "no-store" {
				t.Fatalf("Vary = %q, Cache-Control = %q; want Accept, no-store", got.header.Get("Vary"), got.header.Get("Cache-Control"))
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(got.body), &body); err != nil {
				t.Fatalf("body %q: %v", got.body, err)
			}
			want := map[string]any{
				"status":          "ok",
				"uptime":          "1h2m3s",
				"uptime_seconds":  float64(3723),
				"active_sessions": float64(2),
				"providers":       map[string]any{"stt": "deepgram", "llm": "openai", "tts": "cartesia"},
			}
			if !reflect.DeepEqual(body, want) {
				t.Fatalf("body = %v, want %v", body, want)
			}
		})
	}
}

func TestHealthJSONReadsTheSessionCountPerRequest(t *testing.T) {
	var live atomic.Int64
	h := newHealthHandler(&config.Config{}, time.Now(), func() int { return int(live.Load()) })
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	for _, n := range []int64{0, 3, 1} {
		live.Store(n)
		got := getHealth(t, srv.URL, []string{"application/json"})
		var body healthReport
		if err := json.Unmarshal([]byte(got.body), &body); err != nil {
			t.Fatal(err)
		}
		if int64(body.ActiveSessions) != n {
			t.Fatalf("active_sessions = %d, want %d", body.ActiveSessions, n)
		}
	}
}

func TestConfiguredProviders(t *testing.T) {
	tests := []struct {
		name     string
		realtime string
		want     providerNames
	}{
		{"classic pipeline", "", providerNames{STT: "deepgram", LLM: "openai", TTS: "cartesia"}},
		{"realtime none is the classic pipeline", "none", providerNames{STT: "deepgram", LLM: "openai", TTS: "cartesia"}},
		{"speech-to-speech names only the realtime provider", "grok", providerNames{Realtime: "grok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.STT.Provider, cfg.LLM.Provider, cfg.TTS.Provider = "deepgram", "openai", "cartesia"
			cfg.Realtime.Provider = tt.realtime
			if got := configuredProviders(cfg); got != tt.want {
				t.Fatalf("configuredProviders() = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("unset providers are omitted", func(t *testing.T) {
		if got := configuredProviders(&config.Config{}); got != (providerNames{}) {
			t.Fatalf("configuredProviders() = %+v, want none", got)
		}
	})
	t.Run("an unrecognised value is reported as unknown, not echoed", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.STT.Provider = "https://user:sk-secret@stt.example/v1"
		cfg.LLM.Provider = "sk-live-0123456789"
		cfg.TTS.Provider = "Deepgram" // names are case-sensitive in NewClient too
		want := providerNames{STT: "unknown", LLM: "unknown", TTS: "unknown"}
		if got := configuredProviders(cfg); got != want {
			t.Fatalf("configuredProviders() = %+v, want %+v", got, want)
		}
	})
}

func TestEveryCanonicalProviderNameIsReported(t *testing.T) {
	for _, slot := range []struct {
		name  string
		known map[string]bool
		set   func(*config.Config, string)
		get   func(providerNames) string
	}{
		{"stt", sttProviders, func(c *config.Config, v string) { c.STT.Provider = v }, func(p providerNames) string { return p.STT }},
		{"llm", llmProviders, func(c *config.Config, v string) { c.LLM.Provider = v }, func(p providerNames) string { return p.LLM }},
		{"tts", ttsProviders, func(c *config.Config, v string) { c.TTS.Provider = v }, func(p providerNames) string { return p.TTS }},
		{"realtime", realtimeProviders, func(c *config.Config, v string) { c.Realtime.Provider = v }, func(p providerNames) string { return p.Realtime }},
	} {
		for name := range slot.known {
			cfg := &config.Config{}
			slot.set(cfg, name)
			if got := slot.get(configuredProviders(cfg)); got != name {
				t.Errorf("%s provider %q reported as %q", slot.name, name, got)
			}
		}
	}
}

// The allowlists must name exactly what each constructor accepts: a provider
// added to a constructor but not here would show as "unknown", and one listed
// here that no constructor builds would be reported as if it ran.
func TestProviderAllowlistsMatchTheConstructors(t *testing.T) {
	for _, tt := range []struct {
		file, fn, field string
		known           map[string]bool
	}{
		{"internal/stt/stt.go", "NewClient", "STT", sttProviders},
		{"internal/llm/llm.go", "NewClient", "LLM", llmProviders},
		{"internal/tts/client.go", "newProviderClient", "TTS", ttsProviders},
		{"internal/realtime/realtime.go", "NewClient", "Realtime", realtimeProviders},
	} {
		t.Run(tt.file, func(t *testing.T) {
			cases := constructorProviderCases(t, tt.file, tt.fn, tt.field)
			if len(cases) == 0 {
				t.Fatalf("no `switch cfg.%s.Provider` cases found in %s %s", tt.field, tt.file, tt.fn)
			}
			var missing, extra []string
			for name := range cases {
				if !tt.known[name] {
					missing = append(missing, name)
				}
			}
			for name := range tt.known {
				if !cases[name] {
					extra = append(extra, name)
				}
			}
			sort.Strings(missing)
			sort.Strings(extra)
			if len(missing) > 0 || len(extra) > 0 {
				t.Fatalf("status.go allowlist for %s is out of step with %s: add %v, remove %v", tt.field, tt.file, missing, extra)
			}
		})
	}
}

// constructorProviderCases returns the string cases of the
// `switch cfg.<field>.Provider` statement in function fn of file.
func constructorProviderCases(t *testing.T, file, fn, field string) map[string]bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	for _, decl := range parsed.Decls {
		f, ok := decl.(*ast.FuncDecl)
		if !ok || f.Name.Name != fn || f.Recv != nil {
			continue
		}
		ast.Inspect(f.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok || !isProviderSelector(sw.Tag, field) {
				return true
			}
			for _, stmt := range sw.Body.List {
				for _, expr := range stmt.(*ast.CaseClause).List {
					lit, ok := expr.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: non-literal provider case %T", file, expr)
					}
					name, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					cases[name] = true
				}
			}
			return false
		})
	}
	return cases
}

// isProviderSelector matches cfg.<field>.Provider.
func isProviderSelector(expr ast.Expr, field string) bool {
	outer, ok := expr.(*ast.SelectorExpr)
	if !ok || outer.Sel.Name != "Provider" {
		return false
	}
	inner, ok := outer.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == field
}

// A real config file with credentials or URLs pasted into the provider slots
// loads without error, so the wired /health must still not repeat them.
func TestLoadedConfigProviderSlotsNeverEchoRawValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := fmt.Sprintf(`
[stt]
provider = "https://%[1]s-stt@stt.example/v1"
[llm]
provider = "%[1]s-llm-key"
[tts]
provider = "%[1]s tts"
[openai]
api_key = "%[1]s-openai"
`, secretSentinel)
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !strings.Contains(cfg.STT.Provider, secretSentinel) {
		t.Fatalf("the sentinel did not reach the loaded config: %q", cfg.STT.Provider)
	}

	srv := httptest.NewServer(corsMiddleware(newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil,
		newHealthHandler(cfg, time.Now(), func() int { return 0 }), versionHandler(currentBuildInfo()))))
	t.Cleanup(srv.Close)

	got := getHealth(t, srv.URL, []string{"application/json"})
	if strings.Contains(got.body, secretSentinel) {
		t.Fatalf("/health echoed a raw provider value: %s", got.body)
	}
	var report healthReport
	if err := json.Unmarshal([]byte(got.body), &report); err != nil {
		t.Fatal(err)
	}
	if want := (providerNames{STT: "unknown", LLM: "unknown", TTS: "unknown"}); report.Providers != want {
		t.Fatalf("providers = %+v, want %+v", report.Providers, want)
	}
}

const secretSentinel = "SC30-SENTINEL-SECRET"

// providerNameFields are the only config strings the status endpoints may
// echo. Everything else is filled with a sentinel.
var providerNameFields = map[string]string{
	"Realtime.Provider": "grok",
	"STT.Provider":      "deepgram",
	"LLM.Provider":      "openai",
	"TTS.Provider":      "cartesia",
}

// fillWithSentinels sets every string reachable in v — struct fields, string
// slices and maps — to a sentinel naming its path, so a leak names the field
// it came from. Fields that look like endpoints instead get a URL on endpoint
// (still carrying the sentinel), so a handler that dialled a provider would be
// seen doing it. It returns the paths it filled.
func fillWithSentinels(v reflect.Value, path, endpoint string, filled *[]string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			p := f.Name
			if path != "" {
				p = path + "." + f.Name
			}
			fillWithSentinels(v.Field(i), p, endpoint, filled)
		}
	case reflect.String:
		if name, ok := providerNameFields[path]; ok {
			v.SetString(name)
			return
		}
		value := secretSentinel + "-" + path
		if upper := strings.ToUpper(path); strings.HasSuffix(upper, "URL") || strings.Contains(upper, "ENDPOINT") || strings.HasSuffix(upper, "HOST") {
			value = endpoint + "/" + value
		}
		v.SetString(value)
		*filled = append(*filled, path)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.String {
			v.Set(reflect.ValueOf([]string{secretSentinel + "-" + path}))
			*filled = append(*filled, path)
		}
	case reflect.Map:
		if v.Type().Key().Kind() == reflect.String {
			m := reflect.MakeMap(v.Type())
			elem := reflect.New(v.Type().Elem()).Elem()
			if elem.Kind() == reflect.Map {
				inner := reflect.MakeMap(elem.Type())
				inner.SetMapIndex(reflect.ValueOf(secretSentinel+"-key"), reflect.ValueOf(any(secretSentinel+"-"+path)))
				elem = inner
			}
			m.SetMapIndex(reflect.ValueOf(secretSentinel+"-"+path), elem)
			v.Set(m)
			*filled = append(*filled, path)
		}
	}
}

// tripwireTransport fails any outbound request made through the default
// client while the redaction test runs.
type tripwireTransport struct{ calls atomic.Int64 }

func (tw *tripwireTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	tw.calls.Add(1)
	return nil, errors.New("status endpoint made an outbound request to " + r.URL.String())
}

// No credential may appear in /health or /version, and neither may reach a
// provider: both are unauthenticated and internet-facing on a default
// deployment. Every string in the config — not a hand-picked list of secret
// fields — carries a sentinel, so a credential field added later is covered
// without editing this test.
func TestStatusEndpointsNeverExposeSecretsOrCallProviders(t *testing.T) {
	var upstreamHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
	}))
	t.Cleanup(upstream.Close)

	tripwire := &tripwireTransport{}
	saved := http.DefaultTransport
	http.DefaultTransport = tripwire
	t.Cleanup(func() { http.DefaultTransport = saved })
	// The test's own requests must not trip it.
	client := &http.Client{Transport: saved.(*http.Transport).Clone()}

	for _, realtime := range []bool{false, true} {
		t.Run(fmt.Sprintf("realtime=%v", realtime), func(t *testing.T) {
			cfg := &config.Config{}
			var filled []string
			fillWithSentinels(reflect.ValueOf(cfg).Elem(), "", upstream.URL, &filled)
			if !realtime {
				cfg.Realtime.Provider = ""
			}

			// The fill must have reached the secrets the issue names, and the
			// endpoint fields, or the test proves nothing.
			for _, secret := range []string{cfg.Server.JWTSecret, cfg.Server.TurnSecret, cfg.Server.APIKey,
				cfg.Grok.APIKey, cfg.Deepgram.APIKey, cfg.OpenAI.APIKey, cfg.Cartesia.APIKey,
				cfg.Agent.APIKey, cfg.Supabase.APIKey, cfg.Pgvector.ConnectionString} {
				if !strings.Contains(secret, secretSentinel) {
					t.Fatalf("a credential field was not filled: %q", secret)
				}
			}
			if !strings.HasPrefix(cfg.Ollama.BaseURL, upstream.URL) {
				t.Fatalf("endpoint field not pointed at the upstream recorder: %q", cfg.Ollama.BaseURL)
			}
			if len(filled) < 50 {
				t.Fatalf("only %d config strings filled; the walk is not reaching the config", len(filled))
			}

			h := newHealthHandler(cfg, time.Now(), func() int { return 1 })
			srv := httptest.NewServer(corsMiddleware(newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil, h, versionHandler(currentBuildInfo()))))
			t.Cleanup(srv.Close)

			for _, probe := range []struct{ path, accept string }{
				{"/health", ""},
				{"/health", "*/*"},
				{"/health", "application/json"},
				{"/version", ""},
				{"/version", "application/json"},
			} {
				req, err := http.NewRequest(http.MethodGet, srv.URL+probe.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if probe.accept != "" {
					req.Header.Set("Accept", probe.accept)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s (Accept %q): status %d", probe.path, probe.accept, resp.StatusCode)
				}
				if strings.Contains(string(body), secretSentinel) {
					t.Errorf("%s (Accept %q) leaked a config value: %s", probe.path, probe.accept, body)
				}
				for name, values := range resp.Header {
					for _, v := range values {
						if strings.Contains(v, secretSentinel) {
							t.Errorf("%s (Accept %q) leaked a config value in header %s: %s", probe.path, probe.accept, name, v)
						}
					}
				}
				if probe.path == "/health" && probe.accept == "application/json" {
					var report healthReport
					if err := json.Unmarshal(body, &report); err != nil {
						t.Fatal(err)
					}
					want := providerNames{STT: "deepgram", LLM: "openai", TTS: "cartesia"}
					if realtime {
						want = providerNames{Realtime: "grok"}
					}
					if report.Providers != want {
						t.Errorf("providers = %+v, want %+v", report.Providers, want)
					}
				}
			}
		})
	}

	if n := upstreamHits.Load(); n != 0 {
		t.Errorf("status endpoints reached a configured provider endpoint %d times", n)
	}
	if n := tripwire.calls.Load(); n != 0 {
		t.Errorf("status endpoints made %d outbound requests", n)
	}
}
