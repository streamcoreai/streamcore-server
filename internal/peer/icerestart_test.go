package peer

import (
	"errors"
	"strings"
	"testing"
)

// A minimal but realistic browser offer: session-level origin, one bundled
// audio section with credentials, an SSRC, and a stale candidate.
const testRemoteOffer = "v=0\r\n" +
	"o=- 4611731400430051336 2 IN IP4 127.0.0.1\r\n" +
	"s=-\r\n" +
	"t=0 0\r\n" +
	"a=group:BUNDLE 0 1\r\n" +
	"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
	"c=IN IP4 0.0.0.0\r\n" +
	"a=mid:0\r\n" +
	"a=ice-ufrag:oldU\r\n" +
	"a=ice-pwd:oldPassword0000000000\r\n" +
	"a=fingerprint:sha-256 AA:BB:CC\r\n" +
	"a=candidate:1 1 udp 2130706431 192.0.2.10 41000 typ host\r\n" +
	"a=end-of-candidates\r\n" +
	"a=rtpmap:111 opus/48000/2\r\n" +
	"a=ssrc:12345 cname:stream\r\n" +
	"a=sendrecv\r\n" +
	"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
	"c=IN IP4 0.0.0.0\r\n" +
	"a=mid:1\r\n" +
	"a=ice-ufrag:oldU\r\n" +
	"a=ice-pwd:oldPassword0000000000\r\n" +
	"a=sctp-port:5000\r\n"

func TestParseICEFragment(t *testing.T) {
	frag := "a=ice-ufrag:newU\r\n" +
		"a=ice-pwd:newPassword111111111\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"a=mid:0\r\n" +
		"a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host\r\n" +
		"a=candidate:2 1 udp 1694498815 203.0.113.9 51000 typ srflx\r\n"

	ufrag, pwd, candidates, err := ParseICEFragment(frag)
	if err != nil {
		t.Fatalf("ParseICEFragment: %v", err)
	}
	if ufrag != "newU" || pwd != "newPassword111111111" {
		t.Fatalf("credentials = %q / %q", ufrag, pwd)
	}
	if len(candidates) != 2 {
		t.Fatalf("got %d candidates, want 2: %v", len(candidates), candidates)
	}
	if !strings.HasPrefix(candidates[0], "1 1 udp") {
		t.Fatalf("candidate kept its a=candidate: prefix: %q", candidates[0])
	}
}

func TestParseICEFragmentLFOnlyAndDuplicateCredentials(t *testing.T) {
	// Some clients send LF-only bodies, and repeat the credentials at both
	// session and media level. The first of each wins.
	frag := "a=ice-ufrag:newU\n" +
		"a=ice-pwd:newPassword111111111\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\n" +
		"a=ice-ufrag:newU\n" +
		"a=ice-pwd:newPassword111111111\n"

	ufrag, pwd, candidates, err := ParseICEFragment(frag)
	if err != nil {
		t.Fatalf("ParseICEFragment: %v", err)
	}
	if ufrag != "newU" || pwd != "newPassword111111111" {
		t.Fatalf("credentials = %q / %q", ufrag, pwd)
	}
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0", len(candidates))
	}
}

func TestParseICEFragmentTrickleOnly(t *testing.T) {
	// Candidates with no credentials is a trickle update, not a restart.
	frag := "m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"a=mid:0\r\n" +
		"a=candidate:1 1 udp 2130706431 198.51.100.7 51000 typ host\r\n"

	if _, _, _, err := ParseICEFragment(frag); !errors.Is(err, ErrNoICECredentials) {
		t.Fatalf("err = %v, want ErrNoICECredentials", err)
	}
}

func TestParseICEFragmentUfragWithoutPwd(t *testing.T) {
	if _, _, _, err := ParseICEFragment("a=ice-ufrag:newU\r\n"); !errors.Is(err, ErrNoICECredentials) {
		t.Fatalf("err = %v, want ErrNoICECredentials", err)
	}
}

