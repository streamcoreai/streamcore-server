package mediasoupclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	pionsdp "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

// ConnectRequest maps to mediasoup-client Transport "connect" event payload.
type ConnectRequest struct {
	DtlsParameters DtlsParameters
}

// ConnectFunc maps to mediasoup-client Transport "connect" callback flow.
type ConnectFunc func(ctx context.Context, request ConnectRequest) error

// ProduceRequest maps to mediasoup-client Transport "produce" event payload.
type ProduceRequest struct {
	Kind          MediaKind
	RtpParameters RtpParameters
	AppData       AppData
}

// ProduceFunc maps to mediasoup-client Transport "produce" callback flow.
type ProduceFunc func(ctx context.Context, request ProduceRequest) (producerID string, err error)

// ProduceDataRequest maps to mediasoup-client Transport "producedata" event payload.
type ProduceDataRequest struct {
	SctpStreamParameters SctpStreamParameters
	Label                string
	Protocol             string
	AppData              AppData
}

// ProduceDataFunc maps to mediasoup-client Transport "producedata" callback flow.
type ProduceDataFunc func(ctx context.Context, request ProduceDataRequest) (dataProducerID string, err error)

// BaseTransportOptions mirrors mediasoup-client TransportOptions plus Go callbacks.
type BaseTransportOptions struct {
	ID                 string
	ICEParameters      IceParameters
	ICECandidates      []IceCandidate
	DTLSParameters     DtlsParameters
	SCTPParameters     *SctpParameters
	ICEServers         []webrtc.ICEServer
	ICETransportPolicy webrtc.ICETransportPolicy
	AdditionalSettings *webrtc.Configuration
	AppData            AppData
	OnConnect          ConnectFunc
}

// SendTransportOptions extends BaseTransportOptions for send flow.
type SendTransportOptions struct {
	BaseTransportOptions
	OnProduce     ProduceFunc
	OnProduceData ProduceDataFunc
}

// RecvTransportOptions extends BaseTransportOptions for recv flow.
type RecvTransportOptions struct {
	BaseTransportOptions
}

// ProduceOptions mirrors mediasoup-client transport.produce() input in Go form.
type ProduceOptions struct {
	Track          webrtc.TrackLocal
	StreamID       string
	Encodings      []RtpEncodingParameters
	Codec          *RtpCodecCapability
	OnRTPSender    func(*webrtc.RTPSender)
	AppData        AppData
	ZeroRTPOnPause bool
}

// ConsumeOptions mirrors mediasoup-client transport.consume() input in Go form.
type ConsumeOptions struct {
	ID            string
	ProducerID    string
	Kind          MediaKind
	RtpParameters RtpParameters
	StreamID      string
	OnRTPReceiver func(*webrtc.RTPReceiver)
	AppData       AppData
}

// ProduceDataOptions mirrors mediasoup-client transport.produceData() input in Go form.
type ProduceDataOptions struct {
	Ordered           *bool
	MaxPacketLifeTime *uint16
	MaxRetransmits    *uint16
	Label             string
	Protocol          string
	AppData           AppData
}

// ConsumeDataOptions mirrors mediasoup-client transport.consumeData() input in Go form.
type ConsumeDataOptions struct {
	ID                   string
	DataProducerID       string
	SctpStreamParameters SctpStreamParameters
	Label                string
	Protocol             string
	AppData              AppData
}

type transportDirection string

const (
	transportDirectionSend transportDirection = "send"
	transportDirectionRecv transportDirection = "recv"
)

type transportCoreConfig struct {
	api         *webrtc.API
	direction   transportDirection
	baseOptions BaseTransportOptions

	getSendExtendedRtpCapabilities func(nativeSendRtpCapabilities RtpCapabilities) ExtendedRtpCapabilities
	nativeRtpCapabilities          NativeRtpCapabilitiesFunc
	recvRtpCapabilities            RtpCapabilities
	canProduceByKind               map[MediaKind]bool
}

type transportCore struct {
	api       *webrtc.API
	pc        *webrtc.PeerConnection
	direction transportDirection
	id        string

	mu              sync.Mutex
	negotiationMu   sync.Mutex
	closed          bool
	connected       bool
	connecting      bool
	connectWaitCh   chan struct{}
	connectErr      error
	connectionState TransportConnectionState

	onConnect ConnectFunc
	appData   AppData

	defaultSendStreamID string

	localDtlsParameters DtlsParameters

	remoteICEParameters  IceParameters
	remoteICECandidates  []IceCandidate
	remoteDTLSParameters DtlsParameters
	sctpParameters       *SctpParameters

	getSendExtendedRtpCapabilities func(nativeSendRtpCapabilities RtpCapabilities) ExtendedRtpCapabilities
	nativeRtpCapabilities          NativeRtpCapabilitiesFunc
	recvRtpCapabilities            RtpCapabilities
	canProduceByKind               map[MediaKind]bool

	nextLocalID uint64
	nextSCTPID  uint32

	producersByID        map[string]*Producer
	producersByLocalID   map[string]*Producer
	consumersByID        map[string]*Consumer
	consumersByLocalID   map[string]*Consumer
	consumersByReceiver  map[*webrtc.RTPReceiver]*Consumer
	dataProducersByID    map[string]*DataProducer
	dataConsumersByID    map[string]*DataConsumer
	dataConsumersByLocal map[string]*DataConsumer

	// mediasoup-client-like remote SDP state used to drive offer/answer with Pion.
	sdpSessionID      uint64
	sdpSessionVersion uint64

	sendAnswerRtpByMID      map[string]RtpParameters
	sendHasDataMedia        bool
	recvOfferMediaByMID     map[string]recvOfferMediaSection
	recvOfferMediaOrder     []string
	recvHasDataMedia        bool
	probatorConsumerCreated bool
}

type recvOfferMediaSection struct {
	MID           string
	Kind          MediaKind
	RtpParameters RtpParameters
	StreamID      string
	TrackID       string
}

// SendTransport mirrors mediasoup-client send Transport.
type SendTransport struct {
	core          *transportCore
	onProduce     ProduceFunc
	onProduceData ProduceDataFunc
}

// RecvTransport mirrors mediasoup-client recv Transport.
type RecvTransport struct {
	core *transportCore
}

