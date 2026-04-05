package mediasoupclient

// defaultPionNativeRtpCapabilities exposes a mediasoup-style capability table
// aligned with Pion's default MediaEngine codec registration.
//
// mediasoup-client handlers query browser native RTP capabilities at runtime.
// Pion does not expose a direct equivalent API, so this table is the Go-side
// mapping used by Device.Load().
var defaultPionNativeRtpCapabilities = RtpCapabilities{
	Codecs: []RtpCodecCapability{
		{
			Kind:                 MediaKindAudio,
			MimeType:             "audio/opus",
			PreferredPayloadType: 111,
			ClockRate:            48000,
			Channels:             2,
			Parameters: map[string]any{
				"minptime":     10,
				"useinbandfec": 1,
			},
			RTCPFeedback: []RtcpFeedback{{Type: "transport-cc"}},
		},
		{
			Kind:                 MediaKindAudio,
			MimeType:             "audio/G722",
			PreferredPayloadType: 9,
			ClockRate:            8000,
			Channels:             1,
			Parameters:           map[string]any{},
			RTCPFeedback:         []RtcpFeedback{{Type: "transport-cc"}},
		},
		{
			Kind:                 MediaKindAudio,
			MimeType:             "audio/PCMU",
			PreferredPayloadType: 0,
			ClockRate:            8000,
			Channels:             1,
			Parameters:           map[string]any{},
			RTCPFeedback:         []RtcpFeedback{{Type: "transport-cc"}},
		},
		{
			Kind:                 MediaKindAudio,
			MimeType:             "audio/PCMA",
			PreferredPayloadType: 8,
			ClockRate:            8000,
			Channels:             1,
			Parameters:           map[string]any{},
			RTCPFeedback:         []RtcpFeedback{{Type: "transport-cc"}},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/VP8",
			PreferredPayloadType: 96,
			ClockRate:            90000,
			Parameters:           map[string]any{},
			RTCPFeedback: []RtcpFeedback{
				{Type: "goog-remb"},
				{Type: "transport-cc"},
				{Type: "ccm", Parameter: "fir"},
				{Type: "nack"},
				{Type: "nack", Parameter: "pli"},
			},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/rtx",
			PreferredPayloadType: 97,
			ClockRate:            90000,
			Parameters: map[string]any{
				"apt": 96,
			},
			RTCPFeedback: []RtcpFeedback{},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/H264",
			PreferredPayloadType: 102,
			ClockRate:            90000,
			Parameters: map[string]any{
				"level-asymmetry-allowed": 1,
				"packetization-mode":      1,
				"profile-level-id":        "42e01f",
			},
			RTCPFeedback: []RtcpFeedback{
				{Type: "goog-remb"},
				{Type: "transport-cc"},
				{Type: "ccm", Parameter: "fir"},
				{Type: "nack"},
				{Type: "nack", Parameter: "pli"},
			},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/rtx",
			PreferredPayloadType: 103,
			ClockRate:            90000,
			Parameters: map[string]any{
				"apt": 102,
			},
			RTCPFeedback: []RtcpFeedback{},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/VP9",
			PreferredPayloadType: 98,
			ClockRate:            90000,
			Parameters: map[string]any{
				"profile-id": 0,
			},
			RTCPFeedback: []RtcpFeedback{
				{Type: "goog-remb"},
				{Type: "transport-cc"},
				{Type: "ccm", Parameter: "fir"},
				{Type: "nack"},
				{Type: "nack", Parameter: "pli"},
			},
		},
		{
			Kind:                 MediaKindVideo,
			MimeType:             "video/rtx",
			PreferredPayloadType: 99,
			ClockRate:            90000,
			Parameters: map[string]any{
				"apt": 98,
			},
			RTCPFeedback: []RtcpFeedback{},
		},
	},
	HeaderExtensions: []RtpHeaderExtension{
		{
			Kind:        MediaKindAudio,
			URI:         "urn:ietf:params:rtp-hdrext:sdes:mid",
			PreferredID: 1,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindVideo,
			URI:         "urn:ietf:params:rtp-hdrext:sdes:mid",
			PreferredID: 1,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindAudio,
			URI:         "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time",
			PreferredID: 3,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindVideo,
			URI:         "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time",
			PreferredID: 3,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindAudio,
			URI:         "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01",
			PreferredID: 5,
			Direction:   RTPDirectionRecvOnly,
		},
		{
			Kind:        MediaKindVideo,
			URI:         "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01",
			PreferredID: 5,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindAudio,
			URI:         "urn:ietf:params:rtp-hdrext:ssrc-audio-level",
			PreferredID: 10,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindVideo,
			URI:         "urn:3gpp:video-orientation",
			PreferredID: 11,
			Direction:   RTPDirectionSendRecv,
		},
		{
			Kind:        MediaKindVideo,
			URI:         "urn:ietf:params:rtp-hdrext:toffset",
			PreferredID: 12,
			Direction:   RTPDirectionSendRecv,
		},
	},
}

// DefaultNativeRtpCapabilities returns mediasoup-style capabilities based on
// Pion defaults.
func DefaultNativeRtpCapabilities(direction RTPDirection) (RtpCapabilities, error) {
	_ = direction
	return cloneRtpCapabilities(defaultPionNativeRtpCapabilities), nil
}

// DefaultNativeSctpCapabilities returns conservative SCTP capability defaults.
func DefaultNativeSctpCapabilities() (SctpCapabilities, error) {
	return SctpCapabilities{
		NumStreams: NumSctpStreams{OS: 1024, MIS: 1024},
	}, nil
}
