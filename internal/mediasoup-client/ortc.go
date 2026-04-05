package mediasoupclient

import (
	"errors"
	"fmt"
	"strings"
)

const (
	rtpProbatorMID                = "probator"
	rtpProbatorSSRC        uint32 = 1234
	rtpProbatorPayloadType        = 127
)

// ValidateAndNormalizeRtpCapabilities mirrors mediasoup-client ortc.validateAndNormalizeRtpCapabilities().
func ValidateAndNormalizeRtpCapabilities(caps *RtpCapabilities) error {
	if caps == nil {
		return errors.New("caps is not an object")
	}

	if caps.Codecs == nil {
		caps.Codecs = []RtpCodecCapability{}
	}
	for i := range caps.Codecs {
		if err := validateAndNormalizeRtpCodecCapability(&caps.Codecs[i]); err != nil {
			return err
		}
	}

	if caps.HeaderExtensions == nil {
		caps.HeaderExtensions = []RtpHeaderExtension{}
	}
	for i := range caps.HeaderExtensions {
		if err := validateAndNormalizeRtpHeaderExtension(&caps.HeaderExtensions[i]); err != nil {
			return err
		}
	}

	return nil
}

// ValidateAndNormalizeRtpParameters mirrors mediasoup-client ortc.validateAndNormalizeRtpParameters().
func ValidateAndNormalizeRtpParameters(params *RtpParameters) error {
	if params == nil {
		return errors.New("params is not an object")
	}
	if params.Codecs == nil {
		return errors.New("missing params.codecs")
	}

	for i := range params.Codecs {
		if err := validateAndNormalizeRtpCodecParameters(&params.Codecs[i]); err != nil {
			return err
		}
	}

	if params.HeaderExtensions == nil {
		params.HeaderExtensions = []RtpHeaderExtensionParameters{}
	}
	for i := range params.HeaderExtensions {
		if err := validateAndNormalizeRtpHeaderExtensionParameters(&params.HeaderExtensions[i]); err != nil {
			return err
		}
	}

	if params.Encodings == nil {
		params.Encodings = []RtpEncodingParameters{}
	}
	for i := range params.Encodings {
		if err := validateAndNormalizeRtpEncodingParameters(&params.Encodings[i]); err != nil {
			return err
		}
	}

	if err := validateAndNormalizeRtcpParameters(&params.RTCP); err != nil {
		return err
	}

	return nil
}

// ValidateSctpCapabilities mirrors mediasoup-client ortc.validateSctpCapabilities().
func ValidateSctpCapabilities(caps *SctpCapabilities) error {
	if caps == nil {
		return errors.New("caps is not an object")
	}
	if caps.NumStreams.OS == 0 {
		return errors.New("missing caps.numStreams.OS")
	}
	if caps.NumStreams.MIS == 0 {
		return errors.New("missing caps.numStreams.MIS")
	}
	return nil
}

// ValidateAndNormalizeSctpStreamParameters mirrors mediasoup-client
// ortc.validateAndNormalizeSctpStreamParameters().
func ValidateAndNormalizeSctpStreamParameters(params *SctpStreamParameters) error {
	if params == nil {
		return errors.New("params is not an object")
	}
	if params.StreamID == nil {
		return errors.New("missing params.streamId")
	}

	orderedGiven := params.Ordered != nil
	if !orderedGiven {
		ordered := true
		params.Ordered = &ordered
	}

	if params.MaxPacketLifeTime != nil && params.MaxRetransmits != nil {
		return errors.New("cannot provide both maxPacketLifeTime and maxRetransmits")
	}

	if orderedGiven && *params.Ordered && (params.MaxPacketLifeTime != nil || params.MaxRetransmits != nil) {
		return errors.New("cannot be ordered with maxPacketLifeTime or maxRetransmits")
	} else if !orderedGiven && (params.MaxPacketLifeTime != nil || params.MaxRetransmits != nil) {
		ordered := false
		params.Ordered = &ordered
	}

	return nil
}

