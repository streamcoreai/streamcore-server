package session

import (
	"context"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

func testManager(graceMs int) *Manager {
	cfg := &config.Config{}
	cfg.Server.SessionGraceMs = graceMs
	return NewManager(cfg, nil, nil)
}

func TestReapCollectsSessionsIdlePastTheGrace(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("gone")

	// A client that drops off the network never sends DELETE, so the session
	// sits at zero peers with nothing else to collect it.
	if s.PeerCount() != 0 {
		t.Fatalf("PeerCount = %d, want 0", s.PeerCount())
	}

	now := time.Now()
	s.mu.Lock()
	s.idleSince = now.Add(-2 * time.Second)
	s.mu.Unlock()

	m.reap(now)

	if got := m.Get("gone"); got != nil {
		t.Fatal("idle session survived the reap")
	}
	if s.ctx.Err() == nil {
		t.Fatal("reaped session's context was not cancelled")
	}
}

func TestReapKeepsSessionsInsideTheGrace(t *testing.T) {
	// The grace window is the whole point: it is what leaves the door open for
	// an ICE restart or a redial after a transport drop.
	m := testManager(30000)
	s := m.GetOrCreate("recovering")

	now := time.Now()
	s.mu.Lock()
	s.idleSince = now.Add(-5 * time.Second)
	s.mu.Unlock()

	m.reap(now)

	if m.Get("recovering") != s {
		t.Fatal("session inside the grace window was reaped")
	}
	if s.ctx.Err() != nil {
		t.Fatal("session inside the grace window had its context cancelled")
	}
}

func TestReapSkipsSessionsWithPeers(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("busy")

	// Stand in for a live peer without building a PeerConnection: what reap
	// reads is idleSince, which AddPeer zeroes.
	s.mu.Lock()
	s.peers["p1"] = nil
	s.idleSince = time.Time{}
	s.mu.Unlock()

	m.reap(time.Now().Add(time.Hour))

	if m.Get("busy") != s {
		t.Fatal("session with a peer was reaped")
	}
}

func TestRemovePeerStartsTheIdleClock(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("draining")

	s.mu.Lock()
	s.peers["p1"] = nil
	s.peers["p2"] = nil
	s.idleSince = time.Time{}
	s.mu.Unlock()

	s.removePeer("p1")
	if !s.IdleSince().IsZero() {
		t.Fatal("idle clock started while a peer was still connected")
	}

	s.removePeer("p2")
	if s.IdleSince().IsZero() {
		t.Fatal("idle clock did not start when the last peer left")
	}
}

func TestRemoveStaysIdempotent(t *testing.T) {
	// Documented behaviour of the DELETE path — removing twice must not panic
	// or report differently.
	m := testManager(1000)
	m.GetOrCreate("bye")

	m.Remove("bye")
	m.Remove("bye")
	m.Remove("never-existed")

	if m.Get("bye") != nil {
		t.Fatal("session survived Remove")
	}
}

func TestETagRotatesOnRestart(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("abc")

	if want := `"abc"`; s.ETag() != want {
		t.Fatalf("initial ETag = %s, want %s", s.ETag(), want)
	}

	rotated := s.RotateETag()
	if rotated == `"abc"` {
		t.Fatal("ETag did not change after a restart")
	}
	if s.ETag() != rotated {
		t.Fatalf("ETag() = %s, want the rotated %s", s.ETag(), rotated)
	}
}

func TestStartReaperStopsWithContext(t *testing.T) {
	m := testManager(1000)
	ctx, cancel := context.WithCancel(context.Background())
	m.StartReaper(ctx)
	cancel()
	// Nothing to assert beyond "does not panic or leak a ticker" — the sweep
	// itself is covered by the reap tests, which are deterministic.
}
