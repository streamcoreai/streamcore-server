package mediasoupclient

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	pionsdp "github.com/pion/sdp/v3"
)

func buildSendAnswerSDP(
	localOfferSDP string,
	sessionID uint64,
	sessionVersion uint64,
	iceParameters IceParameters,
	iceCandidates []IceCandidate,
	dtlsParameters DtlsParameters,
	remoteRtpByMID map[string]RtpParameters,
	includeDataSection bool,
	sctpParameters *SctpParameters,
) (string, error) {
	var localOffer pionsdp.SessionDescription
	if err := localOffer.UnmarshalString(localOfferSDP); err != nil {
		return "", fmt.Errorf("parse local offer SDP: %w", err)
	}

	mids := make([]string, 0, len(localOffer.MediaDescriptions))
	answer := newBaseSDP(sessionID, sessionVersion, iceParameters, dtlsParameters)
	if hasExtmapAllowMixed(&localOffer) {
		answer.WithPropertyAttribute("extmap-allow-mixed")
	}

	for i, offerMedia := range localOffer.MediaDescriptions {
		if offerMedia == nil {
			continue
		}

		mid, ok := offerMedia.Attribute("mid")
		if !ok || strings.TrimSpace(mid) == "" {
			mid = strconv.Itoa(i)
		}
		mids = append(mids, mid)

		mediaType := strings.ToLower(strings.TrimSpace(offerMedia.MediaName.Media))
		switch mediaType {
		case "audio", "video":
			kind := MediaKind(mediaType)
			rtpParameters, found := remoteRtpByMID[mid]
			if !found {
				if fallback, ok := findFirstRtpByKind(remoteRtpByMID, kind); ok {
					rtpParameters = fallback
					found = true
				}
			}

			if !found || len(rtpParameters.Codecs) == 0 {
				// Keep the m= section disabled when no matching RTP parameters exist.
				disabled := newMediaDescription(
					offerMedia.MediaName.Media,
					0,
					offerMedia.MediaName.Protos,
					offerMedia.MediaName.Formats,
				)
				disabled.WithValueAttribute("mid", mid)
				answer.WithMedia(disabled)
				continue
			}

			formats := codecFormats(rtpParameters.Codecs)
			answerMedia := newMediaDescription(
				offerMedia.MediaName.Media,
				7,
				offerMedia.MediaName.Protos,
				formats,
			)
			answerMedia.WithValueAttribute("mid", mid)
			answerMedia.WithPropertyAttribute("recvonly")
			answerMedia.WithPropertyAttribute("rtcp-mux")
			answerMedia.WithPropertyAttribute("rtcp-rsize")
			addTransportAttributes(answerMedia, iceParameters, iceCandidates, dtlsParameters, "passive")
			addCodecsToMedia(answerMedia, rtpParameters.Codecs)
			addHeaderExtensionsToMedia(answerMedia, rtpParameters.HeaderExtensions)

			if hasAttribute(offerMedia, "extmap-allow-mixed") {
				answerMedia.WithPropertyAttribute("extmap-allow-mixed")
			}

			answer.WithMedia(answerMedia)

		case "application":
			if !includeDataSection || sctpParameters == nil {
				disabled := newMediaDescription(
					offerMedia.MediaName.Media,
					0,
					offerMedia.MediaName.Protos,
					offerMedia.MediaName.Formats,
				)
				disabled.WithValueAttribute("mid", mid)
				answer.WithMedia(disabled)
				continue
			}

			formats := offerMedia.MediaName.Formats
			if len(formats) == 0 {
				formats = []string{"webrtc-datachannel"}
			}

			answerMedia := newMediaDescription(
				offerMedia.MediaName.Media,
				7,
				offerMedia.MediaName.Protos,
				formats,
			)
			answerMedia.WithValueAttribute("mid", mid)
			addTransportAttributes(answerMedia, iceParameters, iceCandidates, dtlsParameters, "passive")
			answerMedia.WithValueAttribute("sctp-port", strconv.FormatUint(uint64(sctpParameters.Port), 10))
			answerMedia.WithValueAttribute("max-message-size", strconv.FormatUint(uint64(sctpParameters.MaxMessageSize), 10))
			answer.WithMedia(answerMedia)

		default:
			// Unknown m= sections are kept disabled so SDP structure remains aligned.
			disabled := newMediaDescription(
				offerMedia.MediaName.Media,
				0,
				offerMedia.MediaName.Protos,
				offerMedia.MediaName.Formats,
			)
			disabled.WithValueAttribute("mid", mid)
			answer.WithMedia(disabled)
		}
	}

	if len(mids) > 0 {
		answer.WithValueAttribute("group", "BUNDLE "+strings.Join(mids, " "))
	}

	raw, err := answer.Marshal()
	if err != nil {
		return "", fmt.Errorf("marshal remote answer SDP: %w", err)
	}

	return string(raw), nil
}

