package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"encoding/json"

	"github.com/golang-jwt/jwt/v5"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/rag"
	"github.com/streamcoreai/streamcore-server/internal/session"
	"github.com/streamcoreai/streamcore-server/internal/signaling"
	"github.com/streamcoreai/streamcore-server/internal/tools"
	turnserver "github.com/streamcoreai/streamcore-server/internal/turn"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if cfg.RealtimeEnabled() {
		// Naming the STT/LLM/TTS providers here would be actively
		// misleading: in realtime mode none of them are constructed.
		log.Printf("Provider — speech-to-speech: %s (model: %s, voice: %s)",
			cfg.Realtime.Provider, cfg.Grok.Model, cfg.Grok.Voice)
	} else {
		log.Printf("Providers — STT: %s, LLM: %s, TTS: %s", cfg.STT.Provider, cfg.LLM.Provider, cfg.TTS.Provider)
	}

	// Initialize plugin manager
	pluginMgr := plugin.NewManager(cfg.Plugins.Directory)
	if err := pluginMgr.LoadAll(context.Background()); err != nil {
		log.Printf("Warning: plugin loading: %v", err)
	}
	defer pluginMgr.Close()

	// Native drivetrain tools for the desktop-car firmware. These are
	// metadata-only — the pipeline intercepts "car.*" calls and writes a
	// data-channel command directly to the device.
	for _, t := range tools.All() {
		pluginMgr.RegisterNative(t)
	}

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

	mux := http.NewServeMux()
	whipHandler := signaling.NewWHIPHandler(sm)
	if cfg.Server.JWTSecret != "" {
		log.Println("JWT authentication enabled for /whip")
		whipHandler = jwtMiddleware(cfg.Server.JWTSecret, whipHandler)
	}
	mux.HandleFunc("/whip", whipHandler)
	mux.HandleFunc("/whip/", whipHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	if cfg.Server.JWTSecret != "" {
		mux.HandleFunc("/token", tokenHandler(cfg.Server.JWTSecret, cfg.Server.APIKey))
	}

	handler := corsMiddleware(mux)

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

	sm.CloseAll()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("Server stopped")
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
