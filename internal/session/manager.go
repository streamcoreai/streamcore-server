package session

import (
	"log"
	"sync"

	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/plugin"
)

type Manager struct {
	cfg            *config.Config
	pluginMgr      *plugin.Manager
	pipelineOptsFn PipelineOptsFunc
	OnRemove       func(sessionID string) // optional callback fired after a session is removed
	mu             sync.RWMutex
	sessions       map[string]*Session
}

func NewManager(cfg *config.Config, pluginMgr *plugin.Manager, pipelineOptsFn PipelineOptsFunc) *Manager {
	return &Manager{
		cfg:            cfg,
		pluginMgr:      pluginMgr,
		pipelineOptsFn: pipelineOptsFn,
		sessions:       make(map[string]*Session),
	}
}

func (m *Manager) GetOrCreate(sessionID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if s, ok := m.sessions[sessionID]; ok {
		return s
	}

	s := NewSession(sessionID, m.cfg, m.pluginMgr, m.pipelineOptsFn)
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
	s, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if ok {
		s.Close()
		log.Printf("[manager] removed session %s", sessionID)
		if m.OnRemove != nil {
			m.OnRemove(sessionID)
		}
	}
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