// GetExtendedRtpCapabilities mirrors mediasoup-client ortc.getExtendedRtpCapabilities().
func GetExtendedRtpCapabilities(
	localCaps RtpCapabilities,
	remoteCaps RtpCapabilities,
	preferLocalCodecsOrder bool,
) ExtendedRtpCapabilities {
	extended := ExtendedRtpCapabilities{
		Codecs:           []ExtendedRtpCodecCapability{},
		HeaderExtensions: []ExtendedRtpHeaderExtension{},
	}

	if preferLocalCodecsOrder {
		for _, localCodec := range localCaps.Codecs {
			if isRtxMime(localCodec.MimeType) {
				continue
			}

			var matchingRemote *RtpCodecCapability
			for i := range remoteCaps.Codecs {
				candidate := remoteCaps.Codecs[i]
				if matchCodecCapability(localCodec, candidate, true) {
					matchingRemote = &candidate
					break
				}
			}
			if matchingRemote == nil {
				continue
			}

			extCodec := ExtendedRtpCodecCapability{
				Kind:              localCodec.Kind,
				MimeType:          localCodec.MimeType,
				ClockRate:         localCodec.ClockRate,
				Channels:          localCodec.Channels,
				LocalPayloadType:  localCodec.PreferredPayloadType,
				RemotePayloadType: matchingRemote.PreferredPayloadType,
				LocalParameters:   cloneMap(localCodec.Parameters),
				RemoteParameters:  cloneMap(matchingRemote.Parameters),
				RTCPFeedback:      reduceRTCPFeedback(localCodec.RTCPFeedback, matchingRemote.RTCPFeedback),
			}
			extended.Codecs = append(extended.Codecs, extCodec)
		}
	} else {
		for _, remoteCodec := range remoteCaps.Codecs {
			if isRtxMime(remoteCodec.MimeType) {
				continue
			}

			var matchingLocal *RtpCodecCapability
			for i := range localCaps.Codecs {
				candidate := localCaps.Codecs[i]
				if matchCodecCapability(candidate, remoteCodec, true) {
					matchingLocal = &candidate
					break
				}
			}
			if matchingLocal == nil {
				continue
			}

			extCodec := ExtendedRtpCodecCapability{
				Kind:              matchingLocal.Kind,
				MimeType:          matchingLocal.MimeType,
				ClockRate:         matchingLocal.ClockRate,
				Channels:          matchingLocal.Channels,
				LocalPayloadType:  matchingLocal.PreferredPayloadType,
				RemotePayloadType: remoteCodec.PreferredPayloadType,
				LocalParameters:   cloneMap(matchingLocal.Parameters),
				RemoteParameters:  cloneMap(remoteCodec.Parameters),
				RTCPFeedback:      reduceRTCPFeedback(matchingLocal.RTCPFeedback, remoteCodec.RTCPFeedback),
			}
			extended.Codecs = append(extended.Codecs, extCodec)
		}
	}

	for i := range extended.Codecs {
		extCodec := &extended.Codecs[i]
		for _, localCodec := range localCaps.Codecs {
			if !isRtxMime(localCodec.MimeType) {
				continue
			}
			if apt, ok := intFromAny(localCodec.Parameters["apt"]); ok && apt == extCodec.LocalPayloadType {
				v := localCodec.PreferredPayloadType
				extCodec.LocalRtxPayloadType = &v
				break
			}
		}
		for _, remoteCodec := range remoteCaps.Codecs {
			if !isRtxMime(remoteCodec.MimeType) {
				continue
			}
			if apt, ok := intFromAny(remoteCodec.Parameters["apt"]); ok && apt == extCodec.RemotePayloadType {
				v := remoteCodec.PreferredPayloadType
				extCodec.RemoteRtxPayloadType = &v
				break
			}
		}
	}

	for _, remoteExt := range remoteCaps.HeaderExtensions {
		var matchingLocal *RtpHeaderExtension
		for i := range localCaps.HeaderExtensions {
			candidate := localCaps.HeaderExtensions[i]
			if matchHeaderExtensions(candidate, remoteExt) {
				matchingLocal = &candidate
				break
			}
		}
		if matchingLocal == nil {
			continue
		}

		direction := RTPDirectionSendRecv
		switch normalizeDirection(remoteExt.Direction) {
		case RTPDirectionRecvOnly:
			direction = RTPDirectionSendOnly
		case RTPDirectionSendOnly:
			direction = RTPDirectionRecvOnly
		case RTPDirectionInactive:
			direction = RTPDirectionInactive
		}

		extended.HeaderExtensions = append(extended.HeaderExtensions, ExtendedRtpHeaderExtension{
			Kind:      remoteExt.Kind,
			URI:       remoteExt.URI,
			SendID:    matchingLocal.PreferredID,
			RecvID:    remoteExt.PreferredID,
			Encrypt:   matchingLocal.PreferredEncrypt,
			Direction: direction,
		})
	}

	return extended
}

// GetRecvRtpCapabilities mirrors mediasoup-client ortc.getRecvRtpCapabilities().
func GetRecvRtpCapabilities(extended ExtendedRtpCapabilities) RtpCapabilities {
	return getRtpCapabilities(RTPDirectionRecvOnly, extended)
}

// GetSendRtpCapabilities mirrors mediasoup-client ortc.getSendRtpCapabilities().
func GetSendRtpCapabilities(extended ExtendedRtpCapabilities) RtpCapabilities {
	return getRtpCapabilities(RTPDirectionSendOnly, extended)
}