func newTransportCore(config transportCoreConfig) (*transportCore, error) {
	if config.api == nil {
		return nil, errors.New("missing API")
	}
	if config.baseOptions.ID == "" {
		return nil, errors.New("missing id")
	}
	if config.baseOptions.ICEParameters.UsernameFragment == "" {
		return nil, errors.New("missing iceParameters.usernameFragment")
	}
	if config.baseOptions.ICEParameters.Password == "" {
		return nil, errors.New("missing iceParameters.password")
	}
	if len(config.baseOptions.DTLSParameters.Fingerprints) == 0 {
		return nil, errors.New("missing dtlsParameters.fingerprints")
	}

	pcConfig := webrtc.Configuration{}
	if config.baseOptions.AdditionalSettings != nil {
		pcConfig = *config.baseOptions.AdditionalSettings
	}
	pcConfig.ICEServers = cloneIceServers(config.baseOptions.ICEServers)
	if config.baseOptions.ICETransportPolicy != webrtc.ICETransportPolicy(0) {
		pcConfig.ICETransportPolicy = config.baseOptions.ICETransportPolicy
	}

	localDtlsParameters, certificate, err := generateLocalDtlsParameters()
	if err != nil {
		return nil, err
	}
	pcConfig.Certificates = []webrtc.Certificate{certificate}

	pc, err := config.api.NewPeerConnection(pcConfig)
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	core := &transportCore{
		api:                            config.api,
		pc:                             pc,
		direction:                      config.direction,
		id:                             config.baseOptions.ID,
		connectionState:                TransportConnectionStateNew,
		onConnect:                      config.baseOptions.OnConnect,
		appData:                        cloneAppData(config.baseOptions.AppData),
		defaultSendStreamID:            randomID("stream"),
		localDtlsParameters:            localDtlsParameters,
		remoteICEParameters:            config.baseOptions.ICEParameters,
		remoteICECandidates:            append([]IceCandidate(nil), config.baseOptions.ICECandidates...),
		remoteDTLSParameters:           config.baseOptions.DTLSParameters,
		sctpParameters:                 config.baseOptions.SCTPParameters,
		getSendExtendedRtpCapabilities: config.getSendExtendedRtpCapabilities,
		nativeRtpCapabilities:          config.nativeRtpCapabilities,
		recvRtpCapabilities:            cloneRtpCapabilities(config.recvRtpCapabilities),
		canProduceByKind:               cloneCanProduceByKind(config.canProduceByKind),
		producersByID:                  make(map[string]*Producer),
		producersByLocalID:             make(map[string]*Producer),
		consumersByID:                  make(map[string]*Consumer),
		consumersByLocalID:             make(map[string]*Consumer),
		consumersByReceiver:            make(map[*webrtc.RTPReceiver]*Consumer),
		dataProducersByID:              make(map[string]*DataProducer),
		dataConsumersByID:              make(map[string]*DataConsumer),
		dataConsumersByLocal:           make(map[string]*DataConsumer),
		sdpSessionID:                   uint64(mathrand.Uint32()) + 1,
		sdpSessionVersion:              0,
		sendAnswerRtpByMID:             make(map[string]RtpParameters),
		recvOfferMediaByMID:            make(map[string]recvOfferMediaSection),
		recvOfferMediaOrder:            make([]string, 0, 4),
	}

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		core.mu.Lock()
		defer core.mu.Unlock()
		if core.closed {
			return
		}
		core.connectionState = mapPionConnectionState(state)
	})

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track == nil {
			return
		}

		core.mu.Lock()
		consumer := core.consumersByID[track.ID()]
		if consumer == nil && receiver != nil {
			consumer = core.consumersByReceiver[receiver]
		}
		core.mu.Unlock()
		if consumer != nil {
			consumer.setTrack(track)
		}
	})

	return core, nil
}

func (t *transportCore) ensureConnected(ctx context.Context, localDTLS *DtlsParameters) error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("closed")
	}
	if t.connected {
		t.mu.Unlock()
		return nil
	}
	if t.onConnect == nil {
		t.mu.Unlock()
		return errors.New("no OnConnect callback configured")
	}

	if t.connecting {
		waitCh := t.connectWaitCh
		t.mu.Unlock()
		select {
		case <-waitCh:
			return t.connectErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	t.connecting = true
	t.connectWaitCh = make(chan struct{})
	t.connectionState = TransportConnectionStateConnecting

	dtlsParameters := cloneDtlsParameters(t.localDtlsParameters)
	if localDTLS != nil && len(localDTLS.Fingerprints) > 0 {
		dtlsParameters = cloneDtlsParameters(*localDTLS)
	}
	if dtlsParameters.Role == "" || dtlsParameters.Role == DtlsRoleAuto {
		dtlsParameters.Role = DtlsRoleClient
	}

	onConnect := t.onConnect
	waitCh := t.connectWaitCh
	t.mu.Unlock()

	err := onConnect(ctx, ConnectRequest{DtlsParameters: dtlsParameters})

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		t.connectErr = errors.New("closed")
	} else {
		t.connectErr = err
		if err == nil {
			t.connected = true
			t.localDtlsParameters = cloneDtlsParameters(dtlsParameters)
			t.connectionState = TransportConnectionStateConnected
		} else {
			t.connectionState = TransportConnectionStateFailed
		}
	}

	t.connecting = false
	close(waitCh)

	return t.connectErr
}

func (t *transportCore) nextLocalIDString() string {
	id := atomic.AddUint64(&t.nextLocalID, 1)
	return strconv.FormatUint(id, 10)
}

func (t *transportCore) close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}

	t.closed = true
	t.connectionState = TransportConnectionStateClosed

	producers := make([]*Producer, 0, len(t.producersByID))
	for _, producer := range t.producersByID {
		producers = append(producers, producer)
	}
	consumers := make([]*Consumer, 0, len(t.consumersByID))
	for _, consumer := range t.consumersByID {
		consumers = append(consumers, consumer)
	}
	dataProducers := make([]*DataProducer, 0, len(t.dataProducersByID))
	for _, dataProducer := range t.dataProducersByID {
		dataProducers = append(dataProducers, dataProducer)
	}
	dataConsumers := make([]*DataConsumer, 0, len(t.dataConsumersByID))
	for _, dataConsumer := range t.dataConsumersByID {
		dataConsumers = append(dataConsumers, dataConsumer)
	}

	t.producersByID = map[string]*Producer{}
	t.producersByLocalID = map[string]*Producer{}
	t.consumersByID = map[string]*Consumer{}
	t.consumersByLocalID = map[string]*Consumer{}
	t.consumersByReceiver = map[*webrtc.RTPReceiver]*Consumer{}
	t.dataProducersByID = map[string]*DataProducer{}
	t.dataConsumersByID = map[string]*DataConsumer{}
	t.dataConsumersByLocal = map[string]*DataConsumer{}
	t.sendAnswerRtpByMID = map[string]RtpParameters{}
	t.sendHasDataMedia = false
	t.recvOfferMediaByMID = map[string]recvOfferMediaSection{}
	t.recvOfferMediaOrder = nil
	t.recvHasDataMedia = false
	t.probatorConsumerCreated = false

	pc := t.pc
	t.mu.Unlock()

	for _, producer := range producers {
		producer.transportClosed()
	}
	for _, consumer := range consumers {
		consumer.transportClosed()
	}
	for _, dataProducer := range dataProducers {
		dataProducer.transportClosed()
	}
	for _, dataConsumer := range dataConsumers {
		dataConsumer.transportClosed()
	}

	if pc != nil {
		return pc.Close()
	}

	return nil
}

