package session

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/peer"
	"github.com/streamcoreai/streamcore-server/internal/pipeline"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/rag"
)

type Session struct {
	ID        string
	cfg       *config.Config
	pluginMgr *plugin.Manager
	ragClient rag.Client
	ctx       context.Context
	cancel    context.CancelFunc

	mu    sync.Mutex
	peers map[string]*peer.Peer
	// idleSince is when the session last dropped to zero peers, or the zero
	// time while it has any. The Manager reaps sessions that stay idle past
	// the grace period — without it, a client that vanishes without sending
	// DELETE (which is exactly what a dropped connection is) leaves an empty
	// Session in the map forever.
	idleSince time.Time
	// etag identifies the current ICE session (RFC 9725 §4.3.1). It rotates on
	// every successful ICE restart so a client racing two restarts can tell
	// which generation it is patching.
	etag string
}

func NewSession(id string, cfg *config.Config, pluginMgr *plugin.Manager, ragClient rag.Client) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		ID:        id,
		cfg:       cfg,
		pluginMgr: pluginMgr,
		ragClient: ragClient,
		ctx:       ctx,
		cancel:    cancel,
		peers:     make(map[string]*peer.Peer),
		// A session that is created and never gets a peer — a POST that fails
		// between GetOrCreate and AddPeer — is idle from birth.
		idleSince: time.Now(),
		etag:      `"` + id + `"`,
	}
}

// AddPeer creates a new Pion peer and launches a goroutine that waits for
// the remote audio track to arrive, then builds and starts the channel-based
// pipeline. Event messages are delivered via the peer's DataChannel.
func (s *Session) AddPeer(peerID string, direction string) (*peer.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	turnCfg := peer.TURNConfig{
		PublicIP: s.cfg.Server.PublicIP,
		Secret:   s.cfg.Server.TurnSecret,
	}
	p, err := peer.New(s.ctx, peerID, s.cfg.Server.PublicIP, turnCfg)
	if err != nil {
		return nil, err
	}

	p.OnClose = func() {
		s.removePeer(peerID)
	}

	s.peers[peerID] = p
	s.idleSince = time.Time{}

	// Wait for the remote track to arrive, then start the pipeline.
	go func() {
		var pl *pipeline.Pipeline

		select {
		case remoteTrack := <-p.RemoteTrackCh:
			log.Printf("[session:%s] remote track ready, starting pipeline", s.ID)

			var err error
			pl, err = pipeline.New(p.Context(), s.cfg, remoteTrack, p.LocalTrack(), p.SendEvent, s.pluginMgr, s.ragClient, direction)
			if err != nil {
				log.Printf("[session:%s] pipeline create error: %v", s.ID, err)
				p.Close()
				return
			}

			// Route incoming data channel messages to the pipeline (e.g. image chunks).
			p.OnDataChannelMessage = func(msg string) {
				pl.HandleDataChannelMessage(msg)
			}
		case <-s.ctx.Done():
			return
		}

		// Start blocks until the pipeline context is cancelled.
		pl.Start()
	}()

	return p, nil
}

func (s *Session) GetPeer(peerID string) *peer.Peer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers[peerID]
}

func (s *Session) removePeer(peerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.peers, peerID)
	if len(s.peers) == 0 {
		s.idleSince = time.Now()
	}
	log.Printf("[session:%s] peer %s removed, %d peers remaining", s.ID, peerID, len(s.peers))
}

func (s *Session) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
}

// IdleSince returns when the session last dropped to zero peers, or the zero
// time if it still has any. The grace period it starts is what keeps the door
// open for an ICE restart or a client redial after a transport drop.
func (s *Session) IdleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleSince
}

// ETag returns the current ICE-session ETag, quoted and ready for the header.
func (s *Session) ETag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.etag
}

// RotateETag issues a new ICE-session ETag and returns it. Called after a
// successful ICE restart: the previous tag now refers to a generation that no
// longer exists, so a PATCH still carrying it is stale.
func (s *Session) RotateETag() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.etag = `"` + uuid.NewString() + `"`
	return s.etag
}

func (s *Session) Close() {
	s.cancel()

	// Collect peers and clear the map before closing them.
	// p.Close() calls p.OnClose → s.removePeer which locks s.mu,
	// so we must NOT hold s.mu while closing peers.
	s.mu.Lock()
	peers := make([]*peer.Peer, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.peers = make(map[string]*peer.Peer)
	s.mu.Unlock()

	for _, p := range peers {
		p.Close()
	}
	log.Printf("[session:%s] closed", s.ID)
}