// GetSendingRtpParameters mirrors mediasoup-client ortc.getSendingRtpParameters().
func GetSendingRtpParameters(kind MediaKind, extended ExtendedRtpCapabilities) RtpParameters {
	rtp := RtpParameters{
		Codecs:           []RtpCodecParameters{},
		HeaderExtensions: []RtpHeaderExtensionParameters{},
		Encodings:        []RtpEncodingParameters{},
		RTCP:             RtcpParameters{},
	}

	for _, extCodec := range extended.Codecs {
		if extCodec.Kind != kind {
			continue
		}

		codec := RtpCodecParameters{
			MimeType:     extCodec.MimeType,
			PayloadType:  extCodec.LocalPayloadType,
			ClockRate:    extCodec.ClockRate,
			Channels:     extCodec.Channels,
			Parameters:   cloneMap(extCodec.LocalParameters),
			RTCPFeedback: cloneRTCPFeedback(extCodec.RTCPFeedback),
		}
		rtp.Codecs = append(rtp.Codecs, codec)

		if extCodec.LocalRtxPayloadType != nil {
			rtxCodec := RtpCodecParameters{
				MimeType:    fmt.Sprintf("%s/rtx", kind),
				PayloadType: *extCodec.LocalRtxPayloadType,
				ClockRate:   extCodec.ClockRate,
				Parameters: map[string]any{
					"apt": extCodec.LocalPayloadType,
				},
				RTCPFeedback: []RtcpFeedback{},
			}
			rtp.Codecs = append(rtp.Codecs, rtxCodec)
		}
	}

	for _, ext := range extended.HeaderExtensions {
		if ext.Kind != "" && ext.Kind != kind {
			continue
		}
		if ext.Direction != RTPDirectionSendRecv && ext.Direction != RTPDirectionSendOnly {
			continue
		}
		rtp.HeaderExtensions = append(rtp.HeaderExtensions, RtpHeaderExtensionParameters{
			URI:        ext.URI,
			ID:         ext.SendID,
			Encrypt:    ext.Encrypt,
			Parameters: map[string]any{},
		})
	}

	return rtp
}

// GetSendingRemoteRtpParameters mirrors mediasoup-client ortc.getSendingRemoteRtpParameters().
func GetSendingRemoteRtpParameters(kind MediaKind, extended ExtendedRtpCapabilities) RtpParameters {
	rtp := RtpParameters{
		Codecs:           []RtpCodecParameters{},
		HeaderExtensions: []RtpHeaderExtensionParameters{},
		Encodings:        []RtpEncodingParameters{},
		RTCP:             RtcpParameters{},
	}

	for _, extCodec := range extended.Codecs {
		if extCodec.Kind != kind {
			continue
		}

		codec := RtpCodecParameters{
			MimeType:     extCodec.MimeType,
			PayloadType:  extCodec.LocalPayloadType,
			ClockRate:    extCodec.ClockRate,
			Channels:     extCodec.Channels,
			Parameters:   cloneMap(extCodec.RemoteParameters),
			RTCPFeedback: cloneRTCPFeedback(extCodec.RTCPFeedback),
		}
		rtp.Codecs = append(rtp.Codecs, codec)

		if extCodec.LocalRtxPayloadType != nil {
			rtxCodec := RtpCodecParameters{
				MimeType:    fmt.Sprintf("%s/rtx", kind),
				PayloadType: *extCodec.LocalRtxPayloadType,
				ClockRate:   extCodec.ClockRate,
				Parameters: map[string]any{
					"apt": extCodec.LocalPayloadType,
				},
				RTCPFeedback: []RtcpFeedback{},
			}
			rtp.Codecs = append(rtp.Codecs, rtxCodec)
		}
	}

	for _, ext := range extended.HeaderExtensions {
		if ext.Kind != "" && ext.Kind != kind {
			continue
		}
		if ext.Direction != RTPDirectionSendRecv && ext.Direction != RTPDirectionSendOnly {
			continue
		}
		rtp.HeaderExtensions = append(rtp.HeaderExtensions, RtpHeaderExtensionParameters{
			URI:        ext.URI,
			ID:         ext.SendID,
			Encrypt:    ext.Encrypt,
			Parameters: map[string]any{},
		})
	}

	if hasHeaderExtension(rtp.HeaderExtensions,
		"http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01") {
		for i := range rtp.Codecs {
			rtp.Codecs[i].RTCPFeedback = filterRTCPFeedback(rtp.Codecs[i].RTCPFeedback, map[string]struct{}{
				"goog-remb": {},
			})
		}
	} else if hasHeaderExtension(rtp.HeaderExtensions,
		"http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time") {
		for i := range rtp.Codecs {
			rtp.Codecs[i].RTCPFeedback = filterRTCPFeedback(rtp.Codecs[i].RTCPFeedback, map[string]struct{}{
				"transport-cc": {},
			})
		}
	} else {
		for i := range rtp.Codecs {
			rtp.Codecs[i].RTCPFeedback = filterRTCPFeedback(rtp.Codecs[i].RTCPFeedback, map[string]struct{}{
				"transport-cc": {},
				"goog-remb":    {},
			})
		}
	}

	return rtp
}