func (t *transportCore) removeProducer(localID string) error {
	t.mu.Lock()
	producer := t.producersByLocalID[localID]
	if producer == nil {
		t.mu.Unlock()
		return nil
	}
	delete(t.producersByLocalID, localID)
	delete(t.producersByID, producer.id)
	delete(t.sendAnswerRtpByMID, producer.rtpParameters.MID)
	pc := t.pc
	sender := producer.rtpSender
	t.mu.Unlock()

	if pc != nil && sender != nil {
		return pc.RemoveTrack(sender)
	}

	return nil
}

func (t *transportCore) removeConsumer(localID string) error {
	t.mu.Lock()
	consumer := t.consumersByLocalID[localID]
	if consumer == nil {
		t.mu.Unlock()
		return nil
	}
	delete(t.consumersByLocalID, localID)
	delete(t.consumersByID, consumer.id)
	if consumer.rtpReceiver != nil {
		delete(t.consumersByReceiver, consumer.rtpReceiver)
	}
	if consumer.rtpParameters.MID != "" {
		delete(t.recvOfferMediaByMID, consumer.rtpParameters.MID)
		for i, mid := range t.recvOfferMediaOrder {
			if mid == consumer.rtpParameters.MID {
				t.recvOfferMediaOrder = append(t.recvOfferMediaOrder[:i], t.recvOfferMediaOrder[i+1:]...)
				break
			}
		}
	}
	transceiver := consumer.transceiver
	t.mu.Unlock()

	if transceiver != nil {
		return transceiver.Stop()
	}

	return nil
}

func (t *transportCore) nextSCTPStreamID() (uint16, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.sctpParameters == nil {
		return 0, errors.New("SCTP not enabled")
	}

	mis := uint32(t.sctpParameters.MIS)
	if mis == 0 {
		mis = 65535
	}

	current := t.nextSCTPID % mis
	t.nextSCTPID = (t.nextSCTPID + 1) % mis

	return uint16(current), nil
}

func (t *transportCore) removeDataProducer(id string) {
	t.mu.Lock()
	delete(t.dataProducersByID, id)
	t.mu.Unlock()
}

func (t *transportCore) removeDataConsumer(localID string) {
	t.mu.Lock()
	if consumer := t.dataConsumersByLocal[localID]; consumer != nil {
		delete(t.dataConsumersByID, consumer.id)
	}
	delete(t.dataConsumersByLocal, localID)
	if len(t.dataConsumersByID) == 0 {
		t.recvHasDataMedia = false
	}
	t.mu.Unlock()
}

func (t *transportCore) negotiateSend(
	ctx context.Context,
	newMID string,
	newRemoteRtpParameters *RtpParameters,
	enableDataSection bool,
) error {
	t.negotiationMu.Lock()
	defer t.negotiationMu.Unlock()

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("closed")
	}

	remoteRtpByMID := make(map[string]RtpParameters, len(t.sendAnswerRtpByMID)+1)
	for mid, params := range t.sendAnswerRtpByMID {
		remoteRtpByMID[mid] = cloneRtpParameters(params)
	}
	if newMID != "" && newRemoteRtpParameters != nil {
		remoteRtpByMID[newMID] = cloneRtpParameters(*newRemoteRtpParameters)
	}

	includeDataSection := t.sendHasDataMedia || enableDataSection
	iceParameters := t.remoteICEParameters
	iceCandidates := append([]IceCandidate(nil), t.remoteICECandidates...)
	dtlsParameters := cloneDtlsParameters(t.remoteDTLSParameters)
	sctpParameters := t.sctpParameters
	sessionID := t.sdpSessionID
	t.sdpSessionVersion++
	sessionVersion := t.sdpSessionVersion
	t.mu.Unlock()

	offer, err := t.pc.CreateOffer(nil)
	if err != nil {
		return fmt.Errorf("create offer: %w", err)
	}

	localDTLSParameters, err := extractDtlsParametersFromSDP(offer.SDP)
	if err != nil {
		return fmt.Errorf("extract local DTLS parameters from offer: %w", err)
	}
	if err := t.ensureConnected(ctx, &localDTLSParameters); err != nil {
		return err
	}

	if err := t.pc.SetLocalDescription(offer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}

	localDescription := t.pc.LocalDescription()
	if localDescription == nil {
		return errors.New("missing local description after createOffer")
	}

	answerSDP, err := buildSendAnswerSDP(
		localDescription.SDP,
		sessionID,
		sessionVersion,
		iceParameters,
		iceCandidates,
		dtlsParameters,
		remoteRtpByMID,
		includeDataSection,
		sctpParameters,
	)
	if err != nil {
		return err
	}

	if err := t.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}); err != nil {
		return fmt.Errorf("set remote answer: %w", err)
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("closed")
	}
	t.sendAnswerRtpByMID = remoteRtpByMID
	if enableDataSection {
		t.sendHasDataMedia = true
	}
	t.mu.Unlock()

	return nil
}