func buildRecvOfferSDP(
	sessionID uint64,
	sessionVersion uint64,
	iceParameters IceParameters,
	iceCandidates []IceCandidate,
	dtlsParameters DtlsParameters,
	sections []recvOfferMediaSection,
	includeDataSection bool,
	sctpParameters *SctpParameters,
) (string, error) {
	offer := newBaseSDP(sessionID, sessionVersion, iceParameters, dtlsParameters)
	offer.WithPropertyAttribute("extmap-allow-mixed")

	mids := make([]string, 0, len(sections)+1)

	for _, section := range sections {
		if section.MID == "" {
			continue
		}

		mids = append(mids, section.MID)

		formats := codecFormats(section.RtpParameters.Codecs)
		if len(formats) == 0 {
			continue
		}

		offerMedia := newMediaDescription(
			string(section.Kind),
			7,
			[]string{"UDP", "TLS", "RTP", "SAVPF"},
			formats,
		)
		offerMedia.WithValueAttribute("mid", section.MID)
		offerMedia.WithPropertyAttribute("sendonly")
		offerMedia.WithPropertyAttribute("rtcp-mux")
		offerMedia.WithPropertyAttribute("rtcp-rsize")
		offerMedia.WithPropertyAttribute("extmap-allow-mixed")
		addTransportAttributes(offerMedia, iceParameters, iceCandidates, dtlsParameters, "actpass")
		addCodecsToMedia(offerMedia, section.RtpParameters.Codecs)
		addHeaderExtensionsToMedia(offerMedia, section.RtpParameters.HeaderExtensions)
		addMediaSources(offerMedia, section)

		offer.WithMedia(offerMedia)
	}

	if includeDataSection && sctpParameters != nil {
		mid := "datachannel"
		mids = append(mids, mid)

		offerMedia := newMediaDescription(
			"application",
			7,
			[]string{"UDP", "DTLS", "SCTP"},
			[]string{"webrtc-datachannel"},
		)
		offerMedia.WithValueAttribute("mid", mid)
		addTransportAttributes(offerMedia, iceParameters, iceCandidates, dtlsParameters, "actpass")
		offerMedia.WithValueAttribute("sctp-port", strconv.FormatUint(uint64(sctpParameters.Port), 10))
		offerMedia.WithValueAttribute("max-message-size", strconv.FormatUint(uint64(sctpParameters.MaxMessageSize), 10))
		offer.WithMedia(offerMedia)
	}

	if len(mids) > 0 {
		offer.WithValueAttribute("group", "BUNDLE "+strings.Join(mids, " "))
	}

	raw, err := offer.Marshal()
	if err != nil {
		return "", fmt.Errorf("marshal remote offer SDP: %w", err)
	}

	return string(raw), nil
}