// ReduceCodecs mirrors mediasoup-client ortc.reduceCodecs().
func ReduceCodecs(codecs []RtpCodecParameters, capCodec *RtpCodecCapability) ([]RtpCodecParameters, error) {
	if len(codecs) == 0 {
		return nil, errors.New("no codecs")
	}

	filtered := []RtpCodecParameters{}

	if capCodec == nil {
		filtered = append(filtered, cloneRtpCodecParameters(codecs[0]))
		if len(codecs) > 1 && isRtxMime(codecs[1].MimeType) {
			filtered = append(filtered, cloneRtpCodecParameters(codecs[1]))
		}
		return filtered, nil
	}

	for idx := range codecs {
		if matchCodecParametersToCapability(codecs[idx], *capCodec, true) {
			filtered = append(filtered, cloneRtpCodecParameters(codecs[idx]))
			if idx+1 < len(codecs) && isRtxMime(codecs[idx+1].MimeType) {
				filtered = append(filtered, cloneRtpCodecParameters(codecs[idx+1]))
			}
			break
		}
	}

	if len(filtered) == 0 {
		return nil, errors.New("no matching codec found")
	}

	return filtered, nil
}

// GenerateProbatorRtpParameters mirrors mediasoup-client ortc.generateProbatorRtpParameters().
func GenerateProbatorRtpParameters(videoRtpParameters RtpParameters) (RtpParameters, error) {
	cloned := cloneRtpParameters(videoRtpParameters)
	if err := ValidateAndNormalizeRtpParameters(&cloned); err != nil {
		return RtpParameters{}, err
	}
	if len(cloned.Codecs) == 0 {
		return RtpParameters{}, errors.New("videoRtpParameters has no codecs")
	}

	ssrc := rtpProbatorSSRC
	probator := RtpParameters{
		MID: rtpProbatorMID,
		Codecs: []RtpCodecParameters{
			cloneRtpCodecParameters(cloned.Codecs[0]),
		},
		HeaderExtensions: cloneRtpHeaderExtensionParameters(cloned.HeaderExtensions),
		Encodings: []RtpEncodingParameters{
			{SSRC: &ssrc},
		},
		RTCP: RtcpParameters{
			CNAME:       "probator",
			ReducedSize: true,
			Mux:         true,
		},
	}
	probator.Codecs[0].PayloadType = rtpProbatorPayloadType

	return probator, nil
}

// GenerateProbatorRtpParametersFromCapabilities builds RTP probator parameters
// directly from recv RTP capabilities.
//
// mediasoup-client creates the probator from the first video Consumer RTP
// parameters. This Go helper exists for Pion/server-side flows to pre-negotiate
// the probator receiver before the first remote video Consumer is created.
func GenerateProbatorRtpParametersFromCapabilities(recvRtpCapabilities RtpCapabilities) (RtpParameters, error) {
	caps := cloneRtpCapabilities(recvRtpCapabilities)
	if err := ValidateAndNormalizeRtpCapabilities(&caps); err != nil {
		return RtpParameters{}, err
	}

	var videoCodec *RtpCodecCapability
	for i := range caps.Codecs {
		codec := &caps.Codecs[i]
		if codec.Kind != MediaKindVideo {
			continue
		}
		if isRtxMime(codec.MimeType) {
			continue
		}
		videoCodec = codec
		break
	}
	if videoCodec == nil {
		return RtpParameters{}, errors.New("recvRtpCapabilities has no video media codec")
	}

	headerExtensions := make([]RtpHeaderExtensionParameters, 0, len(caps.HeaderExtensions))
	for _, ext := range caps.HeaderExtensions {
		if ext.Kind != MediaKindVideo {
			continue
		}
		if ext.URI == "" || ext.PreferredID <= 0 {
			continue
		}
		switch ext.Direction {
		case "", RTPDirectionSendRecv, RTPDirectionRecvOnly:
			headerExtensions = append(headerExtensions, RtpHeaderExtensionParameters{
				URI:        ext.URI,
				ID:         ext.PreferredID,
				Encrypt:    ext.PreferredEncrypt,
				Parameters: map[string]any{},
			})
		}
	}

	ssrc := rtpProbatorSSRC
	probator := RtpParameters{
		MID: rtpProbatorMID,
		Codecs: []RtpCodecParameters{
			{
				MimeType:     videoCodec.MimeType,
				PayloadType:  rtpProbatorPayloadType,
				ClockRate:    videoCodec.ClockRate,
				Channels:     videoCodec.Channels,
				Parameters:   cloneMap(videoCodec.Parameters),
				RTCPFeedback: cloneRTCPFeedback(videoCodec.RTCPFeedback),
			},
		},
		HeaderExtensions: headerExtensions,
		Encodings: []RtpEncodingParameters{
			{SSRC: &ssrc},
		},
		RTCP: RtcpParameters{
			CNAME:       "probator",
			ReducedSize: true,
			Mux:         true,
		},
	}

	if err := ValidateAndNormalizeRtpParameters(&probator); err != nil {
		return RtpParameters{}, err
	}

	return probator, nil
}