func (t *transportCore) negotiateRecv(
	ctx context.Context,
	section *recvOfferMediaSection,
	enableDataSection bool,
) error {
	t.negotiationMu.Lock()
	defer t.negotiationMu.Unlock()

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("closed")
	}

	offerByMID := make(map[string]recvOfferMediaSection, len(t.recvOfferMediaByMID)+1)
	for mid, existing := range t.recvOfferMediaByMID {
		offerByMID[mid] = cloneRecvOfferMediaSection(existing)
	}
	offerOrder := append([]string(nil), t.recvOfferMediaOrder...)

	if section != nil {
		offerByMID[section.MID] = cloneRecvOfferMediaSection(*section)

		found := false
		for _, mid := range offerOrder {
			if mid == section.MID {
				found = true
				break
			}
		}
		if !found {
			offerOrder = append(offerOrder, section.MID)
		}
	}

	orderedSections := make([]recvOfferMediaSection, 0, len(offerOrder))
	for _, mid := range offerOrder {
		if media, ok := offerByMID[mid]; ok {
			orderedSections = append(orderedSections, cloneRecvOfferMediaSection(media))
		}
	}

	includeDataSection := t.recvHasDataMedia || enableDataSection
	iceParameters := t.remoteICEParameters
	iceCandidates := append([]IceCandidate(nil), t.remoteICECandidates...)
	dtlsParameters := cloneDtlsParameters(t.remoteDTLSParameters)
	sctpParameters := t.sctpParameters
	sessionID := t.sdpSessionID
	t.sdpSessionVersion++
	sessionVersion := t.sdpSessionVersion
	t.mu.Unlock()

	offerSDP, err := buildRecvOfferSDP(
		sessionID,
		sessionVersion,
		iceParameters,
		iceCandidates,
		dtlsParameters,
		orderedSections,
		includeDataSection,
		sctpParameters,
	)
	if err != nil {
		return err
	}

	if err := t.pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		return fmt.Errorf("set remote offer: %w", err)
	}

	answer, err := t.pc.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}

	localDTLSParameters, err := extractDtlsParametersFromSDP(answer.SDP)
	if err != nil {
		return fmt.Errorf("extract local DTLS parameters from answer: %w", err)
	}
	if err := t.ensureConnected(ctx, &localDTLSParameters); err != nil {
		return err
	}

	if err := t.pc.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local description: %w", err)
	}

	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("closed")
	}
	t.recvOfferMediaByMID = offerByMID
	t.recvOfferMediaOrder = offerOrder
	if enableDataSection {
		t.recvHasDataMedia = true
	}
	t.mu.Unlock()

	return nil
}

func (t *transportCore) addRemoteICECandidate(candidate IceCandidate) error {
	candidateSDP := toICECandidateSDP(candidate)
	if candidateSDP == "" {
		return errors.New("invalid ICE candidate")
	}

	return t.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: candidateSDP})
}

func (t *transportCore) ID() string {
	return t.id
}

func (t *transportCore) Direction() string {
	return string(t.direction)
}

func (t *transportCore) Closed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func (t *transportCore) ConnectionState() TransportConnectionState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connectionState
}

func (t *transportCore) PeerConnection() *webrtc.PeerConnection {
	return t.pc
}

func (t *transportCore) LocalDTLSParameters() DtlsParameters {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneDtlsParameters(t.localDtlsParameters)
}

func (t *transportCore) RemoteICEParameters() IceParameters {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.remoteICEParameters
}

func (t *transportCore) RemoteICECandidates() []IceCandidate {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]IceCandidate, len(t.remoteICECandidates))
	copy(out, t.remoteICECandidates)
	return out
}

func (t *transportCore) RemoteDTLSParameters() DtlsParameters {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneDtlsParameters(t.remoteDTLSParameters)
}

func (t *transportCore) SCTPParameters() *SctpParameters {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sctpParameters == nil {
		return nil
	}
	out := *t.sctpParameters
	return &out
}

func (t *transportCore) AppData() AppData {
	t.mu.Lock()
	defer t.mu.Unlock()
	return cloneAppData(t.appData)
}

func (t *transportCore) SetAppData(appData AppData) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appData = cloneAppData(appData)
}

// ID returns transport id.
func (t *SendTransport) ID() string { return t.core.ID() }

// Direction returns "send".
func (t *SendTransport) Direction() string { return t.core.Direction() }

// Closed reports whether transport is closed.
func (t *SendTransport) Closed() bool { return t.core.Closed() }

// ConnectionState returns high-level connection state.
func (t *SendTransport) ConnectionState() TransportConnectionState { return t.core.ConnectionState() }

// PeerConnection returns the underlying Pion PeerConnection.
func (t *SendTransport) PeerConnection() *webrtc.PeerConnection { return t.core.PeerConnection() }

// LocalDTLSParameters returns local DTLS parameters for the connect callback.
func (t *SendTransport) LocalDTLSParameters() DtlsParameters { return t.core.LocalDTLSParameters() }

// AddRemoteICECandidate adds an ICE candidate to the underlying PeerConnection.
func (t *SendTransport) AddRemoteICECandidate(candidate IceCandidate) error {
	return t.core.addRemoteICECandidate(candidate)
}

// RemoteICEParameters returns server-provided ICE parameters.
func (t *SendTransport) RemoteICEParameters() IceParameters { return t.core.RemoteICEParameters() }

// RemoteDTLSParameters returns server-provided DTLS parameters.
func (t *SendTransport) RemoteDTLSParameters() DtlsParameters { return t.core.RemoteDTLSParameters() }

// SCTPParameters returns SCTP parameters if enabled.
func (t *SendTransport) SCTPParameters() *SctpParameters { return t.core.SCTPParameters() }

// AppData returns app data.
func (t *SendTransport) AppData() AppData { return t.core.AppData() }

// SetAppData replaces app data.
func (t *SendTransport) SetAppData(appData AppData) { t.core.SetAppData(appData) }

// Close closes the transport and associated producers.
func (t *SendTransport) Close() error { return t.core.close() }

