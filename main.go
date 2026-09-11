package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/rag"
	"github.com/streamcoreai/streamcore-server/internal/session"
	"github.com/streamcoreai/streamcore-server/internal/signaling"
	turnserver "github.com/streamcoreai/streamcore-server/internal/turn"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	debugSrv, err := startDebugServer(cfg.Debug)
	if err != nil {
		log.Fatalf("debug server: %v", err)
	}

	if cfg.RealtimeEnabled() {
		// Naming the STT/LLM/TTS providers here would be actively
		// misleading: in realtime mode none of them are constructed.
		log.Printf("Provider — speech-to-speech: %s (model: %s, voice: %s)",
			cfg.Realtime.Provider, cfg.Grok.Model, cfg.Grok.Voice)
	} else {
		log.Printf("Providers — STT: %s, LLM: %s, TTS: %s", cfg.STT.Provider, cfg.LLM.Provider, cfg.TTS.Provider)
	}

	// Initialize plugin manager. Settings go in before discovery, since each
	// plugin is handed its own table from config.toml during the handshake.
	pluginMgr := plugin.NewManager(cfg.Plugins.Directory)
	pluginMgr.SetPluginSettings(pluginSettings(cfg))
	if err := pluginMgr.LoadAll(context.Background()); err != nil {
		log.Printf("Warning: plugin loading: %v", err)
	}
	defer pluginMgr.Close()

	// Initialize RAG client (nil if disabled)
	ragClient, err := rag.NewClient(cfg)
	if err != nil {
		log.Fatalf("rag: %v", err)
	}
	if ragClient != nil {
		log.Printf("RAG enabled — provider: %s", cfg.RAG.Provider)
	}

	// Start built-in STUN/TURN server when public_ip and turn_secret are set.
	if cfg.Server.PublicIP != "" && cfg.Server.TurnSecret != "" {
		turnSrv, err := turnserver.Start(cfg.Server.PublicIP, cfg.Server.TurnSecret)
		if err != nil {
			log.Fatalf("turn server: %v", err)
		}
		defer turnSrv.Close()
	}

	sm := session.NewManager(cfg, pluginMgr, ragClient)

	whipHandler := signaling.NewWHIPHandler(sm)
	if cfg.Server.JWTSecret != "" {
		log.Println("JWT authentication enabled for /whip")
		whipHandler = jwtMiddleware(cfg.Server.JWTSecret, whipHandler)
	}

	var issueToken http.HandlerFunc
	if cfg.Server.JWTSecret != "" {
		issueToken = tokenHandler(cfg.Server.JWTSecret, cfg.Server.APIKey)
	}

	handler := corsMiddleware(newPublicMux(whipHandler, issueToken))

	srv := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Sessions are only removed explicitly by DELETE, which a client that
	// dropped off the network can never send. The reaper collects those.
	sm.StartReaper(ctx)

	go func() {
		log.Printf("Voice agent server listening on :%s", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	// Restore default signal behavior so a second Ctrl+C force-kills.
	stop()
	log.Println("Shutting down...")

	// Safety net: force exit after timeout if graceful shutdown stalls.
	go func() {
		time.Sleep(5 * time.Second)
		log.Println("Shutdown timed out, forcing exit")
		os.Exit(1)
	}()

	// Plugins first: a plugin that supervises children of its own — the Codex
	// App Server, say — must take them with it, and the force-exit safety net
	// above is only five seconds away.
	pluginMgr.Close()

	sm.CloseAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	if debugSrv != nil {
		if err := debugSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("debug HTTP shutdown error: %v", err)
		}
	}
	log.Println("Server stopped")
}