// CanSend mirrors mediasoup-client ortc.canSend().
func CanSend(kind MediaKind, rtpCapabilities RtpCapabilities) bool {
	for _, codec := range rtpCapabilities.Codecs {
		if codec.Kind == kind {
			return true
		}
	}
	return false
}

// CanReceive mirrors mediasoup-client ortc.canReceive().
func CanReceive(rtpParameters RtpParameters, rtpCapabilities RtpCapabilities) bool {
	params := cloneRtpParameters(rtpParameters)
	if err := ValidateAndNormalizeRtpParameters(&params); err != nil {
		return false
	}
	if len(params.Codecs) == 0 {
		return false
	}

	firstMediaCodec := params.Codecs[0]
	for _, codec := range rtpCapabilities.Codecs {
		if codec.PreferredPayloadType == firstMediaCodec.PayloadType {
			return true
		}
	}
	return false
}

func getRtpCapabilities(direction RTPDirection, extended ExtendedRtpCapabilities) RtpCapabilities {
	caps := RtpCapabilities{
		Codecs:           []RtpCodecCapability{},
		HeaderExtensions: []RtpHeaderExtension{},
	}

	for _, extCodec := range extended.Codecs {
		codec := RtpCodecCapability{
			Kind:                 extCodec.Kind,
			MimeType:             extCodec.MimeType,
			PreferredPayloadType: extCodec.RemotePayloadType,
			ClockRate:            extCodec.ClockRate,
			Channels:             extCodec.Channels,
			Parameters:           cloneMap(extCodec.LocalParameters),
			RTCPFeedback:         cloneRTCPFeedback(extCodec.RTCPFeedback),
		}
		caps.Codecs = append(caps.Codecs, codec)

		if extCodec.RemoteRtxPayloadType != nil {
			rtxCodec := RtpCodecCapability{
				Kind:                 extCodec.Kind,
				MimeType:             fmt.Sprintf("%s/rtx", extCodec.Kind),
				PreferredPayloadType: *extCodec.RemoteRtxPayloadType,
				ClockRate:            extCodec.ClockRate,
				Parameters: map[string]any{
					"apt": extCodec.RemotePayloadType,
				},
				RTCPFeedback: []RtcpFeedback{},
			}
			caps.Codecs = append(caps.Codecs, rtxCodec)
		}
	}

	for _, ext := range extended.HeaderExtensions {
		if ext.Direction != RTPDirectionSendRecv && ext.Direction != direction {
			continue
		}
		caps.HeaderExtensions = append(caps.HeaderExtensions, RtpHeaderExtension{
			Kind:             ext.Kind,
			URI:              ext.URI,
			PreferredID:      ext.RecvID,
			PreferredEncrypt: ext.Encrypt,
			Direction:        ext.Direction,
		})
	}

	return caps
}

func validateAndNormalizeRtpCodecCapability(codec *RtpCodecCapability) error {
	if codec == nil {
		return errors.New("codec is not an object")
	}
	if codec.MimeType == "" {
		return errors.New("missing codec.mimeType")
	}

	kind, err := parseKindFromMime(codec.MimeType)
	if err != nil {
		return err
	}
	codec.Kind = kind

	if codec.PreferredPayloadType < 0 {
		return errors.New("missing codec.preferredPayloadType")
	}
	if codec.ClockRate == 0 {
		return errors.New("missing codec.clockRate")
	}
	if codec.Kind == MediaKindAudio {
		if codec.Channels == 0 {
			codec.Channels = 1
		}
	} else {
		codec.Channels = 0
	}

	if codec.Parameters == nil {
		codec.Parameters = map[string]any{}
	}
	for k, v := range codec.Parameters {
		if v == nil {
			codec.Parameters[k] = ""
			continue
		}
		switch v.(type) {
		case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		default:
			return fmt.Errorf("invalid codec parameter [key:%s, value:%v]", k, v)
		}
		if k == "apt" {
			if _, ok := intFromAny(v); !ok {
				return errors.New("invalid codec apt parameter")
			}
		}
	}

	if codec.RTCPFeedback == nil {
		codec.RTCPFeedback = []RtcpFeedback{}
	}
	for i := range codec.RTCPFeedback {
		if err := validateAndNormalizeRtcpFeedback(&codec.RTCPFeedback[i]); err != nil {
			return err
		}
	}

	return nil
}

