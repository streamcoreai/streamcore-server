package mediasoupclient

import "testing"

func TestGetExtendedRtpCapabilitiesAndMappings(t *testing.T) {
	local := sampleLocalCapabilities()
	remote := sampleRouterCapabilities()

	extended := GetExtendedRtpCapabilities(local, remote, false)
	if len(extended.Codecs) == 0 {
		t.Fatal("expected at least one matched codec")
	}

	var vp8 *ExtendedRtpCodecCapability
	for i := range extended.Codecs {
		codec := &extended.Codecs[i]
		if codec.MimeType == "video/VP8" {
			vp8 = codec
			break
		}
	}
	if vp8 == nil {
		t.Fatal("expected matched VP8 codec")
	}
	if vp8.LocalPayloadType != 96 {
		t.Fatalf("unexpected local payload type: %d", vp8.LocalPayloadType)
	}
	if vp8.RemotePayloadType != 101 {
		t.Fatalf("unexpected remote payload type: %d", vp8.RemotePayloadType)
	}
	if vp8.LocalRtxPayloadType == nil || *vp8.LocalRtxPayloadType != 97 {
		t.Fatalf("unexpected local RTX payload type: %v", vp8.LocalRtxPayloadType)
	}
	if vp8.RemoteRtxPayloadType == nil || *vp8.RemoteRtxPayloadType != 102 {
		t.Fatalf("unexpected remote RTX payload type: %v", vp8.RemoteRtxPayloadType)
	}
}

func TestPreferLocalCodecOrder(t *testing.T) {
	local := sampleLocalCapabilities()
	remote := sampleRouterCapabilities()

	extended := GetExtendedRtpCapabilities(local, remote, true)
	if len(extended.Codecs) < 2 {
		t.Fatalf("expected at least 2 matched codecs, got %d", len(extended.Codecs))
	}
	if extended.Codecs[0].MimeType != "audio/opus" {
		t.Fatalf("expected first codec in local order to be opus, got %s", extended.Codecs[0].MimeType)
	}
}

func TestGetSendingRemoteRtpParametersFeedbackReduction(t *testing.T) {
	extended := GetExtendedRtpCapabilities(sampleLocalCapabilities(), sampleRouterCapabilities(), false)
	params := GetSendingRemoteRtpParameters(MediaKindVideo, extended)

	for _, codec := range params.Codecs {
		for _, fb := range codec.RTCPFeedback {
			if fb.Type == "goog-remb" {
				t.Fatalf("goog-remb should be removed when transport-cc is available: %+v", codec)
			}
		}
	}
}

func TestReduceCodecs(t *testing.T) {
	extended := GetExtendedRtpCapabilities(sampleLocalCapabilities(), sampleRouterCapabilities(), false)
	params := GetSendingRtpParameters(MediaKindVideo, extended)

	capCodec := &RtpCodecCapability{MimeType: "video/VP8", ClockRate: 90000}
	filtered, err := ReduceCodecs(params.Codecs, capCodec)
	if err != nil {
		t.Fatalf("reduce codecs failed: %v", err)
	}
	if len(filtered) != 2 {
		t.Fatalf("expected VP8 + RTX, got %d codecs", len(filtered))
	}

	noMatchCodec := &RtpCodecCapability{MimeType: "video/H264", ClockRate: 90000}
	_, err = ReduceCodecs(params.Codecs, noMatchCodec)
	if err == nil {
		t.Fatal("expected error for non-matching codec")
	}
}

func TestCanReceive(t *testing.T) {
	extended := GetExtendedRtpCapabilities(sampleLocalCapabilities(), sampleRouterCapabilities(), false)
	recvCaps := GetRecvRtpCapabilities(extended)

	params := RtpParameters{
		Codecs: []RtpCodecParameters{{
			MimeType:    "video/VP8",
			PayloadType: 101,
			ClockRate:   90000,
			Parameters:  map[string]any{},
		}},
		HeaderExtensions: []RtpHeaderExtensionParameters{},
		Encodings:        []RtpEncodingParameters{},
		RTCP:             RtcpParameters{ReducedSize: true, Mux: true},
	}

	if !CanReceive(params, recvCaps) {
		t.Fatal("expected CanReceive to be true")
	}

	params.Codecs[0].PayloadType = 250
	if CanReceive(params, recvCaps) {
		t.Fatal("expected CanReceive to be false for unknown payload type")
	}
}

