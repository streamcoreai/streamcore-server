package peer

import "testing"

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