func validateAndNormalizeRtpHeaderExtension(ext *RtpHeaderExtension) error {
	if ext == nil {
		return errors.New("ext is not an object")
	}
	if ext.Kind != MediaKindAudio && ext.Kind != MediaKindVideo {
		return errors.New("invalid ext.kind")
	}
	if ext.URI == "" {
		return errors.New("missing ext.uri")
	}
	if ext.PreferredID <= 0 {
		return errors.New("missing ext.preferredId")
	}
	ext.Direction = normalizeDirection(ext.Direction)
	return nil
}

func validateAndNormalizeRtpCodecParameters(codec *RtpCodecParameters) error {
	if codec == nil {
		return errors.New("codec is not an object")
	}
	if codec.MimeType == "" {
		return errors.New("missing codec.mimeType")
	}

	kind, err := parseKindFromMime(codec.MimeType)
	if err != nil {
		return err
	}
	if codec.PayloadType < 0 {
		return errors.New("missing codec.payloadType")
	}
	if codec.ClockRate == 0 {
		return errors.New("missing codec.clockRate")
	}
	if kind == MediaKindAudio {
		if codec.Channels == 0 {
			codec.Channels = 1
		}
	} else {
		codec.Channels = 0
	}

	if codec.Parameters == nil {
		codec.Parameters = map[string]any{}
	}
	for k, v := range codec.Parameters {
		if v == nil {
			codec.Parameters[k] = ""
			continue
		}
		switch v.(type) {
		case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		default:
			return fmt.Errorf("invalid codec parameter [key:%s, value:%v]", k, v)
		}
		if k == "apt" {
			if _, ok := intFromAny(v); !ok {
				return errors.New("invalid codec apt parameter")
			}
		}
	}

	if codec.RTCPFeedback == nil {
		codec.RTCPFeedback = []RtcpFeedback{}
	}
	for i := range codec.RTCPFeedback {
		if err := validateAndNormalizeRtcpFeedback(&codec.RTCPFeedback[i]); err != nil {
			return err
		}
	}

	return nil
}

func validateAndNormalizeRtcpFeedback(fb *RtcpFeedback) error {
	if fb == nil {
		return errors.New("fb is not an object")
	}
	if fb.Type == "" {
		return errors.New("missing fb.type")
	}
	return nil
}

func validateAndNormalizeRtpHeaderExtensionParameters(ext *RtpHeaderExtensionParameters) error {
	if ext == nil {
		return errors.New("ext is not an object")
	}
	if ext.URI == "" {
		return errors.New("missing ext.uri")
	}
	if ext.ID <= 0 {
		return errors.New("missing ext.id")
	}
	if ext.Parameters == nil {
		ext.Parameters = map[string]any{}
	}
	for k, v := range ext.Parameters {
		if v == nil {
			ext.Parameters[k] = ""
			continue
		}
		switch v.(type) {
		case string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		default:
			return errors.New("invalid header extension parameter")
		}
	}
	return nil
}

func validateAndNormalizeRtpEncodingParameters(enc *RtpEncodingParameters) error {
	if enc == nil {
		return errors.New("encoding is not an object")
	}
	if enc.RTX != nil && enc.RTX.SSRC == 0 {
		return errors.New("missing encoding.rtx.ssrc")
	}
	if enc.DTX == nil {
		v := false
		enc.DTX = &v
	}
	return nil
}

func validateAndNormalizeRtcpParameters(rtcp *RtcpParameters) error {
	if rtcp == nil {
		return errors.New("rtcp is not an object")
	}
	// mediasoup-client defaults reducedSize to true when unset. Go bool cannot
	// represent undefined, so we keep the mediasoup default by forcing true.
	if !rtcp.ReducedSize {
		rtcp.ReducedSize = true
	}
	return nil
}

func parseKindFromMime(mime string) (MediaKind, error) {
	parts := strings.SplitN(strings.ToLower(mime), "/", 2)
	if len(parts) != 2 {
		return "", errors.New("invalid codec.mimeType")
	}
	switch parts[0] {
	case "audio":
		return MediaKindAudio, nil
	case "video":
		return MediaKindVideo, nil
	default:
		return "", errors.New("invalid codec.mimeType")
	}
}

func isRtxMime(mimeType string) bool {
	return strings.HasSuffix(strings.ToLower(mimeType), "/rtx")
}