// Produce mirrors mediasoup-client transport.produce() flow.
func (t *SendTransport) Produce(ctx context.Context, options ProduceOptions) (*Producer, error) {
	if t.core.direction != transportDirectionSend {
		return nil, errors.New("not a sending transport")
	}
	if options.Track == nil {
		return nil, errors.New("missing track")
	}
	if t.onProduce == nil {
		return nil, errors.New("no OnProduce callback configured")
	}
	if t.core.Closed() {
		return nil, errors.New("closed")
	}

	kind, ok := pionKindToMediaKind(options.Track.Kind())
	if !ok {
		return nil, fmt.Errorf("unsupported track kind %v", options.Track.Kind())
	}
	if !t.core.canProduceByKind[kind] {
		return nil, fmt.Errorf("cannot produce %s", kind)
	}

	sender, err := t.core.pc.AddTrack(options.Track)
	if err != nil {
		return nil, fmt.Errorf("add track: %w", err)
	}
	startSenderRTCPDrain(sender)
	if options.OnRTPSender != nil {
		options.OnRTPSender(sender)
	}

	localID := t.core.nextLocalIDString()
	mid := "mid-" + localID
	if transceiver := findTransceiverBySender(t.core.pc, sender); transceiver != nil {
		if currentMid := transceiver.Mid(); currentMid != "" {
			mid = currentMid
		} else if err := transceiver.SetMid(mid); err == nil && transceiver.Mid() != "" {
			mid = transceiver.Mid()
		}
	}

	nativeSendRtpCapabilities, err := t.core.nativeRtpCapabilities(RTPDirectionSendOnly)
	if err != nil {
		_ = t.core.pc.RemoveTrack(sender)
		return nil, fmt.Errorf("get native send RTP capabilities: %w", err)
	}

	extendedSendRtpCapabilities := t.core.getSendExtendedRtpCapabilities(nativeSendRtpCapabilities)
	rtpParameters := GetSendingRtpParameters(kind, extendedSendRtpCapabilities)
	remoteRtpParameters := GetSendingRemoteRtpParameters(kind, extendedSendRtpCapabilities)

	if options.Codec != nil {
		filteredCodecs, codecErr := ReduceCodecs(rtpParameters.Codecs, options.Codec)
		if codecErr != nil {
			_ = t.core.pc.RemoveTrack(sender)
			return nil, codecErr
		}
		rtpParameters.Codecs = filteredCodecs

		filteredRemoteCodecs, codecErr := ReduceCodecs(remoteRtpParameters.Codecs, options.Codec)
		if codecErr != nil {
			_ = t.core.pc.RemoveTrack(sender)
			return nil, codecErr
		}
		remoteRtpParameters.Codecs = filteredRemoteCodecs
	}

	rtpParameters.MID = mid
	remoteRtpParameters.MID = mid

	useRTX := false
	for _, codec := range rtpParameters.Codecs {
		if isRtxMime(codec.MimeType) {
			useRTX = true
			break
		}
	}

	rtpParameters.Encodings = normalizeProduceEncodings(options.Encodings, useRTX)
	rtpParameters.RTCP = RtcpParameters{
		CNAME:       randomID("cname"),
		ReducedSize: true,
		Mux:         true,
	}

	streamID := options.StreamID
	if streamID == "" {
		streamID = t.core.defaultSendStreamID
	}
	rtpParameters.MSID = streamID + " " + options.Track.ID()
	remoteRtpParameters.MSID = rtpParameters.MSID

	if err := t.core.negotiateSend(ctx, mid, &remoteRtpParameters, false); err != nil {
		_ = t.core.pc.RemoveTrack(sender)
		return nil, err
	}

	// Align exposed RTP parameters with negotiated sender values so mediasoup
	// sees the same MID/SSRC tuple that Pion sends on the wire.
	if transceiver := findTransceiverBySender(t.core.pc, sender); transceiver != nil {
		if negotiatedMid := strings.TrimSpace(transceiver.Mid()); negotiatedMid != "" {
			rtpParameters.MID = negotiatedMid
		}
	}
	syncRtpEncodingsFromSender(sender, &rtpParameters)

	producerID, err := t.onProduce(ctx, ProduceRequest{
		Kind:          kind,
		RtpParameters: cloneRtpParameters(rtpParameters),
		AppData:       cloneAppData(options.AppData),
	})
	if err != nil {
		_ = t.core.pc.RemoveTrack(sender)
		return nil, err
	}
	if producerID == "" {
		_ = t.core.pc.RemoveTrack(sender)
		return nil, errors.New("OnProduce returned empty producer id")
	}

	producer := newProducer(producerConfig{
		id:             producerID,
		localID:        localID,
		track:          options.Track,
		rtpSender:      sender,
		rtpParameters:  rtpParameters,
		appData:        options.AppData,
		zeroRTPOnPause: options.ZeroRTPOnPause,
		transport:      t.core,
	})

	t.core.mu.Lock()
	t.core.producersByID[producer.id] = producer
	t.core.producersByLocalID[producer.localID] = producer
	t.core.mu.Unlock()

	return producer, nil
}

// ProduceData mirrors mediasoup-client transport.produceData() flow.
func (t *SendTransport) ProduceData(ctx context.Context, options ProduceDataOptions) (*DataProducer, error) {
	if t.core.direction != transportDirectionSend {
		return nil, errors.New("not a sending transport")
	}
	if t.core.Closed() {
		return nil, errors.New("closed")
	}
	if t.core.sctpParameters == nil {
		return nil, errors.New("SCTP not enabled by remote transport")
	}
	if t.onProduceData == nil {
		return nil, errors.New("no OnProduceData callback configured")
	}

	streamID, err := t.core.nextSCTPStreamID()
	if err != nil {
		return nil, err
	}

	sctpStreamParameters := SctpStreamParameters{
		StreamID:          &streamID,
		Ordered:           options.Ordered,
		MaxPacketLifeTime: options.MaxPacketLifeTime,
		MaxRetransmits:    options.MaxRetransmits,
		Label:             options.Label,
		Protocol:          options.Protocol,
	}
	if err := ValidateAndNormalizeSctpStreamParameters(&sctpStreamParameters); err != nil {
		return nil, err
	}

	negotiated := true
	channelInit := &webrtc.DataChannelInit{
		Ordered:           sctpStreamParameters.Ordered,
		MaxPacketLifeTime: sctpStreamParameters.MaxPacketLifeTime,
		MaxRetransmits:    sctpStreamParameters.MaxRetransmits,
		Negotiated:        &negotiated,
		ID:                sctpStreamParameters.StreamID,
	}
	if sctpStreamParameters.Protocol != "" {
		protocol := sctpStreamParameters.Protocol
		channelInit.Protocol = &protocol
	}

	dataChannel, err := t.core.pc.CreateDataChannel(sctpStreamParameters.Label, channelInit)
	if err != nil {
		return nil, fmt.Errorf("create data channel: %w", err)
	}

	t.core.mu.Lock()
	needsDataNegotiation := !t.core.sendHasDataMedia
	t.core.mu.Unlock()

	if needsDataNegotiation {
		if err := t.core.negotiateSend(ctx, "", nil, true); err != nil {
			_ = dataChannel.Close()
			return nil, err
		}
	} else if err := t.core.ensureConnected(ctx, nil); err != nil {
		_ = dataChannel.Close()
		return nil, err
	}

	dataProducerID, err := t.onProduceData(ctx, ProduceDataRequest{
		SctpStreamParameters: cloneSctpStreamParameters(sctpStreamParameters),
		Label:                sctpStreamParameters.Label,
		Protocol:             sctpStreamParameters.Protocol,
		AppData:              cloneAppData(options.AppData),
	})
	if err != nil {
		_ = dataChannel.Close()
		return nil, err
	}
	if dataProducerID == "" {
		_ = dataChannel.Close()
		return nil, errors.New("OnProduceData returned empty data producer id")
	}

	localID := t.core.nextLocalIDString()
	dataProducer := newDataProducer(dataProducerConfig{
		id:                   dataProducerID,
		localID:              localID,
		dataChannel:          dataChannel,
		sctpStreamParameters: sctpStreamParameters,
		appData:              options.AppData,
		transport:            t.core,
	})

	t.core.mu.Lock()
	t.core.dataProducersByID[dataProducer.id] = dataProducer
	t.core.mu.Unlock()

	return dataProducer, nil
}

