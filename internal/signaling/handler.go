package signaling

import (
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/streamcoreai/server/internal/session"
)

// NewWHIPHandler returns an HTTP handler implementing WHIP signaling per
// RFC 9725. It handles two URL patterns:
//
//	POST   /whip                   – Session setup (SDP offer/answer), returns sessionId
//	DELETE /whip/{sessionId}       – Session teardown
//	OPTIONS (any)                  – CORS preflight
func NewWHIPHandler(sm *session.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse path segment: /whip or /whip/{sessionId}
		trimmed := strings.TrimPrefix(r.URL.Path, "/whip")
		trimmed = strings.TrimPrefix(trimmed, "/")

		switch r.Method {
		case http.MethodOptions:
			// RFC 9725 §4.2: MUST support OPTIONS for CORS.
			w.Header().Set("Accept-Post", "application/sdp")
			w.WriteHeader(http.StatusNoContent)

		case http.MethodPost:
			handleWHIPPost(w, r, sm)

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

	sessionID := uuid.New().String()
	peerID := sessionID
	log.Printf("[whip] creating session %s", sessionID)

	// Read optional metadata from query parameters.
	direction := r.URL.Query().Get("direction")

	ses := sm.GetOrCreate(sessionID)
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
	w.Header().Set("ETag", `"`+sessionID+`"`)
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(answerSDP))
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
