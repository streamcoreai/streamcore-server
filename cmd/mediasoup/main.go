package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/msbridge"
	"github.com/streamcoreai/server/internal/plugin"
)

// agentEntry tracks a running agent bridge.
type agentEntry struct {
	bridge *msbridge.Bridge
	roomID string
}

func main() {
	cfg, err := config.Load("")
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("Providers — STT: %s, LLM: %s, TTS: %s", cfg.STT.Provider, cfg.LLM.Provider, cfg.TTS.Provider)
	log.Printf("Mediasoup — URL: %s, Room: %s", cfg.Mediasoup.SignalingURL, cfg.Mediasoup.RoomID)

	// Initialize plugin manager
	pluginMgr := plugin.NewManager(cfg.Plugins.Directory)
	if err := pluginMgr.LoadAll(context.Background()); err != nil {
		log.Printf("Warning: plugin loading: %v", err)
	}
	defer pluginMgr.Close()

	// Track active agents.
	var (
		agentsMu sync.Mutex
		agents   = make(map[string]*agentEntry)
		agentSeq int
	)

	mux := http.NewServeMux()

	// POST /dispatch — start an agent in a mediasoup room
	// Body: { "roomId": "dev" }  (optional, defaults to config)
	// Response: { "agentId": "agent-1", "roomId": "dev" }
	mux.HandleFunc("/dispatch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body struct {
				RoomID string `json:"roomId"`
			}
			if r.Body != nil {
				json.NewDecoder(r.Body).Decode(&body)
			}
			roomID := body.RoomID
			if roomID == "" {
				roomID = cfg.Mediasoup.RoomID
			}

			bridge, err := msbridge.NewBridge(context.Background(), cfg, pluginMgr, roomID)
			if err != nil {
				http.Error(w, fmt.Sprintf("failed to start agent: %v", err), http.StatusInternalServerError)
				return
			}

			agentsMu.Lock()
			agentSeq++
			agentID := fmt.Sprintf("agent-%d", agentSeq)
			agents[agentID] = &agentEntry{bridge: bridge, roomID: roomID}
			agentsMu.Unlock()

			log.Printf("[dispatch] started agent %s in room %s", agentID, roomID)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"agentId": agentID,
				"roomId":  roomID,
			})
			return
		}

		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	// DELETE /dispatch/{agentId} — stop an agent
	mux.HandleFunc("/dispatch/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			agentID := strings.TrimPrefix(r.URL.Path, "/dispatch/")
			if agentID == "" {
				http.Error(w, "missing agentId", http.StatusBadRequest)
				return
			}

			agentsMu.Lock()
			entry, ok := agents[agentID]
			if ok {
				delete(agents, agentID)
			}
			agentsMu.Unlock()

			if !ok {
				http.Error(w, "agent not found", http.StatusNotFound)
				return
			}

			entry.bridge.Close()
			log.Printf("[dispatch] stopped agent %s", agentID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

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
		log.Printf("Voice agent server (mediasoup) listening on :%s", cfg.Server.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Println("Shutting down...")

	go func() {
		time.Sleep(5 * time.Second)
		log.Println("Shutdown timed out, forcing exit")
		os.Exit(1)
	}()

	// Close all agents.
	agentsMu.Lock()
	for id, entry := range agents {
		entry.bridge.Close()
		delete(agents, id)
	}
	agentsMu.Unlock()

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
