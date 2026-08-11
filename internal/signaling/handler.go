package signaling

import (
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/streamcoreai/streamcore-server/internal/peer"
	"github.com/streamcoreai/streamcore-server/internal/session"
)

// NewWHIPHandler returns an HTTP handler implementing WHIP signaling per
// RFC 9725. It handles two URL patterns:
//
//	POST   /whip                   – Session setup (SDP offer/answer), returns sessionId
//	PATCH  /whip/{sessionId}       – ICE restart (application/trickle-ice-sdpfrag)
//	DELETE /whip/{sessionId}       – Session teardown
//	OPTIONS (any)                  – CORS preflight
//
// Session creation is rate limited per client IP. /whip is unauthenticated by
// default, and each POST builds a peer connection and gathers ICE, so an
// unthrottled endpoint lets one client burn CPU and ports at will.
// defaultWhipRateLimit caps session-creation POSTs per client IP per minute.
// Generous for humans — a browser session is one POST — while throttling an
// abusive burst.
const defaultWhipRateLimit = 30

// defaultRestartRateLimit caps ICE restarts per client IP per minute, on its
// own bucket so a client recovering from a flapping network never eats into
// its budget for opening new sessions. A restart re-gathers candidates, so it
// is not free, but a device hopping networks legitimately fires several.
const defaultRestartRateLimit = 60

// maxICEFragmentBytes bounds a PATCH body. An sdpfrag is credentials plus a
// handful of candidate lines; anything near this is not one.
const maxICEFragmentBytes = 64 * 1024

// atCapacityRetryAfterS is the Retry-After sent with a 503 when
// server.max_sessions is reached. Calls last minutes, so there is no point
// hammering — but long enough that a slot opening is plausible.
const atCapacityRetryAfterS = 10

// Session resume — a StreamCore extension to RFC 9725, not part of the RFC.
//
// ICE restart covers a transport that is merely broken. It cannot cover one
// that is gone: past roughly 25 seconds the peer has been closed and there is
// nothing left to restart. Resume covers that case, and every client that
// cannot perform an ICE restart at all, by letting a fresh POST reattach to
// the conversation the previous connection was having.
const (
	// resumeTokenHeader carries the credential for the *next* resume. It is
	// re-issued on every response, so possession of an old one is worthless.
	resumeTokenHeader = "X-Resume-Token"

	// resumeStatusHeader tells the client which of these it got, because a
	// silent fallback to a blank conversation is the exact failure the token
	// exists to prevent.
	resumeStatusHeader = "X-Resume-Status"

	resumeStatusNew     = "new"     // no token was sent; a fresh conversation
	resumeStatusResumed = "resumed" // reattached, history intact
	resumeStatusExpired = "expired" // token unknown, spent, or reaped
)

