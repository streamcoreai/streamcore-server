package session

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/rag"
)

// DefaultSessionGraceMs is how long a session with no peers is kept before the
// reaper closes it. Long enough for a client to complete an ICE restart or
// redial after a network change, short enough that an abandoned session is not
// a lasting leak.
const DefaultSessionGraceMs = 30000

// reapIntervalCap bounds how often the sweep runs. A short grace period should
// still not spin the sweep, and a long one should not delay a reap by minutes.
const reapIntervalCap = 10 * time.Second

type Manager struct {
	cfg       *config.Config
	pluginMgr *plugin.Manager
	ragClient rag.Client
	mu        sync.RWMutex
	sessions  map[string]*Session
}

func NewManager(cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) *Manager {
	return &Manager{
		cfg:       cfg,
		pluginMgr: pluginMgr,
		ragClient: ragClient,
		sessions:  make(map[string]*Session),
	}
}

func (m *Manager) GetOrCreate(sessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[sessionID]; ok {
		return s
	}

	s := NewSession(sessionID, m.cfg, m.pluginMgr, m.ragClient)
	m.sessions[sessionID] = s
	log.Printf("[manager] created session %s", sessionID)
	return s
}

func (m *Manager) Get(sessionID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[sessionID]
}

func (m *Manager) Remove(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		s.Close()
		delete(m.sessions, sessionID)
		log.Printf("[manager] removed session %s", sessionID)
	}
}

// StartReaper runs a periodic sweep that closes sessions which have had no
// peers for longer than the grace period, until ctx is cancelled.
//
// Peer teardown only reaches Session.removePeer, and DELETE is the only caller
// of Remove — so without this sweep a client that disappears mid-call (no
// DELETE, because it can't send one) leaves its Session, its context, and
// everything hanging off them alive for the life of the process.
func (m *Manager) StartReaper(ctx context.Context) {
	grace := m.grace()
	interval := grace / 3
	if interval < time.Second {
		interval = time.Second
	}
	if interval > reapIntervalCap {
		interval = reapIntervalCap
	}

	log.Printf("[manager] session reaper started (grace %s, sweep %s)", grace, interval)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				m.reap(now)
			}
		}
	}()
}

// reap closes every session that has been idle since before now-grace.
func (m *Manager) reap(now time.Time) {
	grace := m.grace()

	m.mu.Lock()
	var expired []*Session
	for id, s := range m.sessions {
		idle := s.IdleSince()
		if idle.IsZero() || now.Sub(idle) < grace {
			continue
		}
		expired = append(expired, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	// Closed outside the lock: teardown blocks on DTLS/ICE shutdown and must
	// not stall a concurrent GetOrCreate.
	for _, s := range expired {
		log.Printf("[manager] reaping idle session %s", s.ID)
		s.Close()
	}
}

func (m *Manager) grace() time.Duration {
	ms := DefaultSessionGraceMs
	if m.cfg != nil && m.cfg.Server.SessionGraceMs > 0 {
		ms = m.cfg.Server.SessionGraceMs
	}
	return time.Duration(ms) * time.Millisecond
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.Close()
		delete(m.sessions, id)
	}
	log.Println("[manager] all sessions closed")
}
