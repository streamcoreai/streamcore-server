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