func NewWHIPHandler(sm *session.Manager) http.HandlerFunc {
	limiter := newWidgetRateLimiter(defaultWhipRateLimit, time.Minute)
	restartLimiter := newWidgetRateLimiter(defaultRestartRateLimit, time.Minute)

	return func(w http.ResponseWriter, r *http.Request) {
		// Parse path segment: /whip or /whip/{sessionId}
		trimmed := strings.TrimPrefix(r.URL.Path, "/whip")
		trimmed = strings.TrimPrefix(trimmed, "/")

		switch r.Method {
		case http.MethodOptions:
			// RFC 9725 §4.2: MUST support OPTIONS for CORS.
			w.Header().Set("Accept-Post", "application/sdp")
			// RFC 9725 §4.4: advertises that ICE restart is available here.
			w.Header().Set("Accept-Patch", peer.ICEFragmentContentType)
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPost:
			if ok, retryAfter := limiter.Allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				log.Printf("[whip] rate limited %s", clientIP(r))
				http.Error(w, "too many session requests", http.StatusTooManyRequests)
				return
			}
			handleWHIPPost(w, r, sm)

		case http.MethodPatch:
			if trimmed == "" {
				http.Error(w, "session URL required: /whip/{sessionId}", http.StatusBadRequest)
				return
			}
			if ok, retryAfter := restartLimiter.Allow(clientIP(r)); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
				log.Printf("[whip] rate limited ICE restart from %s", clientIP(r))
				http.Error(w, "too many ICE restart requests", http.StatusTooManyRequests)
				return
			}
			handleWHIPPatch(w, r, sm, trimmed)

		case http.MethodDelete:
			if trimmed == "" {
				http.Error(w, "session URL required: /whip/{sessionId}", http.StatusBadRequest)
				return
			}
			handleWHIPDelete(w, sm, trimmed)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleWHIPPost implements RFC 9725 §4.2 Ingest Session Setup.
// A new sessionId (UUID) is generated for each POST.
func handleWHIPPost(w http.ResponseWriter, r *http.Request, sm *session.Manager) {
	// RFC 9725 §4.2: MUST have content type application/sdp.
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/sdp") {
		http.Error(w, "Content-Type must be application/sdp", http.StatusUnsupportedMediaType)
		return
	}

	offerBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read offer", http.StatusBadRequest)
		return
	}
	offerSDP := string(offerBytes)
	if offerSDP == "" {
		http.Error(w, "empty SDP offer", http.StatusBadRequest)
		return
	}

	// Read optional metadata from query parameters.
	direction := r.URL.Query().Get("direction")

	// A resume token reattaches this offer to a conversation whose transport
	// died — the case ICE restart cannot cover, because by the time the client
	// notices, the old peer is already closed. The conversation history, the
	// rolling summary and the LLM client are all preserved; only the transport
	// is new.
	var ses *session.Session
	resumeStatus := resumeStatusNew
	if token := r.URL.Query().Get("resume"); token != "" {
		if resumed := sm.Resume(token); resumed != nil {
			ses = resumed
			resumeStatus = resumeStatusResumed
			// The conversation carries over but the PeerConnection does not,
			// so this is a genuinely new ICE session. Rotating retires the
			// pre-drop ETag, which would otherwise satisfy If-Match on a
			// PATCH aimed at an ICE session that no longer exists.
			ses.RotateETag()
			log.Printf("[whip] resuming session %s", ses.ID)
		} else {
			// Unknown, already-used, or reaped. A fresh session still gives
			// the caller a working call, so this is not an error — but the
			// response says so, because silently starting over is exactly the
			// amnesia the token exists to prevent.
			resumeStatus = resumeStatusExpired
			log.Printf("[whip] resume token rejected, starting a new session")
		}
	}

	if ses == nil {
		ses = sm.GetOrCreate(uuid.New().String())
		if ses == nil {
			// server.max_sessions is reached. 503 rather than 429: the client
			// did nothing wrong, the server is full. Resumes are exempt — they
			// reattach to a session that is already counted.
			w.Header().Set("Retry-After", strconv.Itoa(atCapacityRetryAfterS))
			log.Printf("[whip] refusing session for %s: server at capacity", clientIP(r))
			http.Error(w, "server is at session capacity", http.StatusServiceUnavailable)
			return
		}
		log.Printf("[whip] creating session %s", ses.ID)
	}

	sessionID := ses.ID
	peerID := sessionID
	p, err := ses.AddPeer(peerID, direction)
	if err != nil {
		log.Printf("[whip] add peer error: %v", err)
		http.Error(w, "failed to create peer", http.StatusInternalServerError)
		return
	}

	answerSDP, err := p.HandleOffer(offerSDP)
	if err != nil {
		log.Printf("[whip] offer error: %v", err)
		p.Close()
		http.Error(w, "failed to handle offer", http.StatusInternalServerError)
		return
	}

	log.Printf("[whip] session %s connected", sessionID)

	sessionURL := "/whip/" + sessionID

	// RFC 9725 §4.2: 201 Created, application/sdp body, Location header.
	// RFC 9725 §4.3.1: ETag identifying the ICE session.
	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", sessionURL)
	w.Header().Set("ETag", ses.ETag())
	// RFC 9725 §4.4: tells the client it can recover a dropped connection with
	// an ICE restart instead of redialling and losing the conversation.
	w.Header().Set("Accept-Patch", peer.ICEFragmentContentType)
	// And when the transport is past restarting, this is how the client keeps
	// the conversation anyway. Empty for a realtime session, whose history
	// lives inside the speech-to-speech provider and cannot be carried over.
	w.Header().Set(resumeStatusHeader, resumeStatus)
	if token := sm.IssueResumeToken(ses); token != "" {
		w.Header().Set(resumeTokenHeader, token)
	}
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(answerSDP))
}