// ID returns transport id.
func (t *RecvTransport) ID() string { return t.core.ID() }

// Direction returns "recv".
func (t *RecvTransport) Direction() string { return t.core.Direction() }

// Closed reports whether transport is closed.
func (t *RecvTransport) Closed() bool { return t.core.Closed() }

// ConnectionState returns high-level connection state.
func (t *RecvTransport) ConnectionState() TransportConnectionState { return t.core.ConnectionState() }

// PeerConnection returns the underlying Pion PeerConnection.
func (t *RecvTransport) PeerConnection() *webrtc.PeerConnection { return t.core.PeerConnection() }

// LocalDTLSParameters returns local DTLS parameters for the connect callback.
func (t *RecvTransport) LocalDTLSParameters() DtlsParameters { return t.core.LocalDTLSParameters() }

// AddRemoteICECandidate adds an ICE candidate to the underlying PeerConnection.
func (t *RecvTransport) AddRemoteICECandidate(candidate IceCandidate) error {
	return t.core.addRemoteICECandidate(candidate)
}

// RemoteICEParameters returns server-provided ICE parameters.
func (t *RecvTransport) RemoteICEParameters() IceParameters { return t.core.RemoteICEParameters() }

// RemoteDTLSParameters returns server-provided DTLS parameters.
func (t *RecvTransport) RemoteDTLSParameters() DtlsParameters { return t.core.RemoteDTLSParameters() }

// SCTPParameters returns SCTP parameters if enabled.
func (t *RecvTransport) SCTPParameters() *SctpParameters { return t.core.SCTPParameters() }

// AppData returns app data.
func (t *RecvTransport) AppData() AppData { return t.core.AppData() }

// SetAppData replaces app data.
func (t *RecvTransport) SetAppData(appData AppData) { t.core.SetAppData(appData) }

// PrimeProbator pre-negotiates the hidden RTP probator receiver.
//
// mediasoup-client creates the probator after the first video Consumer. In
// server-side Pion flows, proactively creating it avoids transient "unhandled
// RTP ssrc(1234)" warnings when the router emits probation packets early.
func (t *RecvTransport) PrimeProbator(ctx context.Context) error {
	t.core.mu.Lock()
	if t.core.closed {
		t.core.mu.Unlock()
		return errors.New("closed")
	}
	recvCaps := cloneRtpCapabilities(t.core.recvRtpCapabilities)
	t.core.mu.Unlock()

	probatorRtpParameters, err := GenerateProbatorRtpParametersFromCapabilities(recvCaps)
	if err != nil {
		return err
	}

	return t.ensureProbatorConsumerWithParameters(ctx, probatorRtpParameters)
}

// Close closes the transport and associated consumers.
func (t *RecvTransport) Close() error { return t.core.close() }

// Consume mirrors mediasoup-client transport.consume() flow.
func (t *RecvTransport) Consume(ctx context.Context, options ConsumeOptions) (*Consumer, error) {
	if t.core.direction != transportDirectionRecv {
		return nil, errors.New("not a receiving transport")
	}
	if t.core.Closed() {
		return nil, errors.New("closed")
	}
	if options.ID == "" {
		return nil, errors.New("missing id")
	}
	if options.ProducerID == "" {
		return nil, errors.New("missing producerId")
	}
	if options.Kind != MediaKindAudio && options.Kind != MediaKindVideo {
		return nil, fmt.Errorf("invalid kind %q", options.Kind)
	}

	clonedRtpParameters := cloneRtpParameters(options.RtpParameters)
	if !CanReceive(clonedRtpParameters, t.core.recvRtpCapabilities) {
		return nil, errors.New("cannot consume this producer")
	}

	// Best effort: pre-negotiate the RTP probator before the first video
	// consumer to reduce the window where mediasoup probation packets (ssrc=1234)
	// can arrive before a matching receiver exists in Pion.
	if options.Kind == MediaKindVideo {
		_ = t.ensureProbatorConsumer(ctx, clonedRtpParameters)
	}

	localID := t.core.nextLocalIDString()
	mid := clonedRtpParameters.MID
	if mid == "" {
		mid = "mid-" + localID
	}
	clonedRtpParameters.MID = mid

	section := recvOfferMediaSection{
		MID:           mid,
		Kind:          options.Kind,
		RtpParameters: clonedRtpParameters,
		StreamID:      resolveConsumeStreamID(options.StreamID, clonedRtpParameters),
		TrackID:       options.ID,
	}

	if err := t.core.negotiateRecv(ctx, &section, false); err != nil {
		return nil, err
	}

	transceiver := findTransceiverByMid(t.core.pc, mid)
	if transceiver == nil {
		return nil, fmt.Errorf("transceiver not found for mid %q", mid)
	}
	receiver := transceiver.Receiver()
	if options.OnRTPReceiver != nil && receiver != nil {
		options.OnRTPReceiver(receiver)
	}

	consumer := newConsumer(consumerConfig{
		id:            options.ID,
		localID:       localID,
		producerID:    options.ProducerID,
		kind:          options.Kind,
		rtpReceiver:   receiver,
		transceiver:   transceiver,
		rtpParameters: clonedRtpParameters,
		appData:       options.AppData,
		transport:     t.core,
	})

	t.core.mu.Lock()
	t.core.consumersByID[consumer.id] = consumer
	t.core.consumersByLocalID[consumer.localID] = consumer
	if receiver != nil {
		t.core.consumersByReceiver[receiver] = consumer
	}
	t.core.mu.Unlock()

	return consumer, nil
}

func (t *RecvTransport) ensureProbatorConsumer(ctx context.Context, videoRtpParameters RtpParameters) error {
	probatorRtpParameters, err := GenerateProbatorRtpParameters(videoRtpParameters)
	if err != nil {
		return err
	}

	return t.ensureProbatorConsumerWithParameters(ctx, probatorRtpParameters)
}

func (t *RecvTransport) ensureProbatorConsumerWithParameters(
	ctx context.Context,
	probatorRtpParameters RtpParameters,
) error {
	t.core.mu.Lock()
	if t.core.probatorConsumerCreated || t.core.closed {
		t.core.mu.Unlock()
		return nil
	}
	t.core.probatorConsumerCreated = true
	t.core.mu.Unlock()

	section := recvOfferMediaSection{
		MID:           probatorRtpParameters.MID,
		Kind:          MediaKindVideo,
		RtpParameters: probatorRtpParameters,
		StreamID:      "probator",
		TrackID:       "probator",
	}

	if err := t.core.negotiateRecv(ctx, &section, false); err != nil {
		t.core.mu.Lock()
		t.core.probatorConsumerCreated = false
		t.core.mu.Unlock()
		return err
	}

	return nil
}

