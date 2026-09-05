package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/signaling"
)

// mintToken drives tokenHandler and returns the signed JWT it issued.
func mintToken(t *testing.T, secret, apiKey, body string) string {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/token", reader)
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	tokenHandler(secret, apiKey)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("token request: status %d, body %q", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	return out.Token
}

// resourceSeenBy runs a token through jwtMiddleware and reports what the
// wrapped handler saw.
func resourceSeenBy(t *testing.T, secret, token string) string {
	t.Helper()

	var seen string
	req := httptest.NewRequest(http.MethodPost, "/whip", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	jwtMiddleware(secret, func(w http.ResponseWriter, r *http.Request) {
		seen = signaling.ResourceIDFromContext(r.Context())
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("middleware rejected a token it minted: status %d", rec.Code)
	}
	return seen
}

func TestTokenCarriesTheRequestedResourceID(t *testing.T) {
	const secret = "test-secret"

	token := mintToken(t, secret, "", `{"resource_id":"user_8891"}`)
	if got := resourceSeenBy(t, secret, token); got != "user_8891" {
		t.Fatalf("resource id = %q, want user_8891", got)
	}
}

// The common case — the SDKs send no body at all.
func TestTokenWithoutAResourceIDYieldsNoIdentity(t *testing.T) {
	const secret = "test-secret"

	for _, body := range []string{"", "{}", "not json at all"} {
		token := mintToken(t, secret, "", body)
		if got := resourceSeenBy(t, secret, token); got != "" {
			t.Fatalf("body %q: resource id = %q, want empty", body, got)
		}
	}
}

// A whitespace-only resource_id would otherwise be forwarded as if it
// identified someone.
func TestBlankResourceIDIsNotMintedAsAClaim(t *testing.T) {
	const secret = "test-secret"

	token := mintToken(t, secret, "", `{"resource_id":"   "}`)
	if got := resourceSeenBy(t, secret, token); got != "" {
		t.Fatalf("resource id = %q, want empty", got)
	}
}

// Wrong signature: reject the token instead of reading its claims.
func TestForgedTokenIsRejectedBeforeItsClaimsAreRead(t *testing.T) {
	forged := mintToken(t, "attacker-secret", "", `{"resource_id":"admin"}`)

	var reached bool
	req := httptest.NewRequest(http.MethodPost, "/whip", nil)
	req.Header.Set("Authorization", "Bearer "+forged)
	rec := httptest.NewRecorder()
	jwtMiddleware("real-secret", func(w http.ResponseWriter, r *http.Request) {
		reached = true
	})(rec, req)

	if reached {
		t.Fatal("a token signed with the wrong secret reached the handler")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// With an api_key configured, you need it to mint anything at all.
func TestResourceIDCannotBeMintedWithoutTheAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/token", strings.NewReader(`{"resource_id":"user_8891"}`))
	rec := httptest.NewRecorder()
	tokenHandler("test-secret", "the-api-key")(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestPublicMuxDoesNotServePprof(t *testing.T) {
	mux := newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil)
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestDebugServerServesPprofOnLoopback(t *testing.T) {
	srv, err := startDebugServer(config.DebugConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startDebugServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown debug server: %v", err)
		}
	})

	resp, err := http.Get("http://" + srv.Addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET pprof index: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestDebugMuxRegistersEveryPprofRoute(t *testing.T) {
	mux := newDebugMux()
	tests := []struct {
		path        string
		wantPattern string
	}{
		{path: "/debug/pprof/", wantPattern: "/debug/pprof/"},
		{path: "/debug/pprof/goroutine", wantPattern: "/debug/pprof/"},
		{path: "/debug/pprof/cmdline", wantPattern: "/debug/pprof/cmdline"},
		{path: "/debug/pprof/profile", wantPattern: "/debug/pprof/profile"},
		{path: "/debug/pprof/symbol", wantPattern: "/debug/pprof/symbol"},
		{path: "/debug/pprof/trace", wantPattern: "/debug/pprof/trace"},
		{path: "/unrelated", wantPattern: ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			_, pattern := mux.Handler(req)
			if pattern != tt.wantPattern {
				t.Fatalf("pattern = %q, want %q", pattern, tt.wantPattern)
			}
		})
	}
}

func TestDebugServerDoesNotServeDefaultMuxHandlers(t *testing.T) {
	previousDefaultMux := http.DefaultServeMux
	http.DefaultServeMux = http.NewServeMux()
	t.Cleanup(func() {
		http.DefaultServeMux = previousDefaultMux
	})
	http.HandleFunc("/unrelated-default-handler", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	srv, err := startDebugServer(config.DebugConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startDebugServer: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("shutdown debug server: %v", err)
		}
	})

	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + srv.Addr + "/unrelated-default-handler")
	if err != nil {
		t.Fatalf("GET unrelated default handler: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestDebugServerRejectsPublicBindWithoutAcknowledgement(t *testing.T) {
	_, err := startDebugServer(config.DebugConfig{Bind: "0.0.0.0:0"})
	if err == nil {
		t.Fatal("public debug bind was accepted without allow_public")
	}
	if !strings.Contains(err.Error(), "debug.allow_public = true") {
		t.Fatalf("error = %q, want allow_public guidance", err)
	}
}

// failingListener returns a permanent Accept error, the shape of a listener
// that has died under a running server. net/http retries temporary errors, so
// a plain error is what actually terminates Serve.
type failingListener struct {
	addr net.Addr
	err  error
}

func (l *failingListener) Accept() (net.Conn, error) { return nil, l.err }
func (l *failingListener) Close() error              { return nil }
func (l *failingListener) Addr() net.Addr            { return l.addr }

// A dead pprof listener must not take the process down with it.
//
// This is the whole point of the change: log.Fatalf is os.Exit(1), which skips
// sm.CloseAll() and both graceful shutdowns, so an accept error on an optional
// profiling socket would drop every live WebRTC call. The assertion is that
// serveDebug *returns* — under the old code this test does not fail, it kills
// the test binary, which is exactly the failure mode being fixed.
func TestServeDebugSurvivesAFailedListener(t *testing.T) {
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:6060")
	if err != nil {
		t.Fatalf("resolve addr: %v", err)
	}
	srv := &http.Server{Addr: addr.String(), Handler: newDebugMux()}
	listener := &failingListener{addr: addr, err: errors.New("accept: bad file descriptor")}

	done := make(chan struct{})
	go func() {
		defer close(done)
		serveDebug(srv, listener)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serveDebug did not return after a permanent Accept error")
	}
}

// The main pipeline keeps serving after pprof dies.
//
// The previous test proves serveDebug returns; on its own that is satisfied by
// a serveDebug nobody calls. This one drives the real startDebugServer path,
// kills the pprof listener underneath it, and then requires the public mux to
// still answer /health — the observable claim an operator cares about.
func TestPublicMuxStillServesAfterTheDebugListenerDies(t *testing.T) {
	debugSrv, err := startDebugServer(config.DebugConfig{Bind: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("startDebugServer: %v", err)
	}
	if debugSrv == nil {
		t.Fatal("startDebugServer returned no server for a non-empty bind")
	}

	public := httptest.NewServer(newPublicMux(func(http.ResponseWriter, *http.Request) {}, nil))
	t.Cleanup(public.Close)

	// Close the debug server out from under its own Serve loop. Serve then
	// returns ErrServerClosed, the same return path a fatal accept error takes.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := debugSrv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown debug server: %v", err)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(public.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health after the debug listener stopped: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// Display projection moved out of the binary into a plugin. A deployment that
// configured it through [display] must keep working without being edited.
func TestLegacyDisplayConfigReachesTheProjectorPlugin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Display.Enabled = true
	cfg.Display.Plugin = "display-projector"
	cfg.Display.Projector.TimeoutMs = 2500
	cfg.Display.Projector.FastPathMaxChars = 60

	settings := pluginSettings(cfg)["display-projector"]
	if settings["enabled"] != true {
		t.Errorf("enabled = %v", settings["enabled"])
	}
	if settings["timeout_ms"] != 2500 {
		t.Errorf("timeout_ms = %v", settings["timeout_ms"])
	}
	if settings["fast_path_max_chars"] != 60 {
		t.Errorf("fast_path_max_chars = %v", settings["fast_path_max_chars"])
	}
}

// A deployment that never asked for projection must not get it, which is what
// keeps the plugin dormant even though it ships in the repo.
func TestProjectorStaysOffWhenDisplayWasNeverEnabled(t *testing.T) {
	cfg := &config.Config{}
	settings := pluginSettings(cfg)["display-projector"]
	if settings["enabled"] != false {
		t.Errorf("enabled = %v, want false", settings["enabled"])
	}
}

// The new form wins, so nobody is stuck with the legacy section once they move.
func TestExplicitPluginConfigOverridesLegacyDisplaySection(t *testing.T) {
	cfg := &config.Config{}
	cfg.Display.Enabled = false
	cfg.Plugins.Config = map[string]map[string]any{
		"display-projector": {"enabled": true, "fast_path_max_chars": 120},
	}

	settings := pluginSettings(cfg)["display-projector"]
	if settings["enabled"] != true || settings["fast_path_max_chars"] != 120 {
		t.Errorf("settings = %+v", settings)
	}
}

// display.plugin names which plugin receives the section, so an operator who
// wrote their own projector still has it configured.
func TestLegacyDisplaySectionFollowsDisplayPlugin(t *testing.T) {
	cfg := &config.Config{}
	cfg.Display.Enabled = true
	cfg.Display.Plugin = "my-projector"

	settings := pluginSettings(cfg)
	if _, ok := settings["my-projector"]; !ok {
		t.Fatalf("settings = %+v", settings)
	}
	if _, ok := settings["display-projector"]; ok {
		t.Errorf("configured a plugin the operator did not name: %+v", settings)
	}
}

// The developer agent moved out of the binary into a plugin. A deployment that
// configured it through [github] and [codex] must keep working unedited.
func TestLegacyDeveloperConfigReachesThePlugin(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Enabled = true
	cfg.GitHub.AppID = "Iv1.abc"
	cfg.GitHub.Repositories = []string{"acme/api"}
	cfg.Codex.Enabled = true
	cfg.Codex.Model = "gpt-5.6-terra"
	cfg.Codex.TurnTimeoutMs = 600000

	settings := pluginSettings(cfg)["developer"]
	if settings["enabled"] != true {
		t.Fatalf("developer plugin not enabled: %+v", settings)
	}

	gh := settings["github"].(map[string]any)
	if gh["enabled"] != true || gh["app_id"] != "Iv1.abc" {
		t.Errorf("github settings = %+v", gh)
	}
	codex := settings["codex"].(map[string]any)
	if codex["enabled"] != true || codex["model"] != "gpt-5.6-terra" {
		t.Errorf("codex settings = %+v", codex)
	}
}

// GitHub without Codex was a supported combination and has to stay one.
func TestGitHubAloneStillEnablesTheDeveloperPlugin(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Enabled = true

	settings := pluginSettings(cfg)["developer"]
	if settings["enabled"] != true {
		t.Fatalf("developer plugin not enabled: %+v", settings)
	}
	if settings["codex"].(map[string]any)["enabled"] != false {
		t.Errorf("codex was enabled without being asked for: %+v", settings["codex"])
	}
}

// A deployment that asked for neither must not get a developer agent.
func TestDeveloperPluginStaysOffWhenNeitherHalfWasEnabled(t *testing.T) {
	if settings, ok := pluginSettings(&config.Config{})["developer"]; ok {
		t.Errorf("developer configured unasked: %+v", settings)
	}
}
