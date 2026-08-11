package signaling

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/peer"
	"github.com/streamcoreai/streamcore-server/internal/session"
)

const restartFragment = "a=ice-ufrag:newU\r\n" +
	"a=ice-pwd:newPassword111111111\r\n" +
	"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
	"a=mid:0\r\n" +
	"a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host\r\n"

func testHandler(t *testing.T) (http.HandlerFunc, *session.Manager) {
	t.Helper()
	sm := session.NewManager(&config.Config{}, nil, nil)
	t.Cleanup(sm.CloseAll)
	return NewWHIPHandler(sm), sm
}

func patch(t *testing.T, h http.HandlerFunc, path, contentType, ifMatch, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestPatchWithoutSessionID(t *testing.T) {
	h, _ := testHandler(t)
	rec := patch(t, h, "/whip", peer.ICEFragmentContentType, "", restartFragment)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPatchRejectsWrongContentType(t *testing.T) {
	h, sm := testHandler(t)
	sm.GetOrCreate("s1")

	rec := patch(t, h, "/whip/s1", "application/sdp", "", restartFragment)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if got := rec.Header().Get("Accept-Patch"); got != peer.ICEFragmentContentType {
		t.Fatalf("Accept-Patch = %q, want %q", got, peer.ICEFragmentContentType)
	}
}

func TestPatchUnknownSession(t *testing.T) {
	h, _ := testHandler(t)
	rec := patch(t, h, "/whip/nope", peer.ICEFragmentContentType, "", restartFragment)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPatchStaleIfMatchIsRejected(t *testing.T) {
	h, sm := testHandler(t)
	s := sm.GetOrCreate("s1")

	// The client is patching the ICE session that a previous restart replaced.
	s.RotateETag()

	rec := patch(t, h, "/whip/s1", peer.ICEFragmentContentType, `"s1"`, restartFragment)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("status = %d, want 412", rec.Code)
	}
	if got := rec.Header().Get("ETag"); got != s.ETag() {
		t.Fatalf("ETag = %q, want the current %q", got, s.ETag())
	}
}

func TestPatchMatchingIfMatchPassesThePrecondition(t *testing.T) {
	h, sm := testHandler(t)
	s := sm.GetOrCreate("s1")

	// No peer was ever added, so this stops at the peer lookup — the point is
	// that a matching ETag does not trip the precondition.
	rec := patch(t, h, "/whip/s1", peer.ICEFragmentContentType, s.ETag(), restartFragment)
	if rec.Code == http.StatusPreconditionFailed {
		t.Fatal("a matching If-Match was rejected")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (session has no peer)", rec.Code)
	}
}

func TestPatchWildcardIfMatchPassesThePrecondition(t *testing.T) {
	h, sm := testHandler(t)
	s := sm.GetOrCreate("s1")
	s.RotateETag()

	rec := patch(t, h, "/whip/s1", peer.ICEFragmentContentType, "*", restartFragment)
	if rec.Code == http.StatusPreconditionFailed {
		t.Fatal("If-Match: * was rejected")
	}
}

func TestPatchTrickleOnlyIsDeclined(t *testing.T) {
	h, sm := testHandler(t)
	s := sm.GetOrCreate("s1")
	if _, err := s.AddPeer("s1", ""); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// Candidates but no credentials: a trickle update. Trickle ICE is optional
	// per RFC 9725 §4.4.1 and 405 is how a server declines it.
	trickle := "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"a=mid:0\r\n" +
		"a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host\r\n"

	rec := patch(t, h, "/whip/s1", peer.ICEFragmentContentType, "", trickle)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Accept-Patch"); got != peer.ICEFragmentContentType {
		t.Fatalf("Accept-Patch = %q, want %q", got, peer.ICEFragmentContentType)
	}
}

func TestPatchOnPeerWithoutNegotiation(t *testing.T) {
	h, sm := testHandler(t)
	s := sm.GetOrCreate("s1")
	if _, err := s.AddPeer("s1", ""); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	// A well-formed restart against a peer that never completed offer/answer
	// has nothing to restart, which is a conflict rather than a server fault.
	rec := patch(t, h, "/whip/s1", peer.ICEFragmentContentType, "", restartFragment)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestOptionsAdvertisesICERestart(t *testing.T) {
	h, _ := testHandler(t)
	req := httptest.NewRequest(http.MethodOptions, "/whip", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Accept-Post"); got != "application/sdp" {
		t.Fatalf("Accept-Post = %q", got)
	}
	if got := rec.Header().Get("Accept-Patch"); got != peer.ICEFragmentContentType {
		t.Fatalf("Accept-Patch = %q, want %q", got, peer.ICEFragmentContentType)
	}
}

func TestDeleteStaysIdempotent(t *testing.T) {
	h, sm := testHandler(t)
	sm.GetOrCreate("s1")

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodDelete, "/whip/s1", nil)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE #%d status = %d, want 200", i+1, rec.Code)
		}
	}
}