func newBaseSDP(
	sessionID uint64,
	sessionVersion uint64,
	iceParameters IceParameters,
	dtlsParameters DtlsParameters,
) *pionsdp.SessionDescription {
	sdp := &pionsdp.SessionDescription{
		Version: 0,
		Origin: pionsdp.Origin{
			Username:       "go-mediasoup-client",
			SessionID:      sessionID,
			SessionVersion: sessionVersion,
			NetworkType:    "IN",
			AddressType:    "IP4",
			UnicastAddress: "0.0.0.0",
		},
		SessionName: "-",
		TimeDescriptions: []pionsdp.TimeDescription{
			{Timing: pionsdp.Timing{StartTime: 0, StopTime: 0}},
		},
	}

	sdp.WithValueAttribute("msid-semantic", "WMS *")
	sdp.WithValueAttribute("ice-options", "ice2")
	if iceParameters.ICELite {
		sdp.WithPropertyAttribute("ice-lite")
	}

	if fingerprint, ok := latestFingerprint(dtlsParameters); ok {
		sdp.WithFingerprint(fingerprint.Algorithm, fingerprint.Value)
	}

	return sdp
}

func newMediaDescription(media string, port int, protos []string, formats []string) *pionsdp.MediaDescription {
	return &pionsdp.MediaDescription{
		MediaName: pionsdp.MediaName{
			Media:   media,
			Port:    pionsdp.RangedPort{Value: port},
			Protos:  append([]string(nil), protos...),
			Formats: append([]string(nil), formats...),
		},
		ConnectionInformation: &pionsdp.ConnectionInformation{
			NetworkType: "IN",
			AddressType: "IP4",
			Address: &pionsdp.Address{
				Address: "127.0.0.1",
			},
		},
	}
}

func addTransportAttributes(
	media *pionsdp.MediaDescription,
	iceParameters IceParameters,
	iceCandidates []IceCandidate,
	dtlsParameters DtlsParameters,
	setup string,
) {
	media.WithICECredentials(iceParameters.UsernameFragment, iceParameters.Password)
	media.WithValueAttribute("ice-options", "renomination")

	for _, candidate := range iceCandidates {
		candidateSDP := toICECandidateSDP(candidate)
		if candidateSDP == "" {
			continue
		}
		media.WithValueAttribute("candidate", strings.TrimPrefix(candidateSDP, "candidate:"))
	}
	media.WithPropertyAttribute("end-of-candidates")

	if fingerprint, ok := latestFingerprint(dtlsParameters); ok {
		media.WithFingerprint(fingerprint.Algorithm, fingerprint.Value)
	}

	switch setup {
	case "active", "passive", "actpass":
		media.WithValueAttribute("setup", setup)
	default:
		media.WithValueAttribute("setup", "actpass")
	}
}

func addCodecsToMedia(media *pionsdp.MediaDescription, codecs []RtpCodecParameters) {
	for _, codec := range codecs {
		name := codecNameFromMimeType(codec.MimeType)
		if name == "" {
			continue
		}

		channels := codec.Channels
		if channels == 0 {
			channels = 1
		}

		media.WithCodec(
			uint8(codec.PayloadType),
			name,
			codec.ClockRate,
			channels,
			fmtpParameters(codec.Parameters),
		)

		for _, fb := range codec.RTCPFeedback {
			value := fmt.Sprintf("%d %s", codec.PayloadType, fb.Type)
			if fb.Parameter != "" {
				value += " " + fb.Parameter
			}
			media.WithValueAttribute("rtcp-fb", value)
		}
	}
}

func addHeaderExtensionsToMedia(media *pionsdp.MediaDescription, extensions []RtpHeaderExtensionParameters) {
	for _, extension := range extensions {
		if extension.ID <= 0 || extension.URI == "" {
			continue
		}

		value := fmt.Sprintf("%d %s", extension.ID, extension.URI)
		media.WithValueAttribute("extmap", value)
	}
}