func normalizeDirection(direction RTPDirection) RTPDirection {
	switch direction {
	case RTPDirectionSendRecv, RTPDirectionSendOnly, RTPDirectionRecvOnly, RTPDirectionInactive:
		return direction
	default:
		return RTPDirectionSendRecv
	}
}

func matchCodecCapability(a, b RtpCodecCapability, strict bool) bool {
	return matchCodecLike(
		a.MimeType,
		a.ClockRate,
		a.Channels,
		a.Parameters,
		b.MimeType,
		b.ClockRate,
		b.Channels,
		b.Parameters,
		strict,
	)
}

func matchCodecParametersToCapability(a RtpCodecParameters, b RtpCodecCapability, strict bool) bool {
	return matchCodecLike(
		a.MimeType,
		a.ClockRate,
		a.Channels,
		a.Parameters,
		b.MimeType,
		b.ClockRate,
		b.Channels,
		b.Parameters,
		strict,
	)
}

func matchCodecLike(
	aMime string,
	aRate uint32,
	aChannels uint16,
	aParams map[string]any,
	bMime string,
	bRate uint32,
	bChannels uint16,
	bParams map[string]any,
	strict bool,
) bool {
	if strings.ToLower(aMime) != strings.ToLower(bMime) {
		return false
	}
	if aRate != bRate {
		return false
	}

	if strings.HasPrefix(strings.ToLower(aMime), "audio/") {
		if aChannels == 0 {
			aChannels = 1
		}
		if bChannels == 0 {
			bChannels = 1
		}
	}
	if aChannels != bChannels {
		return false
	}

	if !strict {
		return true
	}

	mime := strings.ToLower(aMime)
	switch mime {
	case "video/h264":
		aPacketization := paramIntOrDefault(aParams, "packetization-mode", 0)
		bPacketization := paramIntOrDefault(bParams, "packetization-mode", 0)
		if aPacketization != bPacketization {
			return false
		}

		aProfile := strings.ToLower(paramStringOrEmpty(aParams, "profile-level-id"))
		bProfile := strings.ToLower(paramStringOrEmpty(bParams, "profile-level-id"))
		// mediasoup-client uses h264-profile-level-id for negotiation. We keep a
		// conservative strict match when both ends provide profile-level-id.
		if aProfile != "" && bProfile != "" && aProfile != bProfile {
			return false
		}
	case "video/vp9":
		aProfile := paramIntOrDefault(aParams, "profile-id", 0)
		bProfile := paramIntOrDefault(bParams, "profile-id", 0)
		if aProfile != bProfile {
			return false
		}
	}

	return true
}

func matchHeaderExtensions(a, b RtpHeaderExtension) bool {
	if a.Kind != "" && b.Kind != "" && a.Kind != b.Kind {
		return false
	}
	return a.URI == b.URI
}

func reduceRTCPFeedback(aFeedback, bFeedback []RtcpFeedback) []RtcpFeedback {
	result := []RtcpFeedback{}
	for _, a := range aFeedback {
		for _, b := range bFeedback {
			if b.Type != a.Type {
				continue
			}
			if b.Parameter == a.Parameter {
				result = append(result, b)
				break
			}
			if b.Parameter == "" && a.Parameter == "" {
				result = append(result, b)
				break
			}
		}
	}
	return result
}

func filterRTCPFeedback(feedback []RtcpFeedback, blockedTypes map[string]struct{}) []RtcpFeedback {
	filtered := make([]RtcpFeedback, 0, len(feedback))
	for _, fb := range feedback {
		if _, blocked := blockedTypes[fb.Type]; blocked {
			continue
		}
		filtered = append(filtered, fb)
	}
	return filtered
}

func hasHeaderExtension(exts []RtpHeaderExtensionParameters, uri string) bool {
	for _, ext := range exts {
		if ext.URI == uri {
			return true
		}
	}
	return false
}

