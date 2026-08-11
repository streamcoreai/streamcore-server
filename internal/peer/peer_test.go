package peer

import (
	"testing"

	"github.com/pion/webrtc/v4"
)

// The connection-state policy is the difference between a network blip and a
// lost conversation, so pin every state rather than only the interesting ones.
func TestIsTerminalState(t *testing.T) {
	tests := []struct {
		state webrtc.PeerConnectionState
		want  bool
	}{
		{webrtc.PeerConnectionStateNew, false},
		{webrtc.PeerConnectionStateConnecting, false},
		{webrtc.PeerConnectionStateConnected, false},
		// Transient: ICE recovers from this on its own after a Wi-Fi/cellular
		// handover or a NAT rebind, and Pion escalates to Failed if it does not.
		{webrtc.PeerConnectionStateDisconnected, false},
		{webrtc.PeerConnectionStateFailed, true},
		{webrtc.PeerConnectionStateClosed, true},
	}
	for _, tc := range tests {
		t.Run(tc.state.String(), func(t *testing.T) {
			if got := isTerminalState(tc.state); got != tc.want {
				t.Fatalf("isTerminalState(%s) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func TestDisconnectedFlagReportsRecoveryOnce(t *testing.T) {
	p := &Peer{ID: "test"}

	if p.clearDisconnected() {
		t.Fatal("a peer that never disconnected reported a recovery")
	}

	p.markDisconnected()
	if !p.clearDisconnected() {
		t.Fatal("recovery after a disconnect was not reported")
	}
	if p.clearDisconnected() {
		t.Fatal("recovery reported twice for one disconnect")
	}
}

func TestDetectOpusChannels(t *testing.T) {
	tests := []struct {
		name string
		sdp  string
		want uint16
	}{
		{
			name: "browser-style offer (stereo convention)",
			sdp: "v=0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
				"a=rtpmap:111 opus/48000/2\r\n" +
				"a=fmtp:111 minptime=10;useinbandfec=1\r\n",
			want: 2,
		},
		{
			name: "streamcoreai/go-sdk offer (mono)",
			sdp: "v=0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
				"a=rtpmap:111 opus/48000/1\r\n" +
				"a=fmtp:111 minptime=10;useinbandfec=1\r\n",
			want: 1,
		},
		{
			name: "no opus rtpmap, fall back to stereo",
			sdp: "v=0\r\n" +
				"m=audio 9 UDP/TLS/RTP/SAVPF 0 8\r\n" +
				"a=rtpmap:0 PCMU/8000\r\n",
			want: 2,
		},
		{
			name: "both offered - stereo wins (browser-compatible default)",
			sdp: "m=audio 9 UDP/TLS/RTP/SAVPF 109 111\r\n" +
				"a=rtpmap:109 opus/48000/1\r\n" +
				"a=rtpmap:111 opus/48000/2\r\n",
			want: 2,
		},
		{
			name: "tab-separated rtpmap",
			sdp:  "a=rtpmap:111\topus/48000/1\r\n",
			want: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectOpusChannels(tc.sdp)
			if got != tc.want {
				t.Fatalf("detectOpusChannels = %d, want %d", got, tc.want)
			}
		})
	}
}
