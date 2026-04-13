package session

import (
	"context"
	"log"
	"sync"

	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/peer"
	"github.com/streamcoreai/server/internal/pipeline"
	"github.com/streamcoreai/server/internal/plugin"
	"github.com/streamcoreai/server/internal/rag"
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
	}
}

// AddPeer creates a new Pion peer and launches a goroutine that waits for
// the remote audio track to arrive, then builds and starts the channel-based
// pipeline. Event messages are delivered via the peer's DataChannel.
func (s *Session) AddPeer(peerID string, direction string) (*peer.Peer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, err := peer.New(s.ctx, peerID)
	if err != nil {
		return nil, err
	}

	p.OnClose = func() {
		s.removePeer(peerID)
	}

	s.peers[peerID] = p

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
	log.Printf("[session:%s] peer %s removed, %d peers remaining", s.ID, peerID, len(s.peers))
}

func (s *Session) PeerCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.peers)
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
