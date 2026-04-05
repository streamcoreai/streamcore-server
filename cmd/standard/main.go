package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/plugin"
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

	sm := session.NewManager(cfg, pluginMgr, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("/whip", signaling.NewWHIPHandler(sm))
	mux.HandleFunc("/whip/", signaling.NewWHIPHandler(sm))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler := corsMiddleware(mux)

	srv := &http.Server{
		Addr:    ":" + cfg.Server.Port,
		Handler: handler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Voice agent server (standard) listening on :%s", cfg.Server.Port)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "Location, ETag")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
