package mediasoupclient

import (
	"strings"
	"testing"

	pionsdp "github.com/pion/sdp/v3"
)

func TestBuildSendAnswerSDPIncludesExpectedMediaSections(t *testing.T) {
	ice := IceParameters{UsernameFragment: "ufrag", Password: "pwd", ICELite: true}
	dtls := DtlsParameters{
		Role:         DtlsRoleServer,
		Fingerprints: []DtlsFingerprint{{Algorithm: "sha-256", Value: "AA:BB:CC:DD"}},
	}
	sctp := &SctpParameters{Port: 5000, OS: 1024, MIS: 1024, MaxMessageSize: 262144}

	offer := newBaseSDP(1111, 1, ice, dtls)
	audioOffer := newMediaDescription("audio", 9, []string{"UDP", "TLS", "RTP", "SAVPF"}, []string{"111"})
	audioOffer.WithValueAttribute("mid", "mid-1")
	audioOffer.WithPropertyAttribute("sendonly")
	audioOffer.WithValueAttribute("rtpmap", "111 opus/48000/2")
	offer.WithMedia(audioOffer)

	appOffer := newMediaDescription("application", 9, []string{"UDP", "DTLS", "SCTP"}, []string{"webrtc-datachannel"})
	appOffer.WithValueAttribute("mid", "datachannel")
	appOffer.WithValueAttribute("sctp-port", "5000")
	offer.WithMedia(appOffer)

	offer.WithValueAttribute("group", "BUNDLE mid-1 datachannel")
	offerRaw, err := offer.Marshal()
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}

	remoteRtpByMID := map[string]RtpParameters{
		"mid-1": {
			MID: "mid-1",
			Codecs: []RtpCodecParameters{
				{MimeType: "audio/opus", PayloadType: 111, ClockRate: 48000, Channels: 2},
			},
			HeaderExtensions: []RtpHeaderExtensionParameters{{URI: "urn:ietf:params:rtp-hdrext:sdes:mid", ID: 1}},
		},
	}

	answerSDP, err := buildSendAnswerSDP(
		string(offerRaw),
		2222,
		2,
		ice,
		[]IceCandidate{{
			Foundation: "1",
			Priority:   1,
			Address:    "127.0.0.1",
			Protocol:   "udp",
			Port:       40000,
			Type:       "host",
		}},
		dtls,
		remoteRtpByMID,
		true,
		sctp,
	)
	if err != nil {
		t.Fatalf("build answer: %v", err)
	}

	var answer pionsdp.SessionDescription
	if err := answer.UnmarshalString(answerSDP); err != nil {
		t.Fatalf("parse answer: %v", err)
	}

	if len(answer.MediaDescriptions) != 2 {
		t.Fatalf("expected 2 media sections, got %d", len(answer.MediaDescriptions))
	}

	audio := answer.MediaDescriptions[0]
	if _, ok := audio.Attribute("recvonly"); !ok {
		t.Fatalf("expected audio media to be recvonly")
	}
	if setup, ok := audio.Attribute("setup"); !ok || setup != "passive" {
		t.Fatalf("expected passive setup, got %q", setup)
	}

	application := answer.MediaDescriptions[1]
	if value, ok := application.Attribute("sctp-port"); !ok || value != "5000" {
		t.Fatalf("expected sctp-port=5000, got %q", value)
	}
}

func TestBuildRecvOfferSDPIncludesDataChannelAndSendonlyMedia(t *testing.T) {
	ice := IceParameters{UsernameFragment: "ufrag", Password: "pwd"}
	dtls := DtlsParameters{
		Role:         DtlsRoleServer,
		Fingerprints: []DtlsFingerprint{{Algorithm: "sha-256", Value: "AA:BB:CC:DD"}},
	}
	sctp := &SctpParameters{Port: 5000, OS: 1024, MIS: 1024, MaxMessageSize: 262144}

	ssrc := uint32(12345)
	offerSDP, err := buildRecvOfferSDP(
		3333,
		1,
		ice,
		nil,
		dtls,
		[]recvOfferMediaSection{{
			MID:  "mid-1",
			Kind: MediaKindAudio,
			RtpParameters: RtpParameters{
				MID: "mid-1",
				Codecs: []RtpCodecParameters{{
					MimeType:    "audio/opus",
					PayloadType: 111,
					ClockRate:   48000,
					Channels:    2,
				}},
				HeaderExtensions: []RtpHeaderExtensionParameters{{URI: "urn:ietf:params:rtp-hdrext:sdes:mid", ID: 1}},
				Encodings:        []RtpEncodingParameters{{SSRC: &ssrc}},
				RTCP:             RtcpParameters{CNAME: "recv-cname"},
			},
			StreamID: "stream-1",
			TrackID:  "track-1",
		}},
		true,
		sctp,
	)
	if err != nil {
		t.Fatalf("build offer: %v", err)
	}

	if !strings.Contains(offerSDP, "a=group:BUNDLE mid-1 datachannel") {
		t.Fatalf("expected bundle group to include datachannel, got SDP:\n%s", offerSDP)
	}

	var offer pionsdp.SessionDescription
	if err := offer.UnmarshalString(offerSDP); err != nil {
		t.Fatalf("parse offer: %v", err)
	}

	if len(offer.MediaDescriptions) != 2 {
		t.Fatalf("expected 2 media sections, got %d", len(offer.MediaDescriptions))
	}

	audio := offer.MediaDescriptions[0]
	if _, ok := audio.Attribute("sendonly"); !ok {
		t.Fatalf("expected sendonly audio media")
	}
	if _, ok := audio.Attribute("ssrc"); !ok {
		t.Fatalf("expected ssrc attributes in audio media")
	}

	application := offer.MediaDescriptions[1]
	if value, ok := application.Attribute("sctp-port"); !ok || value != "5000" {
		t.Fatalf("expected sctp-port=5000, got %q", value)
	}
}
