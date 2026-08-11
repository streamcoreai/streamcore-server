package peer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// newTestClient builds a browser-shaped offerer: one audio track, one
// DataChannel, host candidates only so the test never waits on STUN.
func newTestClient(t *testing.T) *webrtc.PeerConnection {
	t.Helper()

	pc, err := webrtc.NewAPI().NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("create client peer connection: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio", "client",
	)
	if err != nil {
		t.Fatalf("create client track: %v", err)
	}
	if _, err := pc.AddTrack(track); err != nil {
		t.Fatalf("add client track: %v", err)
	}
	if _, err := pc.CreateDataChannel(EventChannelLabel, nil); err != nil {
		t.Fatalf("create client data channel: %v", err)
	}
	return pc
}

// clientOffer creates an offer, applies it, and waits for gathering so the SDP
// carries candidates. With iceRestart set it is the offer a browser produces
// after restartIce().
func clientOffer(t *testing.T, pc *webrtc.PeerConnection, iceRestart bool) string {
	t.Helper()

	offer, err := pc.CreateOffer(&webrtc.OfferOptions{ICERestart: iceRestart})
	if err != nil {
		t.Fatalf("create client offer: %v", err)
	}
	gatherDone := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatalf("set client local description: %v", err)
	}
	select {
	case <-gatherDone:
	case <-time.After(10 * time.Second):
		t.Fatal("client ICE gathering timed out")
	}
	return pc.LocalDescription().SDP
}

func sdpICEUfrag(t *testing.T, sdp string) string {
	t.Helper()
	for _, line := range splitSDPLines(sdp) {
		if strings.HasPrefix(line, "a=ice-ufrag:") {
			return strings.TrimPrefix(line, "a=ice-ufrag:")
		}
	}
	t.Fatalf("no ice-ufrag in:\n%s", sdp)
	return ""
}

// TestRestartICEPreservesTheConnection is the acceptance test for the recovery
// path: a client that has lost connectivity re-gathers ICE and PATCHes the
// result, and everything above the transport — the PeerConnection, the tracks,
// the channel the pipeline reads from — must come through untouched. If any of
// it were rebuilt, the caller would hear the agent forget the conversation.
func TestRestartICEPreservesTheConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real peer connections and gathers ICE")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server, err := New(ctx, "srv", "", TURNConfig{})
	if err != nil {
		t.Fatalf("create server peer: %v", err)
	}
	defer server.Close()

	closed := make(chan struct{})
	server.OnClose = func() { close(closed) }

	client := newTestClient(t)

	answerSDP, err := server.HandleOffer(clientOffer(t, client, false))
	if err != nil {
		t.Fatalf("HandleOffer: %v", err)
	}
	if err := client.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}); err != nil {
		t.Fatalf("set client remote description: %v", err)
	}

	// Identity before the restart.
	var (
		pcBefore       = server.pc
		trackBefore    = server.LocalTrack()
		trackChBefore  = server.RemoteTrackCh
		peerCtxBefore  = server.Context()
		serverUfragOld = sdpICEUfrag(t, answerSDP)
		clientUfragOld = sdpICEUfrag(t, client.LocalDescription().SDP)
	)

	// The client re-gathers, exactly as it would after a network handover, and
	// sends the result as an sdpfrag.
	fragment := iceFragmentFromSDP(clientOffer(t, client, true))

	clientUfragNew := sdpICEUfrag(t, client.LocalDescription().SDP)
	if clientUfragNew == clientUfragOld {
		t.Fatal("client did not generate new ICE credentials")
	}

	answerFragment, err := server.RestartICE(fragment)
	if err != nil {
		t.Fatalf("server RestartICE: %v", err)
	}

	// The server answered with a new ICE generation of its own.
	serverUfragNew := sdpICEUfrag(t, answerFragment)
	if serverUfragNew == serverUfragOld {
		t.Fatalf("server reused ICE ufrag %q across the restart", serverUfragNew)
	}
	if !strings.Contains(answerFragment, "a=ice-pwd:") {
		t.Fatalf("answer fragment has no ice-pwd:\n%s", answerFragment)
	}
	if !strings.Contains(answerFragment, "a=candidate:") {
		t.Fatalf("answer fragment has no candidates:\n%s", answerFragment)
	}

	// The server adopted the client's credentials.
	if got := sdpICEUfrag(t, server.pc.RemoteDescription().SDP); got != clientUfragNew {
		t.Fatalf("server remote ufrag = %q, want the client's new %q", got, clientUfragNew)
	}

	// Nothing above the transport was rebuilt.
	if server.pc != pcBefore {
		t.Fatal("PeerConnection was replaced by the restart")
	}
	if server.LocalTrack() != trackBefore {
		t.Fatal("outbound track was replaced by the restart")
	}
	if server.RemoteTrackCh != trackChBefore {
		t.Fatal("remote track channel was replaced by the restart")
	}
	if server.Context() != peerCtxBefore || peerCtxBefore.Err() != nil {
		t.Fatal("peer context was replaced or cancelled by the restart")
	}
	if len(server.pc.GetTransceivers()) != len(pcBefore.GetTransceivers()) {
		t.Fatal("transceiver set changed across the restart")
	}

	select {
	case <-closed:
		t.Fatal("peer was closed by the restart")
	default:
	}

	// The reply is parseable as the restart fragment it claims to be, which is
	// what the client folds back into its own remote description.
	gotUfrag, gotPwd, gotCandidates, err := ParseICEFragment(answerFragment)
	if err != nil {
		t.Fatalf("server's own answer fragment does not parse: %v\n%s", err, answerFragment)
	}
	if gotUfrag != serverUfragNew || gotPwd == "" || len(gotCandidates) == 0 {
		t.Fatalf("answer fragment round-trip lost content: %q / %q / %d candidates",
			gotUfrag, gotPwd, len(gotCandidates))
	}
}