func TestRewriteICEForRestart(t *testing.T) {
	candidates := []string{
		"1 1 udp 2130706431 198.51.100.7 51000 typ host",
		"2 1 udp 1694498815 203.0.113.9 51000 typ srflx",
	}
	got := rewriteICEForRestart(testRemoteOffer, "newU", "newPassword111111111", candidates)
	lines := splitSDPLines(got)

	if strings.Contains(got, "oldU") || strings.Contains(got, "oldPassword0000000000") {
		t.Fatalf("old credentials survived:\n%s", got)
	}
	// Both media sections carry the new credentials — the datachannel section
	// is bundled onto the audio one and must agree with it.
	if n := strings.Count(got, "a=ice-ufrag:newU"); n != 2 {
		t.Fatalf("a=ice-ufrag:newU appears %d times, want 2:\n%s", n, got)
	}
	if n := strings.Count(got, "a=ice-pwd:newPassword111111111"); n != 2 {
		t.Fatalf("a=ice-pwd appears %d times, want 2:\n%s", n, got)
	}

	if strings.Contains(got, "192.0.2.10") {
		t.Fatalf("stale candidate survived:\n%s", got)
	}
	for _, c := range candidates {
		if !strings.Contains(got, "a=candidate:"+c) {
			t.Fatalf("missing new candidate %q:\n%s", c, got)
		}
	}

	// Everything Pion matches transceivers and receivers on must survive
	// verbatim, or the restart detaches the pipeline from its track.
	for _, must := range []string{
		"a=mid:0", "a=mid:1", "a=ssrc:12345 cname:stream",
		"a=fingerprint:sha-256 AA:BB:CC", "a=rtpmap:111 opus/48000/2",
		"a=group:BUNDLE 0 1", "m=application 9 UDP/DTLS/SCTP webrtc-datachannel",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("rewrite dropped %q:\n%s", must, got)
		}
	}

	// The origin advertises a new revision of the same session.
	if !strings.Contains(got, "o=- 4611731400430051336 3 IN IP4 127.0.0.1") {
		t.Fatalf("origin version not bumped:\n%s", got)
	}

	// Candidates belong to the first (bundle master) media section, which is
	// the only place Pion reads them from.
	audioStart, appStart := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(line, "m=audio") {
			audioStart = i
		}
		if strings.HasPrefix(line, "m=application") {
			appStart = i
		}
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "a=candidate:") && (i < audioStart || i > appStart) {
			t.Fatalf("candidate at line %d is outside the first media section:\n%s", i, got)
		}
	}
}

func TestRewriteICEForRestartNoCandidates(t *testing.T) {
	// A credentials-only restart is legal — the client may trickle later, or
	// simply have nothing new to offer.
	got := rewriteICEForRestart(testRemoteOffer, "newU", "newPassword111111111", nil)
	if strings.Contains(got, "a=candidate:") {
		t.Fatalf("expected no candidates:\n%s", got)
	}
	if strings.Contains(got, "a=end-of-candidates") {
		t.Fatalf("end-of-candidates without candidates:\n%s", got)
	}
}

func TestICEFragmentFromSDP(t *testing.T) {
	answer := "v=0\r\n" +
		"o=- 1 2 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\n" +
		"a=mid:0\r\n" +
		"a=ice-ufrag:srvU\r\n" +
		"a=ice-pwd:srvPassword2222222222\r\n" +
		"a=candidate:1 1 udp 2130706431 198.51.100.1 39132 typ host\r\n" +
		"m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
		"a=mid:1\r\n" +
		"a=ice-ufrag:srvU\r\n" +
		"a=candidate:9 1 udp 1 10.0.0.1 1 typ host\r\n"

	got := iceFragmentFromSDP(answer)

	for _, must := range []string{
		"a=ice-ufrag:srvU",
		"a=ice-pwd:srvPassword2222222222",
		"m=audio 9 UDP/TLS/RTP/SAVPF 111",
		"a=mid:0",
		"a=candidate:1 1 udp 2130706431 198.51.100.1 39132 typ host",
		"a=end-of-candidates",
	} {
		if !strings.Contains(got, must) {
			t.Fatalf("fragment missing %q:\n%s", must, got)
		}
	}
	// Only the bundle master's section is described.
	if strings.Contains(got, "m=application") || strings.Contains(got, "a=mid:1") {
		t.Fatalf("fragment leaked a bundled section:\n%s", got)
	}
	if n := strings.Count(got, "a=candidate:"); n != 1 {
		t.Fatalf("got %d candidates, want 1:\n%s", n, got)
	}
}

func TestBumpSDPOrigin(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "increments the session version",
			in:   "o=- 4611731400430051336 2 IN IP4 127.0.0.1",
			want: "o=- 4611731400430051336 3 IN IP4 127.0.0.1",
		},
		{
			name: "leaves a malformed origin alone",
			in:   "o=- 123",
			want: "o=- 123",
		},
		{
			name: "leaves a non-numeric version alone",
			in:   "o=- 123 abc IN IP4 127.0.0.1",
			want: "o=- 123 abc IN IP4 127.0.0.1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := bumpSDPOrigin(tc.in); got != tc.want {
				t.Fatalf("bumpSDPOrigin = %q, want %q", got, tc.want)
			}
		})
	}
}