func addMediaSources(media *pionsdp.MediaDescription, section recvOfferMediaSection) {
	if len(section.RtpParameters.Encodings) == 0 {
		return
	}

	encoding := section.RtpParameters.Encodings[0]
	if encoding.SSRC == nil || *encoding.SSRC == 0 {
		return
	}

	ssrc := *encoding.SSRC
	cname := strings.TrimSpace(section.RtpParameters.RTCP.CNAME)
	if cname == "" {
		cname = randomID("cname")
	}

	streamID := strings.TrimSpace(section.StreamID)
	if streamID == "" {
		streamID = "-"
	}
	trackID := strings.TrimSpace(section.TrackID)
	if trackID == "" {
		trackID = randomID("track")
	}

	media.WithValueAttribute("ssrc", fmt.Sprintf("%d cname:%s", ssrc, cname))
	media.WithValueAttribute("ssrc", fmt.Sprintf("%d msid:%s %s", ssrc, streamID, trackID))
	media.WithValueAttribute("ssrc", fmt.Sprintf("%d mslabel:%s", ssrc, streamID))
	media.WithValueAttribute("ssrc", fmt.Sprintf("%d label:%s", ssrc, trackID))

	if encoding.RTX != nil && encoding.RTX.SSRC != 0 {
		rtxSSRC := encoding.RTX.SSRC
		media.WithValueAttribute("ssrc-group", fmt.Sprintf("FID %d %d", ssrc, rtxSSRC))
		media.WithValueAttribute("ssrc", fmt.Sprintf("%d cname:%s", rtxSSRC, cname))
		media.WithValueAttribute("ssrc", fmt.Sprintf("%d msid:%s %s", rtxSSRC, streamID, trackID))
		media.WithValueAttribute("ssrc", fmt.Sprintf("%d mslabel:%s", rtxSSRC, streamID))
		media.WithValueAttribute("ssrc", fmt.Sprintf("%d label:%s", rtxSSRC, trackID))
	}
}

func codecFormats(codecs []RtpCodecParameters) []string {
	formats := make([]string, 0, len(codecs))
	for _, codec := range codecs {
		formats = append(formats, strconv.Itoa(codec.PayloadType))
	}
	return formats
}

func codecNameFromMimeType(mimeType string) string {
	parts := strings.SplitN(strings.TrimSpace(mimeType), "/", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

func fmtpParameters(parameters map[string]any) string {
	if len(parameters) == 0 {
		return ""
	}

	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := parameters[key]
		if value == nil {
			parts = append(parts, key)
			continue
		}
		parts = append(parts, key+"="+fmt.Sprint(value))
	}

	return strings.Join(parts, ";")
}

func latestFingerprint(parameters DtlsParameters) (DtlsFingerprint, bool) {
	if len(parameters.Fingerprints) == 0 {
		return DtlsFingerprint{}, false
	}
	return parameters.Fingerprints[len(parameters.Fingerprints)-1], true
}

func hasExtmapAllowMixed(sdpObject *pionsdp.SessionDescription) bool {
	if sdpObject == nil {
		return false
	}
	for _, attribute := range sdpObject.Attributes {
		if attribute.Key == "extmap-allow-mixed" {
			return true
		}
	}
	for _, media := range sdpObject.MediaDescriptions {
		if hasAttribute(media, "extmap-allow-mixed") {
			return true
		}
	}
	return false
}

func hasAttribute(media *pionsdp.MediaDescription, key string) bool {
	if media == nil {
		return false
	}
	_, ok := media.Attribute(key)
	return ok
}

func findFirstRtpByKind(in map[string]RtpParameters, kind MediaKind) (RtpParameters, bool) {
	for _, params := range in {
		if len(params.Codecs) == 0 {
			continue
		}
		for _, codec := range params.Codecs {
			if strings.HasPrefix(strings.ToLower(codec.MimeType), string(kind)+"/") {
				return cloneRtpParameters(params), true
			}
		}
	}
	return RtpParameters{}, false
}