func intFromAny(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int8:
		return int(n), true
	case int16:
		return int(n), true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case uint:
		return int(n), true
	case uint8:
		return int(n), true
	case uint16:
		return int(n), true
	case uint32:
		return int(n), true
	case uint64:
		return int(n), true
	case float32:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func paramIntOrDefault(params map[string]any, key string, fallback int) int {
	if params == nil {
		return fallback
	}
	v, ok := intFromAny(params[key])
	if !ok {
		return fallback
	}
	return v
}

func paramStringOrEmpty(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	raw, ok := params[key]
	if !ok || raw == nil {
		return ""
	}
	s, ok := raw.(string)
	if !ok {
		return ""
	}
	return s
}

func cloneRtpCapabilities(caps RtpCapabilities) RtpCapabilities {
	out := RtpCapabilities{
		Codecs:           make([]RtpCodecCapability, 0, len(caps.Codecs)),
		HeaderExtensions: make([]RtpHeaderExtension, 0, len(caps.HeaderExtensions)),
	}
	for _, c := range caps.Codecs {
		out.Codecs = append(out.Codecs, RtpCodecCapability{
			Kind:                 c.Kind,
			MimeType:             c.MimeType,
			PreferredPayloadType: c.PreferredPayloadType,
			ClockRate:            c.ClockRate,
			Channels:             c.Channels,
			Parameters:           cloneMap(c.Parameters),
			RTCPFeedback:         cloneRTCPFeedback(c.RTCPFeedback),
		})
	}
	out.HeaderExtensions = append(out.HeaderExtensions, caps.HeaderExtensions...)
	return out
}

func cloneRtpParameters(params RtpParameters) RtpParameters {
	out := RtpParameters{
		MID:              params.MID,
		Codecs:           make([]RtpCodecParameters, 0, len(params.Codecs)),
		HeaderExtensions: cloneRtpHeaderExtensionParameters(params.HeaderExtensions),
		Encodings:        make([]RtpEncodingParameters, 0, len(params.Encodings)),
		RTCP:             params.RTCP,
		MSID:             params.MSID,
	}
	for _, codec := range params.Codecs {
		out.Codecs = append(out.Codecs, cloneRtpCodecParameters(codec))
	}
	for _, enc := range params.Encodings {
		out.Encodings = append(out.Encodings, cloneRtpEncodingParameters(enc))
	}
	return out
}

func cloneRtpCodecParameters(codec RtpCodecParameters) RtpCodecParameters {
	return RtpCodecParameters{
		MimeType:     codec.MimeType,
		PayloadType:  codec.PayloadType,
		ClockRate:    codec.ClockRate,
		Channels:     codec.Channels,
		Parameters:   cloneMap(codec.Parameters),
		RTCPFeedback: cloneRTCPFeedback(codec.RTCPFeedback),
	}
}

func cloneRtpEncodingParameters(enc RtpEncodingParameters) RtpEncodingParameters {
	out := enc
	if enc.SSRC != nil {
		ssrc := *enc.SSRC
		out.SSRC = &ssrc
	}
	if enc.Active != nil {
		active := *enc.Active
		out.Active = &active
	}
	if enc.CodecPayloadType != nil {
		pt := *enc.CodecPayloadType
		out.CodecPayloadType = &pt
	}
	if enc.DTX != nil {
		dtx := *enc.DTX
		out.DTX = &dtx
	}
	if enc.ScaleResolutionDownBy != nil {
		v := *enc.ScaleResolutionDownBy
		out.ScaleResolutionDownBy = &v
	}
	if enc.MaxBitrate != nil {
		v := *enc.MaxBitrate
		out.MaxBitrate = &v
	}
	if enc.MaxFramerate != nil {
		v := *enc.MaxFramerate
		out.MaxFramerate = &v
	}
	if enc.AdaptivePtime != nil {
		v := *enc.AdaptivePtime
		out.AdaptivePtime = &v
	}
	if enc.RTX != nil {
		out.RTX = &RtxInfo{SSRC: enc.RTX.SSRC}
	}
	return out
}

func cloneRtpHeaderExtensionParameters(exts []RtpHeaderExtensionParameters) []RtpHeaderExtensionParameters {
	out := make([]RtpHeaderExtensionParameters, 0, len(exts))
	for _, ext := range exts {
		out = append(out, RtpHeaderExtensionParameters{
			URI:        ext.URI,
			ID:         ext.ID,
			Encrypt:    ext.Encrypt,
			Parameters: cloneMap(ext.Parameters),
		})
	}
	return out
}

func cloneRTCPFeedback(feedback []RtcpFeedback) []RtcpFeedback {
	if len(feedback) == 0 {
		return []RtcpFeedback{}
	}
	out := make([]RtcpFeedback, len(feedback))
	copy(out, feedback)
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch typed := v.(type) {
		case map[string]any:
			out[k] = cloneMap(typed)
		default:
			out[k] = typed
		}
	}
	return out
}

func cloneSctpStreamParameters(in SctpStreamParameters) SctpStreamParameters {
	out := in
	if in.StreamID != nil {
		v := *in.StreamID
		out.StreamID = &v
	}
	if in.Ordered != nil {
		v := *in.Ordered
		out.Ordered = &v
	}
	if in.MaxPacketLifeTime != nil {
		v := *in.MaxPacketLifeTime
		out.MaxPacketLifeTime = &v
	}
	if in.MaxRetransmits != nil {
		v := *in.MaxRetransmits
		out.MaxRetransmits = &v
	}
	return out
}
