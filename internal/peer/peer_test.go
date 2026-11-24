package peer

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// Only a caller that can plainly reach a host candidate gets the early
// answer; anyone else still waits for the reflexive candidate they may need.
func TestCallerIsLocal(t *testing.T) {
	cand := func(addr, typ string) string {
		return "a=candidate:1 1 udp 2130706431 " + addr + " 50000 typ " + typ + "\r\n"
	}
	reflexiveIP.Store("203.0.113.7")
	defer reflexiveIP.Store("")

	cases := []struct {
		name string
		sdp  string
		want bool
	}{
		{"loopback host", cand("127.0.0.1", "host"), true},
		{"same reflexive address as the server", cand("f3a1.local", "host") + cand("203.0.113.7", "srflx"), true},
		{"browser mDNS host, other network", cand("f3a1.local", "host") + cand("198.51.100.9", "srflx"), false},
		{"public host on another network", cand("198.51.100.9", "host"), false},
		{"trickle client, no candidates", "v=0\r\n", false},
	}
	// A host candidate in one of our subnets counts only without a foreign
	// reflexive address: another network can use the same private range.
	if nets := localNetworks(); len(nets) > 0 {
		lan := nets[0].IP.String()
		cases = append(cases,
			struct {
				name string
				sdp  string
				want bool
			}{"same subnet, no reflexive", cand(lan, "host"), true},
			struct {
				name string
				sdp  string
				want bool
			}{"same private range, other network", cand(lan, "host") + cand("198.51.100.9", "srflx"), false},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := callerIsLocal(c.sdp); got != c.want {
				t.Errorf("callerIsLocal = %v, want %v", got, c.want)
			}
		})
	}
	if w := gatherWait("t", cand("198.51.100.9", "host"), true); w != 5*time.Second {
		t.Errorf("remote caller wait %v, want the full 5s", w)
	}
	if w := gatherWait("t", cand("127.0.0.1", "host"), true); w != localAnswerWait {
		t.Errorf("local caller wait %v, want %v", w, localAnswerWait)
	}
	if w := gatherWait("t", cand("127.0.0.1", "host"), false); w != 5*time.Second {
		t.Errorf("with the built-in STUN/TURN server a local caller waited %v, want the full 5s", w)
	}
}

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