func newPublicMux(whipHandler, issueToken http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/whip", whipHandler)
	mux.HandleFunc("/whip/", whipHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	if issueToken != nil {
		mux.HandleFunc("/token", issueToken)
	}
	return mux
}

func newDebugMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func startDebugServer(cfg config.DebugConfig) (*http.Server, error) {
	if cfg.Bind == "" {
		if cfg.BlockProfileRate != 0 || cfg.MutexProfileFraction != 0 {
			log.Printf("Warning: debug profiling rates are ignored because debug.bind is empty")
		}
		return nil, nil
	}
	if cfg.BlockProfileRate < 0 {
		return nil, fmt.Errorf("block_profile_rate must not be negative")
	}
	if cfg.MutexProfileFraction < 0 {
		return nil, fmt.Errorf("mutex_profile_fraction must not be negative")
	}

	listener, err := net.Listen("tcp", cfg.Bind)
	if err != nil {
		return nil, fmt.Errorf("listen on %q: %w", cfg.Bind, err)
	}
	if !cfg.AllowPublic {
		addr, ok := listener.Addr().(*net.TCPAddr)
		if !ok || !addr.IP.IsLoopback() {
			listener.Close()
			return nil, fmt.Errorf("bind %q resolves to a non-loopback address; set debug.allow_public = true to acknowledge public pprof exposure", cfg.Bind)
		}
	}

	runtime.SetBlockProfileRate(cfg.BlockProfileRate)
	runtime.SetMutexProfileFraction(cfg.MutexProfileFraction)

	srv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           newDebugMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go serveDebug(srv, listener)
	return srv, nil
}

// serveDebug runs the pprof listener until it stops.
//
// A failure here must not end the process. pprof is opt-in and optional; the
// media pipeline is neither. log.Fatalf would be os.Exit(1), skipping
// sm.CloseAll() and both graceful shutdowns in main, so an accept error on a
// profiling socket would drop every live WebRTC call with no cleanup. The main
// HTTP server keeps log.Fatalf because the process genuinely cannot do its job
// without that listener; this one it can.
//
// Separate from the goroutine literal so a test can prove it returns.
func serveDebug(srv *http.Server, listener net.Listener) {
	log.Printf("Debug pprof server listening on %s", srv.Addr)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Printf("debug server error: %v; continuing without pprof", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, If-Match")
		// A browser can only read response headers named here. Without the
		// resume pair a JS client cannot see its own resume token, which
		// silently reduces every drop to a fresh conversation.
		w.Header().Set("Access-Control-Expose-Headers", "Location, ETag, Accept-Patch, X-Resume-Token, X-Resume-Status")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenHandler returns an HTTP handler that issues short-lived JWTs.
// Clients call POST /token to get a token before connecting to /whip.
// If apiKey is non-empty, the request must include a matching
// Authorization: Bearer <apiKey> header.
//
// The body may carry {"resource_id": "..."} to bind the token to a caller.
// It is minted here rather than accepted at /whip because this endpoint is
// called by a backend holding the API key, which already knows who its user is.
func tokenHandler(secret, apiKey string) http.HandlerFunc {
	secretBytes := []byte(secret)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Validate API key if configured.
		if apiKey != "" {
			auth := r.Header.Get("Authorization")
			if auth == "" || !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != apiKey {
				http.Error(w, "invalid api key", http.StatusUnauthorized)
				return
			}
		}

		// Most callers want a plain anonymous token and send no body, so a
		// missing or unparseable one is not an error.
		var body struct {
			ResourceID string `json:"resource_id"`
		}
		if r.Body != nil {
			json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body)
		}

		now := time.Now()
		claims := jwt.MapClaims{
			"iat": now.Unix(),
			"exp": now.Add(1 * time.Hour).Unix(),
		}
		if resourceID := strings.TrimSpace(body.ResourceID); resourceID != "" {
			claims["sub"] = resourceID
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

		signed, err := token.SignedString(secretBytes)
		if err != nil {
			http.Error(w, "failed to sign token", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"token": signed})
	}
}

// jwtMiddleware validates a Bearer token in the Authorization header using
// HMAC-SHA256. It wraps a handler and rejects requests with missing or
// invalid tokens with 401 Unauthorized.
//
// A "sub" claim is passed through to the handler as the caller identity, read
// only after the signature checks out.
func jwtMiddleware(secret string, next http.HandlerFunc) http.HandlerFunc {
	secretBytes := []byte(secret)
	return func(w http.ResponseWriter, r *http.Request) {
		// Allow CORS preflight through without auth.
		if r.Method == http.MethodOptions {
			next(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing or malformed Authorization header", http.StatusUnauthorized)
			return
		}

		tokenStr := strings.TrimPrefix(auth, "Bearer ")
		token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secretBytes, nil
		})
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		if subject, err := token.Claims.GetSubject(); err == nil && subject != "" {
			r = r.WithContext(signaling.WithResourceID(r.Context(), subject))
		}

		next(w, r)
	}
}

// pluginSettings is what each plugin is handed at startup.
//
// It folds the [display], [github] and [codex] sections into the settings for
// the plugins those features moved into, so a deployment that configured them
// before they left the binary keeps working without being edited. An explicit
// [plugins.config.<name>] always wins, so the new form is available to anyone
// who wants it.
func pluginSettings(cfg *config.Config) map[string]map[string]any {
	settings := map[string]map[string]any{}
	for name, table := range cfg.Plugins.Config {
		settings[name] = table
	}
	withLegacyDisplaySettings(cfg, settings)
	withLegacyDeveloperSettings(cfg, settings)
	return settings
}

// withLegacyDisplaySettings maps the [display] section onto the projector
// plugin named by display.plugin.
func withLegacyDisplaySettings(cfg *config.Config, settings map[string]map[string]any) {

	name := cfg.Display.Plugin
	if name == "" {
		name = "display-projector"
	}
	if _, explicit := settings[name]; explicit {
		return
	}

	legacy := map[string]any{"enabled": cfg.Display.Enabled}
	if cfg.Display.Projector.TimeoutMs > 0 {
		legacy["timeout_ms"] = cfg.Display.Projector.TimeoutMs
	}
	if cfg.Display.Projector.FastPathMaxChars > 0 {
		legacy["fast_path_max_chars"] = cfg.Display.Projector.FastPathMaxChars
	}
	settings[name] = legacy
}

// withLegacyDeveloperSettings maps the [github] and [codex] sections onto the
// plugins those features became.
//
// They are two plugins now, enabled independently, which is what the two
// sections already described: GitHub without Codex was always a supported
// combination and stays one.
func withLegacyDeveloperSettings(cfg *config.Config, settings map[string]map[string]any) {
	if _, explicit := settings["github"]; !explicit && cfg.GitHub.Enabled {
		settings["github"] = map[string]any{
			"enabled":          true,
			"app_id":           cfg.GitHub.AppID,
			"installation_id":  cfg.GitHub.InstallationID,
			"private_key_path": cfg.GitHub.PrivateKeyPath,
			"repositories":     cfg.GitHub.Repositories,
			"api_base_url":     cfg.GitHub.APIBaseURL,
		}
	}

	if _, explicit := settings["codex"]; !explicit && cfg.Codex.Enabled {
		settings["codex"] = map[string]any{
			"enabled":         true,
			"binary":          cfg.Codex.Binary,
			"model_provider":  cfg.Codex.ModelProvider,
			"model":           cfg.Codex.Model,
			"workspace_root":  cfg.Codex.WorkspaceRoot,
			"turn_timeout_ms": cfg.Codex.TurnTimeoutMs,
			"network_access":  cfg.Codex.NetworkAccess,
			"config":          cfg.Codex.Config,
		}
	}
}
