package mediasoupclient

import "github.com/pion/webrtc/v4"

// MediaKind mirrors mediasoup-client's MediaKind ('audio' | 'video').
type MediaKind string

const (
	MediaKindAudio MediaKind = "audio"
	MediaKindVideo MediaKind = "video"
)

// RTPDirection mirrors the transceiver direction strings used in mediasoup-client ORTC helpers.
type RTPDirection string

const (
	RTPDirectionSendRecv RTPDirection = "sendrecv"
	RTPDirectionSendOnly RTPDirection = "sendonly"
	RTPDirectionRecvOnly RTPDirection = "recvonly"
	RTPDirectionInactive RTPDirection = "inactive"
)

// AppData mirrors mediasoup-client appData objects.
type AppData map[string]any

// RtpCapabilities mirrors mediasoup-client RtpCapabilities.
type RtpCapabilities struct {
	Codecs           []RtpCodecCapability `json:"codecs,omitempty"`
	HeaderExtensions []RtpHeaderExtension `json:"headerExtensions,omitempty"`
}

// RtpCodecCapability mirrors mediasoup-client RtpCodecCapability.
type RtpCodecCapability struct {
	Kind                 MediaKind      `json:"kind"`
	MimeType             string         `json:"mimeType"`
	PreferredPayloadType int            `json:"preferredPayloadType"`
	ClockRate            uint32         `json:"clockRate"`
	Channels             uint16         `json:"channels,omitempty"`
	Parameters           map[string]any `json:"parameters,omitempty"`
	RTCPFeedback         []RtcpFeedback `json:"rtcpFeedback,omitempty"`
}

// RtpHeaderExtension mirrors mediasoup-client RtpHeaderExtension.
type RtpHeaderExtension struct {
	Kind             MediaKind    `json:"kind"`
	URI              string       `json:"uri"`
	PreferredID      int          `json:"preferredId"`
	PreferredEncrypt bool         `json:"preferredEncrypt,omitempty"`
	Direction        RTPDirection `json:"direction,omitempty"`
}

// RtpParameters mirrors mediasoup-client RtpParameters.
type RtpParameters struct {
	MID              string                         `json:"mid,omitempty"`
	Codecs           []RtpCodecParameters           `json:"codecs"`
	HeaderExtensions []RtpHeaderExtensionParameters `json:"headerExtensions,omitempty"`
	Encodings        []RtpEncodingParameters        `json:"encodings,omitempty"`
	RTCP             RtcpParameters                 `json:"rtcp,omitempty"`
	MSID             string                         `json:"msid,omitempty"`
}

