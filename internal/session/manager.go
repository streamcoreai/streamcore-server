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
	// resumable indexes live sessions by their current resume token, so a
	// redialling client can be reattached to the conversation it lost without
	// the session ID ever being usable as a credential.
	resumable map[string]*Session
}

func NewManager(cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) *Manager {
	return &Manager{
		cfg:       cfg,
		pluginMgr: pluginMgr,
		ragClient: ragClient,
		sessions:  make(map[string]*Session),
		resumable: make(map[string]*Session),
	}
}

// IssueResumeToken mints a fresh resume token for a session and indexes it,
// invalidating any previous one. Returns "" when the session holds nothing a
// resume could preserve, so callers never advertise continuity the server
// cannot deliver.
//
// Rotating on every issue makes the token single-use: a stolen or logged token
// stops working the moment the legitimate client uses it, and a replay lands
// on a token the index no longer knows.
func (m *Manager) IssueResumeToken(s *Session) string {
	if s == nil || !s.Resumable() {
		return ""
	}

	old, current, err := s.rotateResumeToken()
	if err != nil {
		// Without a token the call still works; it just cannot be resumed.
		log.Printf("[manager] could not issue resume token for %s: %v", s.ID, err)
		return ""
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if old != "" {
		delete(m.resumable, old)
	}
	m.resumable[current] = s
	return current
}

// Resume looks up the session a token belongs to and detaches the token so it
// cannot be replayed. Returns nil for an unknown, already-used, or reaped
// token — the caller should then treat the request as a brand-new session.
func (m *Manager) Resume(token string) *Session {
	if token == "" {
		return nil
	}

	m.mu.Lock()
	s, ok := m.resumable[token]
	if ok {
		delete(m.resumable, token)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}

	// The connection being resumed from is gone, but its peer may still be
	// registered — it never got the chance to send DELETE. Clear it before
	// the new one arrives.
	s.evictPeers()
	return s
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
		m.dropResumeTokenLocked(s)
		log.Printf("[manager] removed session %s", sessionID)
	}
}

// dropResumeTokenLocked un-indexes a session's resume token so a closed
// conversation can never be reattached to. Caller must hold m.mu.
func (m *Manager) dropResumeTokenLocked(s *Session) {
	if token := s.ResumeToken(); token != "" {
		delete(m.resumable, token)
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
		m.dropResumeTokenLocked(s)
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
	clear(m.resumable)
	log.Println("[manager] all sessions closed")
}