// ConsumeData mirrors mediasoup-client transport.consumeData() flow.
func (t *RecvTransport) ConsumeData(ctx context.Context, options ConsumeDataOptions) (*DataConsumer, error) {
	if t.core.direction != transportDirectionRecv {
		return nil, errors.New("not a receiving transport")
	}
	if t.core.Closed() {
		return nil, errors.New("closed")
	}
	if t.core.sctpParameters == nil {
		return nil, errors.New("SCTP not enabled by remote transport")
	}
	if options.ID == "" {
		return nil, errors.New("missing id")
	}
	if options.DataProducerID == "" {
		return nil, errors.New("missing dataProducerId")
	}

	sctpStreamParameters := cloneSctpStreamParameters(options.SctpStreamParameters)
	if err := ValidateAndNormalizeSctpStreamParameters(&sctpStreamParameters); err != nil {
		return nil, err
	}

	label := options.Label
	if label == "" {
		label = sctpStreamParameters.Label
	}
	protocol := options.Protocol
	if protocol == "" {
		protocol = sctpStreamParameters.Protocol
	}

	negotiated := true
	channelInit := &webrtc.DataChannelInit{
		Ordered:           sctpStreamParameters.Ordered,
		MaxPacketLifeTime: sctpStreamParameters.MaxPacketLifeTime,
		MaxRetransmits:    sctpStreamParameters.MaxRetransmits,
		Negotiated:        &negotiated,
		ID:                sctpStreamParameters.StreamID,
	}
	if protocol != "" {
		channelInit.Protocol = &protocol
	}

	dataChannel, err := t.core.pc.CreateDataChannel(label, channelInit)
	if err != nil {
		return nil, fmt.Errorf("create data channel: %w", err)
	}

	t.core.mu.Lock()
	needsDataNegotiation := !t.core.recvHasDataMedia
	t.core.mu.Unlock()

	if needsDataNegotiation {
		if err := t.core.negotiateRecv(ctx, nil, true); err != nil {
			_ = dataChannel.Close()
			return nil, err
		}
	} else if err := t.core.ensureConnected(ctx, nil); err != nil {
		_ = dataChannel.Close()
		return nil, err
	}

	localID := t.core.nextLocalIDString()
	dataConsumer := newDataConsumer(dataConsumerConfig{
		id:                   options.ID,
		localID:              localID,
		dataProducerID:       options.DataProducerID,
		dataChannel:          dataChannel,
		sctpStreamParameters: sctpStreamParameters,
		appData:              options.AppData,
		transport:            t.core,
	})

	t.core.mu.Lock()
	t.core.dataConsumersByID[dataConsumer.id] = dataConsumer
	t.core.dataConsumersByLocal[dataConsumer.localID] = dataConsumer
	t.core.mu.Unlock()

	return dataConsumer, nil
}

func findTransceiverBySender(pc *webrtc.PeerConnection, sender *webrtc.RTPSender) *webrtc.RTPTransceiver {
	if pc == nil || sender == nil {
		return nil
	}
	for _, transceiver := range pc.GetTransceivers() {
		if transceiver != nil && transceiver.Sender() == sender {
			return transceiver
		}
	}
	return nil
}

func findTransceiverByMid(pc *webrtc.PeerConnection, mid string) *webrtc.RTPTransceiver {
	if pc == nil || mid == "" {
		return nil
	}
	for _, transceiver := range pc.GetTransceivers() {
		if transceiver != nil && transceiver.Mid() == mid {
			return transceiver
		}
	}
	return nil
}

func resolveConsumeStreamID(explicit string, rtpParameters RtpParameters) string {
	if explicit != "" {
		return explicit
	}

	if msid := strings.TrimSpace(rtpParameters.MSID); msid != "" {
		parts := strings.Fields(msid)
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}

	if cname := strings.TrimSpace(rtpParameters.RTCP.CNAME); cname != "" {
		return cname
	}

	return "-"
}

func cloneRecvOfferMediaSection(in recvOfferMediaSection) recvOfferMediaSection {
	return recvOfferMediaSection{
		MID:           in.MID,
		Kind:          in.Kind,
		RtpParameters: cloneRtpParameters(in.RtpParameters),
		StreamID:      in.StreamID,
		TrackID:       in.TrackID,
	}
}

func startSenderRTCPDrain(sender *webrtc.RTPSender) {
	if sender == nil {
		return
	}

	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buffer); err != nil {
				return
			}
		}
	}()
}

func syncRtpEncodingsFromSender(sender *webrtc.RTPSender, rtpParameters *RtpParameters) {
	if sender == nil || rtpParameters == nil {
		return
	}

	sendParameters := sender.GetParameters()
	if len(sendParameters.Encodings) == 0 {
		return
	}

	synced := make([]RtpEncodingParameters, 0, len(sendParameters.Encodings))
	for index, senderEncoding := range sendParameters.Encodings {
		encoding := RtpEncodingParameters{}
		if index < len(rtpParameters.Encodings) {
			encoding = cloneRtpEncodingParameters(rtpParameters.Encodings[index])
		}
		if encoding.Active == nil {
			active := true
			encoding.Active = &active
		}
		if senderEncoding.RID != "" {
			encoding.RID = senderEncoding.RID
		}
		if senderEncoding.SSRC != 0 {
			ssrc := uint32(senderEncoding.SSRC)
			encoding.SSRC = &ssrc
		}
		if senderEncoding.RTX.SSRC != 0 {
			if encoding.RTX == nil {
				encoding.RTX = &RtxInfo{}
			}
			encoding.RTX.SSRC = uint32(senderEncoding.RTX.SSRC)
		} else if encoding.RTX != nil && encoding.RTX.SSRC == 0 {
			encoding.RTX = nil
		}

		synced = append(synced, encoding)
	}

	rtpParameters.Encodings = synced
}

func extractDtlsParametersFromSDP(rawSDP string) (DtlsParameters, error) {
	if strings.TrimSpace(rawSDP) == "" {
		return DtlsParameters{}, errors.New("empty SDP")
	}

	var sdpObject pionsdp.SessionDescription
	if err := sdpObject.UnmarshalString(rawSDP); err != nil {
		return DtlsParameters{}, fmt.Errorf("parse SDP: %w", err)
	}

	setup, fingerprintValue := findDtlsAttributeValues(&sdpObject)
	if setup == "" {
		return DtlsParameters{}, errors.New("no DTLS setup attribute found in SDP")
	}
	if fingerprintValue == "" {
		return DtlsParameters{}, errors.New("no DTLS fingerprint attribute found in SDP")
	}

	role, err := mapSetupToDtlsRole(setup)
	if err != nil {
		return DtlsParameters{}, err
	}

	fingerprint, err := parseFingerprintValue(fingerprintValue)
	if err != nil {
		return DtlsParameters{}, err
	}

	return DtlsParameters{
		Role:         role,
		Fingerprints: []DtlsFingerprint{fingerprint},
	}, nil
}

