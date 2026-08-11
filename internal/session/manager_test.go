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

func TestGetOrCreateRefusesPastTheSessionCap(t *testing.T) {
	m := testManager(30000)
	m.cfg.Server.MaxSessions = 1

	first := m.GetOrCreate("first")
	if first == nil {
		t.Fatal("first session refused below the cap")
	}
	if m.GetOrCreate("second") != nil {
		t.Fatal("a session was created past max_sessions")
	}
	// An existing session holds no new capacity, so fetching it must still
	// work at the cap — this is what keeps resume exempt.
	if m.GetOrCreate("first") != first {
		t.Fatal("an existing session was refused at the cap")
	}

	m.Remove("first")
	if m.GetOrCreate("second") == nil {
		t.Fatal("removing a session did not free its slot")
	}
}

func TestGetOrCreateUnlimitedWhenCapUnset(t *testing.T) {
	m := testManager(30000)
	for i := 0; i < 50; i++ {
		if m.GetOrCreate(string(rune('a'+i))) == nil {
			t.Fatal("a session was refused with max_sessions unset")
		}
	}
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

func TestResumeTokenReattachesTheSameSession(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("live")

	token := m.IssueResumeToken(s)
	if token == "" {
		t.Fatal("no resume token issued for a resumable session")
	}
	if token == s.ID {
		t.Fatal("resume token is the session ID; it must not be guessable from the resource URL")
	}

	if got := m.Resume(token); got != s {
		t.Fatalf("Resume returned %v, want the original session", got)
	}
}

func TestResumeTokenIsSingleUse(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("live")

	token := m.IssueResumeToken(s)
	if m.Resume(token) != s {
		t.Fatal("first use of the token failed")
	}
	// A replayed token must not reattach anyone to a live conversation.
	if got := m.Resume(token); got != nil {
		t.Fatal("a spent resume token was accepted a second time")
	}
}

func TestIssuingATokenInvalidatesThePrevious(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("live")

	first := m.IssueResumeToken(s)
	second := m.IssueResumeToken(s)
	if first == second {
		t.Fatal("re-issuing returned the same token")
	}
	if got := m.Resume(first); got != nil {
		t.Fatal("the superseded token still resumes")
	}
	if got := m.Resume(second); got != s {
		t.Fatal("the current token does not resume")
	}
}

func TestResumeRejectsUnknownTokens(t *testing.T) {
	m := testManager(1000)
	if got := m.Resume("not-a-real-token"); got != nil {
		t.Fatal("an unknown token resumed a session")
	}
	if got := m.Resume(""); got != nil {
		t.Fatal("an empty token resumed a session")
	}
}

func TestReapedSessionCannotBeResumed(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("gone")
	token := m.IssueResumeToken(s)

	now := time.Now()
	s.mu.Lock()
	s.idleSince = now.Add(-2 * time.Second)
	s.mu.Unlock()
	m.reap(now)

	if got := m.Resume(token); got != nil {
		t.Fatal("a reaped session was resumed; its context is already cancelled")
	}
}

func TestDeletedSessionCannotBeResumed(t *testing.T) {
	m := testManager(1000)
	s := m.GetOrCreate("bye")
	token := m.IssueResumeToken(s)

	m.Remove("bye")

	if got := m.Resume(token); got != nil {
		t.Fatal("a DELETEd session was resumed")
	}
}

func TestRealtimeSessionsAreNotResumable(t *testing.T) {
	// Realtime history lives inside the speech-to-speech provider, so a token
	// would promise continuity the server cannot deliver.
	cfg := &config.Config{}
	cfg.Realtime.Provider = "grok"
	m := NewManager(cfg, nil, nil)
	s := m.GetOrCreate("realtime")

	if s.Resumable() {
		t.Fatal("a realtime session reported itself resumable")
	}
	if token := m.IssueResumeToken(s); token != "" {
		t.Fatalf("issued a resume token %q for a realtime session", token)
	}
}

// convManager builds a manager whose sessions can construct a real
// conversation. The OpenAI client is never called — only its identity across a
// resume is under test.
func convManager() *Manager {
	cfg := &config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.OpenAI.APIKey = "test-key-not-used"
	cfg.Server.SessionGraceMs = 1000
	return NewManager(cfg, nil, nil)
}

func TestConversationIsBuiltOncePerSession(t *testing.T) {
	s := convManager().GetOrCreate("live")

	first, err := s.conversation()
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	second, err := s.conversation()
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if first != second {
		t.Fatal("a second call rebuilt the conversation; history would be lost mid-call")
	}
}

// The point of the whole resume path: the caller reconnects and the agent
// still knows who they are. Asserted on identity — the same LLM client, the
// same transcript log — not merely on "a conversation exists".
func TestResumePreservesTheConversation(t *testing.T) {
	m := convManager()
	s := m.GetOrCreate("live")

	before, err := s.conversation()
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	before.Log.Add("user", "my name is Jason")

	token := m.IssueResumeToken(s)
	resumed := m.Resume(token)
	if resumed != s {
		t.Fatal("Resume did not return the original session")
	}

	after, err := resumed.conversation()
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if after != before {
		t.Fatal("resume rebuilt the conversation state")
	}
	if after.LLM != before.LLM {
		t.Fatal("resume rebuilt the LLM client; the message history would be gone")
	}
	if after.Log != before.Log {
		t.Fatal("resume rebuilt the transcript log")
	}
	if got := after.Log.Len(); got != 1 {
		t.Fatalf("transcript log has %d entries after a resume, want 1", got)
	}
	// The session's context must still be live — a resumed call runs on it.
	if s.ctx.Err() != nil {
		t.Fatal("resume left the session context cancelled")
	}
}

func TestResumeKeepsTheRollingSummary(t *testing.T) {
	m := convManager()
	s := m.GetOrCreate("live")

	conv, err := s.conversation()
	if err != nil {
		t.Fatalf("conversation: %v", err)
	}
	if got := conv.Summary(); got != "" {
		t.Fatalf("new conversation already has a summary: %q", got)
	}

	token := m.IssueResumeToken(s)
	if m.Resume(token) != s {
		t.Fatal("Resume did not return the original session")
	}

	after, _ := s.conversation()
	if after != conv {
		t.Fatal("the rolling summary would restart from empty after a resume")
	}
}