// handleWHIPPatch implements RFC 9725 §4.4.2 ICE restart.
//
// The client sends its new ICE credentials and candidates as an sdpfrag; the
// server applies them to the *existing* PeerConnection and replies with its
// own. Nothing above the transport is disturbed — same session, same pipeline,
// same conversation — which is the entire reason to recover this way rather
// than let the client POST a fresh offer and start over.
func handleWHIPPatch(w http.ResponseWriter, r *http.Request, sm *session.Manager, sessionID string) {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, peer.ICEFragmentContentType) {
		w.Header().Set("Accept-Patch", peer.ICEFragmentContentType)
		http.Error(w, "Content-Type must be "+peer.ICEFragmentContentType, http.StatusUnsupportedMediaType)
		return
	}

	ses := sm.Get(sessionID)
	if ses == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	// RFC 9725 §4.4.2: If-Match carries the ETag of the ICE session being
	// restarted. A stale tag means another restart landed first and this one is
	// patching a generation that no longer exists.
	if match := r.Header.Get("If-Match"); match != "" && match != "*" && match != ses.ETag() {
		w.Header().Set("ETag", ses.ETag())
		http.Error(w, "ETag does not match the current ICE session", http.StatusPreconditionFailed)
		return
	}

	// The peer is registered under the session ID — see handleWHIPPost.
	p := ses.GetPeer(sessionID)
	if p == nil {
		http.Error(w, "session has no peer", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxICEFragmentBytes))
	if err != nil {
		http.Error(w, "failed to read ICE fragment", http.StatusBadRequest)
		return
	}

	answerFragment, err := p.RestartICE(string(body))
	switch {
	case errors.Is(err, peer.ErrNoICECredentials):
		// Trickle-only PATCH. RFC 9725 §4.4.1 makes trickle ICE optional and
		// 405 is the prescribed way to decline it; the client falls back to
		// gathering fully before it patches.
		w.Header().Set("Accept-Patch", peer.ICEFragmentContentType)
		http.Error(w, "trickle ICE is not supported; send an ICE restart fragment", http.StatusMethodNotAllowed)
		return
	case errors.Is(err, peer.ErrPeerClosed), errors.Is(err, peer.ErrNoRemoteDescription):
		http.Error(w, "session is not restartable", http.StatusConflict)
		return
	case err != nil:
		log.Printf("[whip] ICE restart error for %s: %v", sessionID, err)
		http.Error(w, "failed to restart ICE", http.StatusInternalServerError)
		return
	}

	log.Printf("[whip] ICE restart for session %s", sessionID)

	w.Header().Set("Content-Type", peer.ICEFragmentContentType)
	w.Header().Set("ETag", ses.RotateETag())
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(answerFragment))
}

// handleWHIPDelete implements RFC 9725 §4.2 session teardown.
func handleWHIPDelete(w http.ResponseWriter, sm *session.Manager, sessionID string) {
	log.Printf("[whip] DELETE session %s", sessionID)

	// Idempotent: return 200 even if the session was already cleaned up by
	// the server (e.g. WebRTC connection state changed to disconnected
	// before the client's DELETE arrived).
	sm.Remove(sessionID)

	w.WriteHeader(http.StatusOK)
}