func findDtlsAttributeValues(sdpObject *pionsdp.SessionDescription) (setup string, fingerprint string) {
	if sdpObject == nil {
		return "", ""
	}

	setup = attributeValueByKey(sdpObject.Attributes, "setup")
	fingerprint = attributeValueByKey(sdpObject.Attributes, "fingerprint")

	for _, media := range sdpObject.MediaDescriptions {
		if media == nil {
			continue
		}
		if setup == "" {
			setup = attributeValueByKey(media.Attributes, "setup")
		}
		if fingerprint == "" {
			fingerprint = attributeValueByKey(media.Attributes, "fingerprint")
		}
		if setup != "" && fingerprint != "" {
			break
		}
	}

	return setup, fingerprint
}

func attributeValueByKey(attributes []pionsdp.Attribute, key string) string {
	for _, attribute := range attributes {
		if strings.EqualFold(attribute.Key, key) {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}

func parseFingerprintValue(value string) (DtlsFingerprint, error) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) < 2 {
		return DtlsFingerprint{}, fmt.Errorf("invalid fingerprint attribute %q", value)
	}

	return DtlsFingerprint{
		Algorithm: fields[0],
		Value:     strings.Join(fields[1:], " "),
	}, nil
}

func mapSetupToDtlsRole(setup string) (DtlsRole, error) {
	switch strings.ToLower(strings.TrimSpace(setup)) {
	case "active":
		return DtlsRoleClient, nil
	case "passive":
		return DtlsRoleServer, nil
	case "actpass":
		return DtlsRoleAuto, nil
	default:
		return "", fmt.Errorf("unsupported DTLS setup value %q", setup)
	}
}

func generateLocalDtlsParameters() (DtlsParameters, webrtc.Certificate, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return DtlsParameters{}, webrtc.Certificate{}, fmt.Errorf("generate ECDSA key: %w", err)
	}

	certificate, err := webrtc.GenerateCertificate(privateKey)
	if err != nil {
		return DtlsParameters{}, webrtc.Certificate{}, fmt.Errorf("generate certificate: %w", err)
	}

	fingerprints, err := certificate.GetFingerprints()
	if err != nil {
		return DtlsParameters{}, webrtc.Certificate{}, fmt.Errorf("get certificate fingerprints: %w", err)
	}

	resultFingerprints := make([]DtlsFingerprint, 0, len(fingerprints))
	for _, fp := range fingerprints {
		resultFingerprints = append(resultFingerprints, DtlsFingerprint{
			Algorithm: fp.Algorithm,
			Value:     fp.Value,
		})
	}

	return DtlsParameters{
		Role:         DtlsRoleAuto,
		Fingerprints: resultFingerprints,
	}, *certificate, nil
}

func normalizeProduceEncodings(encodings []RtpEncodingParameters, useRTX bool) []RtpEncodingParameters {
	if len(encodings) == 0 {
		encodings = []RtpEncodingParameters{{}}
	}

	result := make([]RtpEncodingParameters, 0, len(encodings))
	for _, encoding := range encodings {
		cloned := cloneRtpEncodingParameters(encoding)
		if cloned.Active == nil {
			v := true
			cloned.Active = &v
		}

		// Do not invent SSRC values. mediasoup must map to the SSRC that Pion
		// actually sends on the wire; fabricated values can break producer
		// routing/consumption (notably browser playback).
		if !useRTX {
			cloned.RTX = nil
		}
		result = append(result, cloned)
	}
	return result
}

func randomSSRC() uint32 {
	// Keep 0 out of range to align with practical RTP SSRC usage.
	for {
		candidate := mathrand.Uint32()
		if candidate != 0 {
			return candidate
		}
	}
}

func randomID(prefix string) string {
	return fmt.Sprintf("%s-%08x", prefix, uint32(math.Round(float64(mathrand.Uint32()))))
}

func toICECandidateSDP(candidate IceCandidate) string {
	address := candidate.Address
	if address == "" {
		address = candidate.IP
	}
	if candidate.Foundation == "" || address == "" || candidate.Protocol == "" || candidate.Type == "" || candidate.Port == 0 {
		return ""
	}

	builder := strings.Builder{}
	builder.WriteString("candidate:")
	builder.WriteString(candidate.Foundation)
	builder.WriteString(" 1 ")
	builder.WriteString(strings.ToLower(candidate.Protocol))
	builder.WriteString(" ")
	builder.WriteString(strconv.FormatUint(uint64(candidate.Priority), 10))
	builder.WriteString(" ")
	builder.WriteString(address)
	builder.WriteString(" ")
	builder.WriteString(strconv.FormatUint(uint64(candidate.Port), 10))
	builder.WriteString(" typ ")
	builder.WriteString(candidate.Type)
	if candidate.TCPType != "" {
		builder.WriteString(" tcptype ")
		builder.WriteString(candidate.TCPType)
	}

	return builder.String()
}

func mapPionConnectionState(state webrtc.PeerConnectionState) TransportConnectionState {
	switch state {
	case webrtc.PeerConnectionStateNew:
		return TransportConnectionStateNew
	case webrtc.PeerConnectionStateConnecting:
		return TransportConnectionStateConnecting
	case webrtc.PeerConnectionStateConnected:
		return TransportConnectionStateConnected
	case webrtc.PeerConnectionStateDisconnected:
		return TransportConnectionStateDisconnected
	case webrtc.PeerConnectionStateFailed:
		return TransportConnectionStateFailed
	case webrtc.PeerConnectionStateClosed:
		return TransportConnectionStateClosed
	default:
		return TransportConnectionStateNew
	}
}

func cloneIceServers(in []webrtc.ICEServer) []webrtc.ICEServer {
	if len(in) == 0 {
		return nil
	}
	out := make([]webrtc.ICEServer, len(in))
	copy(out, in)
	return out
}

func cloneDtlsParameters(in DtlsParameters) DtlsParameters {
	out := DtlsParameters{
		Role:         in.Role,
		Fingerprints: make([]DtlsFingerprint, len(in.Fingerprints)),
	}
	copy(out.Fingerprints, in.Fingerprints)
	return out
}

func cloneCanProduceByKind(in map[MediaKind]bool) map[MediaKind]bool {
	out := make(map[MediaKind]bool, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAppData(in AppData) AppData {
	if len(in) == 0 {
		return AppData{}
	}
	out := make(AppData, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