func TestGenerateProbatorRtpParameters(t *testing.T) {
	videoParams := RtpParameters{
		Codecs: []RtpCodecParameters{{
			MimeType:    "video/VP8",
			PayloadType: 101,
			ClockRate:   90000,
			Parameters:  map[string]any{},
		}},
		HeaderExtensions: []RtpHeaderExtensionParameters{{
			URI: "urn:ietf:params:rtp-hdrext:sdes:mid",
			ID:  1,
		}},
		Encodings: []RtpEncodingParameters{{}},
		RTCP:      RtcpParameters{ReducedSize: true, Mux: true},
	}

	probator, err := GenerateProbatorRtpParameters(videoParams)
	if err != nil {
		t.Fatalf("generate probator parameters failed: %v", err)
	}

	if probator.MID != "probator" {
		t.Fatalf("unexpected probator MID: %s", probator.MID)
	}
	if len(probator.Codecs) != 1 || probator.Codecs[0].PayloadType != 127 {
		t.Fatalf("unexpected probator codec payload type: %+v", probator.Codecs)
	}
	if len(probator.Encodings) != 1 || probator.Encodings[0].SSRC == nil || *probator.Encodings[0].SSRC != 1234 {
		t.Fatalf("unexpected probator encoding: %+v", probator.Encodings)
	}
}

func TestGenerateProbatorRtpParametersFromCapabilities(t *testing.T) {
	probator, err := GenerateProbatorRtpParametersFromCapabilities(sampleLocalCapabilities())
	if err != nil {
		t.Fatalf("generate probator parameters from capabilities failed: %v", err)
	}

	if probator.MID != "probator" {
		t.Fatalf("unexpected probator MID: %s", probator.MID)
	}
	if len(probator.Codecs) != 1 {
		t.Fatalf("expected one codec, got %d", len(probator.Codecs))
	}
	if probator.Codecs[0].MimeType != "video/VP8" {
		t.Fatalf("unexpected codec mime type: %s", probator.Codecs[0].MimeType)
	}
	if probator.Codecs[0].PayloadType != 127 {
		t.Fatalf("unexpected payload type: %d", probator.Codecs[0].PayloadType)
	}
	if len(probator.HeaderExtensions) == 0 {
		t.Fatal("expected video header extensions")
	}
	if len(probator.Encodings) != 1 || probator.Encodings[0].SSRC == nil || *probator.Encodings[0].SSRC != 1234 {
		t.Fatalf("unexpected probator encoding: %+v", probator.Encodings)
	}
}

func TestGenerateProbatorRtpParametersFromCapabilitiesNoVideo(t *testing.T) {
	caps := RtpCapabilities{
		Codecs: []RtpCodecCapability{
			{
				Kind:                 MediaKindAudio,
				MimeType:             "audio/opus",
				PreferredPayloadType: 111,
				ClockRate:            48000,
				Channels:             2,
			},
		},
	}

	if _, err := GenerateProbatorRtpParametersFromCapabilities(caps); err == nil {
		t.Fatal("expected error when no video codec is present")
	}
}

