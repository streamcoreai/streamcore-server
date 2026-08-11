package peer

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
)

// ICEFragmentContentType is the media type of an ICE restart / trickle body,
// per RFC 8840 and RFC 9725 §4.4.
const ICEFragmentContentType = "application/trickle-ice-sdpfrag"

var (
	// ErrNoICECredentials means the fragment carried candidates but no
	// ice-ufrag / ice-pwd, i.e. it is a trickle-ICE update rather than a
	// restart. Trickle is optional per RFC 9725 §4.4.1 and is not implemented.
	ErrNoICECredentials = errors.New("sdpfrag carries no ICE credentials")

	// ErrPeerClosed means the peer was torn down before the restart arrived.
	ErrPeerClosed = errors.New("peer is closed")

	// ErrNoRemoteDescription means the peer never completed an offer/answer,
	// so there is nothing to restart against.
	ErrNoRemoteDescription = errors.New("peer has no remote description")
)

// RestartICE applies a client's new ICE credentials and candidates to the
// existing PeerConnection and returns the server's own sdpfrag in reply
// (RFC 9725 §4.4.2).
//
// The whole point of routing recovery through ICE restart rather than a fresh
// offer is that *this* PeerConnection survives: the DTLS association, the
// transceivers, the remote track the pipeline is reading from, and every layer
// above the transport are untouched. Only the ICE generation is replaced.
//
// Pion has no entry point for applying a restart from an SDP *fragment*, but
// SetRemoteDescription does trigger one when a renegotiation offer changes the
// remote ICE credentials. So the fragment is folded back into the stored remote
// offer — new ufrag, new pwd, new candidates, everything else verbatim — and
// that synthesised offer drives the restart. Carrying the media sections over
// unchanged is not cosmetic: Pion only keeps an existing RTPReceiver across a
// renegotiation when the SSRCs still match, and a recreated receiver would
// detach the pipeline from its track.
func (p *Peer) RestartICE(fragment string) (string, error) {
	ufrag, pwd, candidates, err := ParseICEFragment(fragment)
	if err != nil {
		return "", err
	}

	p.restartMu.Lock()
	defer p.restartMu.Unlock()

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return "", ErrPeerClosed
	}

	remote := p.pc.RemoteDescription()
	if remote == nil {
		return "", ErrNoRemoteDescription
	}

	offer := rewriteICEForRestart(remote.SDP, ufrag, pwd, candidates)
	if err := p.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offer,
	}); err != nil {
		return "", fmt.Errorf("set restart offer: %w", err)
	}

	// The promise must be created after SetRemoteDescription: the restart is
	// what reopens gathering, and a promise made before it would resolve
	// against the previous generation's completed gather.
	gatherDone := webrtc.GatheringCompletePromise(p.pc)

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create restart answer: %w", err)
	}
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set restart answer: %w", err)
	}

	select {
	case <-gatherDone:
	case <-time.After(5 * time.Second):
		log.Printf("[peer:%s] ICE re-gathering timed out, replying with partial candidates", p.ID)
	}

	log.Printf("[peer:%s] ICE restart applied", p.ID)
	return iceFragmentFromSDP(p.pc.LocalDescription().SDP), nil
}

// ParseICEFragment extracts the ICE credentials and candidates from an
// `application/trickle-ice-sdpfrag` body. Credentials may appear at session or
// media level; the first of each wins, which is what a bundled offer means
// anyway. Returns ErrNoICECredentials when the body is trickle-only.
func ParseICEFragment(fragment string) (ufrag, pwd string, candidates []string, err error) {
	for _, line := range splitSDPLines(fragment) {
		switch {
		case strings.HasPrefix(line, "a=ice-ufrag:"):
			if ufrag == "" {
				ufrag = strings.TrimPrefix(line, "a=ice-ufrag:")
			}
		case strings.HasPrefix(line, "a=ice-pwd:"):
			if pwd == "" {
				pwd = strings.TrimPrefix(line, "a=ice-pwd:")
			}
		case strings.HasPrefix(line, "a=candidate:"):
			candidates = append(candidates, strings.TrimPrefix(line, "a=candidate:"))
		}
	}

	if ufrag == "" || pwd == "" {
		return "", "", nil, ErrNoICECredentials
	}
	return ufrag, pwd, candidates, nil
}

