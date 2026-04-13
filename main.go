package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"encoding/json"

	"github.com/golang-jwt/jwt/v5"
	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/plugin"
	"github.com/streamcoreai/server/internal/rag"
	"github.com/streamcoreai/server/internal/session"
	"github.com/streamcoreai/server/internal/signaling"
)

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("Providers — STT: %s, LLM: %s, TTS: %s", cfg.STT.Provider, cfg.LLM.Provider, cfg.TTS.Provider)

	// Initialize plugin manager
	pluginMgr := plugin.NewManager(cfg.Plugins.Directory)
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Location, ETag")
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

		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"iat": now.Unix(),
			"exp": now.Add(1 * time.Hour).Unix(),
		})

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
		_, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return secretBytes, nil
		})
		if err != nil {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