func TestUnsupportedMethodStillReturns405(t *testing.T) {
	h, _ := testHandler(t)
	req := httptest.NewRequest(http.MethodPut, "/whip/s1", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

// postOffer drives the POST path with a minimal offer. The offer is not
// negotiable, so these assert on the resume protocol around it, not on media.
func postOffer(t *testing.T, h http.HandlerFunc, query string) *httptest.ResponseRecorder {
	t.Helper()
	offer := "v=0\r\n" +
		"o=- 1 2 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"a=group:BUNDLE 0\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"c=IN IP4 0.0.0.0\r\n" +
		"a=mid:0\r\n" +
		"a=ice-ufrag:testUfrag\r\n" +
		"a=ice-pwd:testPasswordAtLeast22Chars\r\n" +
		"a=fingerprint:sha-256 " +
		"11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:" +
		"11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00\r\n" +
		"a=setup:actpass\r\n" +
		"a=rtpmap:111 opus/48000/2\r\n" +
		"a=sendrecv\r\n"
	req := httptest.NewRequest(http.MethodPost, "/whip"+query, strings.NewReader(offer))
	req.Header.Set("Content-Type", "application/sdp")
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestPostIssuesAResumeToken(t *testing.T) {
	h, _ := testHandler(t)
	rec := postOffer(t, h, "")

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Resume-Status"); got != "new" {
		t.Fatalf("X-Resume-Status = %q, want \"new\"", got)
	}
	token := rec.Header().Get("X-Resume-Token")
	if token == "" {
		t.Fatal("no X-Resume-Token on a fresh session")
	}
	// The token must not be derivable from the session URL, which is public.
	sessionID := strings.TrimPrefix(rec.Header().Get("Location"), "/whip/")
	if token == sessionID {
		t.Fatal("resume token equals the session ID")
	}
}

func TestResumeReattachesToTheSameSession(t *testing.T) {
	h, sm := testHandler(t)

	first := postOffer(t, h, "")
	token := first.Header().Get("X-Resume-Token")
	original := strings.TrimPrefix(first.Header().Get("Location"), "/whip/")

	second := postOffer(t, h, "?resume="+token)
	if second.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", second.Code)
	}
	if got := second.Header().Get("X-Resume-Status"); got != "resumed" {
		t.Fatalf("X-Resume-Status = %q, want \"resumed\"", got)
	}
	// Same session URL — the conversation, not just the transport, continues.
	if got := strings.TrimPrefix(second.Header().Get("Location"), "/whip/"); got != original {
		t.Fatalf("resumed into session %q, want the original %q", got, original)
	}
	if sm.Get(original) == nil {
		t.Fatal("the original session is gone after a resume")
	}
	// A fresh token is issued so the spent one cannot be replayed.
	if next := second.Header().Get("X-Resume-Token"); next == "" || next == token {
		t.Fatalf("expected a rotated resume token, got %q", next)
	}
}

func TestExpiredResumeTokenStartsANewSessionAndSaysSo(t *testing.T) {
	h, _ := testHandler(t)

	first := postOffer(t, h, "")
	original := strings.TrimPrefix(first.Header().Get("Location"), "/whip/")

	// A redial with a token the server no longer knows must still produce a
	// working call — but must never pretend the conversation survived.
	rec := postOffer(t, h, "?resume=stale-token-that-was-never-issued")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if got := rec.Header().Get("X-Resume-Status"); got != "expired" {
		t.Fatalf("X-Resume-Status = %q, want \"expired\"", got)
	}
	if got := strings.TrimPrefix(rec.Header().Get("Location"), "/whip/"); got == original {
		t.Fatal("an unknown token reattached to an existing session")
	}
}

func TestSpentResumeTokenCannotBeReplayed(t *testing.T) {
	h, _ := testHandler(t)

	first := postOffer(t, h, "")
	token := first.Header().Get("X-Resume-Token")

	if got := postOffer(t, h, "?resume="+token).Header().Get("X-Resume-Status"); got != "resumed" {
		t.Fatalf("first resume: X-Resume-Status = %q, want \"resumed\"", got)
	}
	// Replaying it must not grant a second attachment to a live conversation.
	if got := postOffer(t, h, "?resume="+token).Header().Get("X-Resume-Status"); got != "expired" {
		t.Fatalf("replayed token: X-Resume-Status = %q, want \"expired\"", got)
	}
}

func TestResumeRetiresThePreDropETag(t *testing.T) {
	h, _ := testHandler(t)

	first := postOffer(t, h, "")
	oldETag := first.Header().Get("ETag")
	token := first.Header().Get("X-Resume-Token")

	second := postOffer(t, h, "?resume="+token)
	newETag := second.Header().Get("ETag")

	// The conversation carried over; the ICE session did not. A PATCH still
	// carrying the pre-drop tag is aimed at a peer that no longer exists.
	if newETag == oldETag {
		t.Fatalf("ETag %q survived the resume", newETag)
	}

	rec := patch(t, h, second.Header().Get("Location"), peer.ICEFragmentContentType, oldETag, restartFragment)
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale ETag after a resume: status = %d, want 412", rec.Code)
	}
}