// RtpCodecParameters mirrors mediasoup-client RtpCodecParameters.
type RtpCodecParameters struct {
	MimeType     string         `json:"mimeType"`
	PayloadType  int            `json:"payloadType"`
	ClockRate    uint32         `json:"clockRate"`
	Channels     uint16         `json:"channels,omitempty"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	RTCPFeedback []RtcpFeedback `json:"rtcpFeedback,omitempty"`
}

// RtcpFeedback mirrors mediasoup-client RtcpFeedback.
type RtcpFeedback struct {
	Type      string `json:"type"`
	Parameter string `json:"parameter,omitempty"`
}

// RtpEncodingParameters mirrors mediasoup-client RtpEncodingParameters.
type RtpEncodingParameters struct {
	Active                *bool    `json:"active,omitempty"`
	SSRC                  *uint32  `json:"ssrc,omitempty"`
	RID                   string   `json:"rid,omitempty"`
	CodecPayloadType      *int     `json:"codecPayloadType,omitempty"`
	RTX                   *RtxInfo `json:"rtx,omitempty"`
	DTX                   *bool    `json:"dtx,omitempty"`
	ScalabilityMode       string   `json:"scalabilityMode,omitempty"`
	ScaleResolutionDownBy *float64 `json:"scaleResolutionDownBy,omitempty"`
	MaxBitrate            *uint64  `json:"maxBitrate,omitempty"`
	MaxFramerate          *uint32  `json:"maxFramerate,omitempty"`
	AdaptivePtime         *bool    `json:"adaptivePtime,omitempty"`
	Priority              string   `json:"priority,omitempty"`
	NetworkPriority       string   `json:"networkPriority,omitempty"`
}

// RtxInfo mirrors mediasoup-client encoding.rtx.
type RtxInfo struct {
	SSRC uint32 `json:"ssrc"`
}

// RtpHeaderExtensionParameters mirrors mediasoup-client RtpHeaderExtensionParameters.
type RtpHeaderExtensionParameters struct {
	URI        string         `json:"uri"`
	ID         int            `json:"id"`
	Encrypt    bool           `json:"encrypt,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// RtcpParameters mirrors mediasoup-client RtcpParameters.
type RtcpParameters struct {
	CNAME       string `json:"cname,omitempty"`
	ReducedSize bool   `json:"reducedSize,omitempty"`
	Mux         bool   `json:"mux,omitempty"`
}

// ExtendedRtpCapabilities mirrors mediasoup-client internal ExtendedRtpCapabilities.
type ExtendedRtpCapabilities struct {
	Codecs           []ExtendedRtpCodecCapability
	HeaderExtensions []ExtendedRtpHeaderExtension
}

// ExtendedRtpCodecCapability mirrors mediasoup-client ExtendedRtpCodecCapability.
type ExtendedRtpCodecCapability struct {
	Kind                 MediaKind
	MimeType             string
	LocalPayloadType     int
	LocalRtxPayloadType  *int
	RemotePayloadType    int
	RemoteRtxPayloadType *int
	ClockRate            uint32
	Channels             uint16
	LocalParameters      map[string]any
	RemoteParameters     map[string]any
	RTCPFeedback         []RtcpFeedback
}

// ExtendedRtpHeaderExtension mirrors mediasoup-client ExtendedRtpHeaderExtension.
type ExtendedRtpHeaderExtension struct {
	Kind      MediaKind
	URI       string
	SendID    int
	RecvID    int
	Encrypt   bool
	Direction RTPDirection
}

// IceParameters mirrors mediasoup-client IceParameters.
type IceParameters struct {
	UsernameFragment string `json:"usernameFragment"`
	Password         string `json:"password"`
	ICELite          bool   `json:"iceLite,omitempty"`
}

// IceCandidate mirrors mediasoup-client IceCandidate.
type IceCandidate struct {
	Foundation string `json:"foundation"`
	Priority   uint32 `json:"priority"`
	Address    string `json:"address"`
	IP         string `json:"ip,omitempty"`
	Protocol   string `json:"protocol"`
	Port       uint16 `json:"port"`
	Type       string `json:"type"`
	TCPType    string `json:"tcpType,omitempty"`
}

// DtlsParameters mirrors mediasoup-client DtlsParameters.
type DtlsParameters struct {
	Role         DtlsRole          `json:"role,omitempty"`
	Fingerprints []DtlsFingerprint `json:"fingerprints"`
}

// DtlsFingerprint mirrors mediasoup-client DtlsFingerprint.
type DtlsFingerprint struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// DtlsRole mirrors mediasoup-client DtlsRole.
type DtlsRole string

const (
	DtlsRoleAuto   DtlsRole = "auto"
	DtlsRoleClient DtlsRole = "client"
	DtlsRoleServer DtlsRole = "server"
)

// SctpCapabilities mirrors mediasoup-client SctpCapabilities.
type SctpCapabilities struct {
	NumStreams NumSctpStreams `json:"numStreams"`
}

// NumSctpStreams mirrors mediasoup-client NumSctpStreams.
type NumSctpStreams struct {
	OS  uint16 `json:"OS"`
	MIS uint16 `json:"MIS"`
}

// SctpParameters mirrors mediasoup-client SctpParameters.
type SctpParameters struct {
	Port           uint16 `json:"port"`
	OS             uint16 `json:"OS"`
	MIS            uint16 `json:"MIS"`
	MaxMessageSize uint32 `json:"maxMessageSize"`
}

// SctpStreamParameters mirrors mediasoup-client SctpStreamParameters.
type SctpStreamParameters struct {
	StreamID          *uint16 `json:"streamId,omitempty"`
	Ordered           *bool   `json:"ordered,omitempty"`
	MaxPacketLifeTime *uint16 `json:"maxPacketLifeTime,omitempty"`
	MaxRetransmits    *uint16 `json:"maxRetransmits,omitempty"`
	Label             string  `json:"label,omitempty"`
	Protocol          string  `json:"protocol,omitempty"`
}

// TransportConnectionState mirrors the high-level mediasoup-client transport states.
type TransportConnectionState string

const (
	TransportConnectionStateNew          TransportConnectionState = "new"
	TransportConnectionStateConnecting   TransportConnectionState = "connecting"
	TransportConnectionStateConnected    TransportConnectionState = "connected"
	TransportConnectionStateFailed       TransportConnectionState = "failed"
	TransportConnectionStateDisconnected TransportConnectionState = "disconnected"
	TransportConnectionStateClosed       TransportConnectionState = "closed"
)

func mediaKindToPion(kind MediaKind) (webrtc.RTPCodecType, bool) {
	switch kind {
	case MediaKindAudio:
		return webrtc.RTPCodecTypeAudio, true
	case MediaKindVideo:
		return webrtc.RTPCodecTypeVideo, true
	default:
		return 0, false
	}
}

func pionKindToMediaKind(kind webrtc.RTPCodecType) (MediaKind, bool) {
	switch kind {
	case webrtc.RTPCodecTypeAudio:
		return MediaKindAudio, true
	case webrtc.RTPCodecTypeVideo:
		return MediaKindVideo, true
	default:
		return "", false
	}
}