func TestValidateAndNormalizeSctpStreamParameters(t *testing.T) {
	streamID := uint16(7)

	params := SctpStreamParameters{StreamID: &streamID}
	if err := ValidateAndNormalizeSctpStreamParameters(&params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params.Ordered == nil || !*params.Ordered {
		t.Fatalf("expected ordered=true default, got %+v", params.Ordered)
	}

	maxPacketLifeTime := uint16(1500)
	params = SctpStreamParameters{
		StreamID:          &streamID,
		MaxPacketLifeTime: &maxPacketLifeTime,
	}
	if err := ValidateAndNormalizeSctpStreamParameters(&params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if params.Ordered == nil || *params.Ordered {
		t.Fatalf("expected ordered=false when maxPacketLifeTime is set, got %+v", params.Ordered)
	}

	orderedTrue := true
	params = SctpStreamParameters{
		StreamID:          &streamID,
		Ordered:           &orderedTrue,
		MaxPacketLifeTime: &maxPacketLifeTime,
	}
	if err := ValidateAndNormalizeSctpStreamParameters(&params); err == nil {
		t.Fatal("expected error when ordered=true and maxPacketLifeTime is set")
	}

	maxRetransmits := uint16(3)
	orderedFalse := false
	params = SctpStreamParameters{
		StreamID:          &streamID,
		Ordered:           &orderedFalse,
		MaxPacketLifeTime: &maxPacketLifeTime,
		MaxRetransmits:    &maxRetransmits,
	}
	if err := ValidateAndNormalizeSctpStreamParameters(&params); err == nil {
		t.Fatal("expected error when both maxPacketLifeTime and maxRetransmits are set")
	}

	params = SctpStreamParameters{}
	if err := ValidateAndNormalizeSctpStreamParameters(&params); err == nil {
		t.Fatal("expected error when streamId is missing")
	}
}

func sampleLocalCapabilities() RtpCapabilities {
	return RtpCapabilities{
		Codecs: []RtpCodecCapability{
			{
				Kind:                 MediaKindAudio,
				MimeType:             "audio/opus",
				PreferredPayloadType: 111,
				ClockRate:            48000,
				Channels:             2,
				Parameters:           map[string]any{"minptime": 10, "useinbandfec": 1},
				RTCPFeedback:         []RtcpFeedback{{Type: "transport-cc"}},
			},
			{
				Kind:                 MediaKindVideo,
				MimeType:             "video/VP8",
				PreferredPayloadType: 96,
				ClockRate:            90000,
				Parameters:           map[string]any{},
				RTCPFeedback: []RtcpFeedback{
					{Type: "nack"},
					{Type: "nack", Parameter: "pli"},
					{Type: "goog-remb"},
					{Type: "transport-cc"},
				},
			},
			{
				Kind:                 MediaKindVideo,
				MimeType:             "video/rtx",
				PreferredPayloadType: 97,
				ClockRate:            90000,
				Parameters:           map[string]any{"apt": 96},
				RTCPFeedback:         []RtcpFeedback{},
			},
		},
		HeaderExtensions: []RtpHeaderExtension{
			{Kind: MediaKindAudio, URI: "urn:ietf:params:rtp-hdrext:sdes:mid", PreferredID: 1, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "urn:ietf:params:rtp-hdrext:sdes:mid", PreferredID: 1, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time", PreferredID: 4, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01", PreferredID: 5, Direction: RTPDirectionSendRecv},
		},
	}
}

func sampleRouterCapabilities() RtpCapabilities {
	return RtpCapabilities{
		Codecs: []RtpCodecCapability{
			{
				Kind:                 MediaKindVideo,
				MimeType:             "video/VP8",
				PreferredPayloadType: 101,
				ClockRate:            90000,
				Parameters:           map[string]any{},
				RTCPFeedback: []RtcpFeedback{
					{Type: "nack"},
					{Type: "nack", Parameter: "pli"},
					{Type: "goog-remb"},
					{Type: "transport-cc"},
				},
			},
			{
				Kind:                 MediaKindVideo,
				MimeType:             "video/rtx",
				PreferredPayloadType: 102,
				ClockRate:            90000,
				Parameters:           map[string]any{"apt": 101},
				RTCPFeedback:         []RtcpFeedback{},
			},
			{
				Kind:                 MediaKindAudio,
				MimeType:             "audio/opus",
				PreferredPayloadType: 100,
				ClockRate:            48000,
				Channels:             2,
				Parameters:           map[string]any{"useinbandfec": 1},
				RTCPFeedback:         []RtcpFeedback{{Type: "transport-cc"}},
			},
		},
		HeaderExtensions: []RtpHeaderExtension{
			{Kind: MediaKindAudio, URI: "urn:ietf:params:rtp-hdrext:sdes:mid", PreferredID: 1, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "urn:ietf:params:rtp-hdrext:sdes:mid", PreferredID: 1, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time", PreferredID: 4, Direction: RTPDirectionSendRecv},
			{Kind: MediaKindVideo, URI: "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01", PreferredID: 5, Direction: RTPDirectionSendRecv},
		},
	}
}