// rewriteICEForRestart folds new ICE credentials and candidates into an
// existing offer, leaving every other line — m-lines, payload types, SSRCs,
// mids, the DTLS fingerprint — exactly as it was.
//
// Candidates land in the first media section because that is where Pion reads
// them from when the offer is BUNDLEd, and they are the only place they can be:
// candidates are media-level attributes.
func rewriteICEForRestart(remoteSDP, ufrag, pwd string, candidates []string) string {
	lines := splitSDPLines(remoteSDP)
	out := make([]string, 0, len(lines)+len(candidates)+1)

	mediaIndex := -1
	inserted := false
	insertCandidates := func() {
		if inserted {
			return
		}
		inserted = true
		for _, c := range candidates {
			out = append(out, "a=candidate:"+c)
		}
		if len(candidates) > 0 {
			out = append(out, "a=end-of-candidates")
		}
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "m="):
			if mediaIndex == 0 {
				// Leaving the first media section — the new candidates belong
				// at its end, after the attributes it already carries.
				insertCandidates()
			}
			mediaIndex++
			out = append(out, line)
		case strings.HasPrefix(line, "o="):
			out = append(out, bumpSDPOrigin(line))
		case strings.HasPrefix(line, "a=ice-ufrag:"):
			out = append(out, "a=ice-ufrag:"+ufrag)
		case strings.HasPrefix(line, "a=ice-pwd:"):
			out = append(out, "a=ice-pwd:"+pwd)
		case strings.HasPrefix(line, "a=candidate:"), line == "a=end-of-candidates":
			// Previous ICE generation — dropped.
		default:
			out = append(out, line)
		}
	}
	insertCandidates()

	return strings.Join(out, "\r\n") + "\r\n"
}

// iceFragmentFromSDP renders the server's half of the restart as an sdpfrag:
// the new credentials, then the bundle-master m-line with its mid and
// candidates. Shaped after the response example in RFC 9725 §4.4.2.
func iceFragmentFromSDP(localSDP string) string {
	var (
		ufrag, pwd string
		mLine, mid string
		candidates []string
		mediaIndex = -1
	)

	for _, line := range splitSDPLines(localSDP) {
		if strings.HasPrefix(line, "m=") {
			mediaIndex++
			if mediaIndex == 0 {
				mLine = line
			}
			continue
		}
		// Session-level attributes (mediaIndex < 0) and the first media
		// section's are both usable; later sections are BUNDLEd onto the first.
		if mediaIndex > 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "a=ice-ufrag:"):
			if ufrag == "" {
				ufrag = strings.TrimPrefix(line, "a=ice-ufrag:")
			}
		case strings.HasPrefix(line, "a=ice-pwd:"):
			if pwd == "" {
				pwd = strings.TrimPrefix(line, "a=ice-pwd:")
			}
		case strings.HasPrefix(line, "a=mid:"):
			if mid == "" {
				mid = strings.TrimPrefix(line, "a=mid:")
			}
		case strings.HasPrefix(line, "a=candidate:"):
			candidates = append(candidates, strings.TrimPrefix(line, "a=candidate:"))
		}
	}

	out := make([]string, 0, len(candidates)+5)
	if ufrag != "" {
		out = append(out, "a=ice-ufrag:"+ufrag)
	}
	if pwd != "" {
		out = append(out, "a=ice-pwd:"+pwd)
	}
	if mLine != "" {
		out = append(out, mLine)
	}
	if mid != "" {
		out = append(out, "a=mid:"+mid)
	}
	for _, c := range candidates {
		out = append(out, "a=candidate:"+c)
	}
	out = append(out, "a=end-of-candidates")

	return strings.Join(out, "\r\n") + "\r\n"
}

// bumpSDPOrigin increments the session version in an `o=` line, which is how
// JSEP marks a description as a new revision of the same session.
func bumpSDPOrigin(line string) string {
	fields := strings.Fields(strings.TrimPrefix(line, "o="))
	if len(fields) < 6 {
		return line
	}
	version, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return line
	}
	fields[2] = strconv.FormatUint(version+1, 10)
	return "o=" + strings.Join(fields, " ")
}

// splitSDPLines splits an SDP or SDP fragment into lines, tolerating LF-only
// bodies and dropping the blank line a trailing CRLF leaves behind.
func splitSDPLines(sdp string) []string {
	raw := strings.Split(sdp, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}
