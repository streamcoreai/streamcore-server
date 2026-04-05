package msbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	mediasoupclient "github.com/jason/go-mediasoup-client/go-mediasoup-client"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/pipeline"
	"github.com/streamcoreai/server/internal/plugin"
)

// Bridge is a standalone mediasoup peer that joins a room, produces agent
// audio (TTS), and consumes all other peers' audio tracks. It owns its
// own pipeline (STT → LLM → TTS) and does not require a WHIP connection.
type Bridge struct {
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *config.Config
	msCfg  *config.MediasoupConfig

	protoo *ProtooClient
	device *mediasoupclient.Device

	// Agent audio (TTS output → mediasoup room)
	sendTransport *mediasoupclient.SendTransport
	agentProducer *mediasoupclient.Producer
	agentTrack    *webrtc.TrackLocalStaticSample

	// Consuming other peers' audio
	recvTransport *mediasoupclient.RecvTransport
	consumersMu   sync.Mutex
	consumers     map[string]*mediasoupclient.Consumer

	// Pipeline
	pipeline  *pipeline.Pipeline
	pluginMgr *plugin.Manager
	decoder   *audio.OpusDecoder

	closed atomic.Bool
}

// --- protoo response types matching the mediasoup server ---

type createTransportResponse struct {
	TransportID    string                          `json:"transportId"`
	IceParameters  mediasoupclient.IceParameters   `json:"iceParameters"`
	IceCandidates  []mediasoupclient.IceCandidate  `json:"iceCandidates"`
	DtlsParameters mediasoupclient.DtlsParameters  `json:"dtlsParameters"`
	SctpParameters *mediasoupclient.SctpParameters `json:"sctpParameters,omitempty"`
}

type produceResponse struct {
	ProducerID string `json:"producerId"`
}

// newConsumerRequest is the data sent by the mediasoup server in a
// "newConsumer" protoo request.
type newConsumerRequest struct {
	PeerID         string                        `json:"peerId"`
	TransportID    string                        `json:"transportId"`
	ConsumerID     string                        `json:"consumerId"`
	ProducerID     string                        `json:"producerId"`
	Kind           string                        `json:"kind"`
	RtpParameters  mediasoupclient.RtpParameters `json:"rtpParameters"`
	Type           string                        `json:"type"`
	ProducerPaused bool                          `json:"producerPaused"`
	AppData        map[string]interface{}        `json:"appData"`
}

// NewBridge creates and starts a mediasoup agent in the given room.
// It connects via protoo WebSocket, joins, produces agent audio, sets up
// a RecvTransport for consuming all peers, and starts the pipeline.
// Call Close() to tear everything down.
func NewBridge(ctx context.Context, cfg *config.Config, pluginMgr *plugin.Manager, roomID string) (*Bridge, error) {
	msCfg := &cfg.Mediasoup
	if roomID != "" {
		msCfg = &config.MediasoupConfig{
			SignalingURL: cfg.Mediasoup.SignalingURL,
			RoomID:       roomID,
			OriginHeader: cfg.Mediasoup.OriginHeader,
		}
	}

	bCtx, cancel := context.WithCancel(ctx)
	peerID := fmt.Sprintf("voiceagent-%d", time.Now().UnixMilli())

	b := &Bridge{
		ctx:       bCtx,
		cancel:    cancel,
		cfg:       cfg,
		msCfg:     msCfg,
		pluginMgr: pluginMgr,
		consumers: make(map[string]*mediasoupclient.Consumer),
	}

	if err := b.setup(peerID); err != nil {
		cancel()
		b.Close()
		return nil, fmt.Errorf("msbridge setup: %w", err)
	}

	log.Printf("[msbridge] agent joined room=%s peer=%s", msCfg.RoomID, peerID)
	return b, nil
}

func (b *Bridge) setup(peerID string) error {
	// 1. Connect protoo WebSocket
	var err error
	b.protoo, err = NewProtooClient(b.msCfg.SignalingURL, b.msCfg.RoomID, peerID, b.msCfg.OriginHeader)
	if err != nil {
		return fmt.Errorf("protoo connect: %w", err)
	}

	b.protoo.OnNotification = func(method string, data json.RawMessage) {
		log.Printf("[msbridge] notification: %s", method)
	}

	b.protoo.OnRequest = b.handleServerRequest

	// 2. Get router RTP capabilities
	capsRaw, err := b.protoo.Request("getRouterRtpCapabilities", nil)
	if err != nil {
		return fmt.Errorf("getRouterRtpCapabilities: %w", err)
	}

	var capsResp struct {
		RouterRtpCapabilities json.RawMessage `json:"routerRtpCapabilities"`
	}
	if err := json.Unmarshal(capsRaw, &capsResp); err != nil {
		return fmt.Errorf("parse router caps wrapper: %w", err)
	}

	var routerCaps mediasoupclient.RtpCapabilities
	if err := json.Unmarshal(capsResp.RouterRtpCapabilities, &routerCaps); err != nil {
		return fmt.Errorf("parse rtp capabilities: %w", err)
	}

	// 3. Create and load Device
	b.device, err = mediasoupclient.NewDevice(mediasoupclient.DeviceOptions{})
	if err != nil {
		return fmt.Errorf("create device: %w", err)
	}
	if err := b.device.Load(routerCaps, false); err != nil {
		return fmt.Errorf("device load: %w", err)
	}

	// 4. Join the room
	rtpCaps, err := b.device.RecvRtpCapabilities()
	if err != nil {
		return fmt.Errorf("get recv rtp caps: %w", err)
	}
	sctpCaps, err := b.device.SctpCapabilities()
	if err != nil {
		return fmt.Errorf("get sctp caps: %w", err)
	}

	joinData := map[string]interface{}{
		"displayName":      "Voice Agent",
		"device":           map[string]string{"name": "streamcore-voiceagent"},
		"rtpCapabilities":  rtpCaps,
		"sctpCapabilities": sctpCaps,
	}
	// 5. Create SendTransport for agent audio (before join)
	b.sendTransport, b.agentTrack, err = b.createSendTransport("agent-tts")
	if err != nil {
		return fmt.Errorf("send transport: %w", err)
	}

	// 6. Create RecvTransport for consuming peers' audio (before join so
	//    the server sees the consumer transport when assigning consumers
	//    for existing producers after join)
	b.recvTransport, err = b.createRecvTransport()
	if err != nil {
		return fmt.Errorf("recv transport: %w", err)
	}

	// 7. Create decoder for incoming consumer audio
	b.decoder, err = audio.NewOpusDecoder()
	if err != nil {
		return fmt.Errorf("opus decoder: %w", err)
	}

	// 8. Create the pipeline (no Pion tracks — mediasoup mode)
	noopSendEvent := func(interface{}) error { return nil }
	pipelineOpts := &pipeline.PipelineOptions{
		OnOutboundOpus: b.sendAgentOpus,
	}
	b.pipeline, err = pipeline.New(
		b.ctx, b.cfg,
		nil, // no remoteTrack — consumers push PCM via PushPCM
		nil, // no localTrack — onOutboundOpus writes to mediasoup
		noopSendEvent,
		b.pluginMgr,
		"",
		pipelineOpts,
	)
	if err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}

	// Start pipeline in background
	go b.pipeline.Start()

	// 9. Join the room — server will send newConsumer for existing producers
	joinResp, err := b.protoo.Request("join", joinData)
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	log.Printf("[msbridge] joined room")

	// 10. Produce agent audio track (after join)
	b.agentProducer, err = b.sendTransport.Produce(b.ctx, mediasoupclient.ProduceOptions{
		Track:   b.agentTrack,
		AppData: mediasoupclient.AppData{"source": "agent-tts"},
	})
	if err != nil {
		return fmt.Errorf("produce agent audio: %w", err)
	}
	log.Printf("[msbridge] agent producer=%s", b.agentProducer.ID())

	// Log existing peers
	b.consumeExistingPeers(joinResp)

	return nil
}

// handleServerRequest dispatches protoo server requests (e.g. newConsumer).
func (b *Bridge) handleServerRequest(method string, data json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "newConsumer":
		return nil, b.onNewConsumer(data)
	default:
		log.Printf("[msbridge] unhandled server request: %s", method)
		return nil, nil // accept silently
	}
}

// onNewConsumer handles a "newConsumer" request from the mediasoup server.
// It creates a consumer on the RecvTransport and starts reading audio from it.
func (b *Bridge) onNewConsumer(data json.RawMessage) error {
	log.Printf("[msbridge] newConsumer request received: %s", string(data)[:min(len(data), 200)])

	var req newConsumerRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("parse newConsumer: %w", err)
	}

	log.Printf("[msbridge] newConsumer: peer=%s kind=%s producer=%s consumer=%s", req.PeerID, req.Kind, req.ProducerID, req.ConsumerID)

	// Only consume audio
	if req.Kind != "audio" {
		log.Printf("[msbridge] skipping non-audio consumer kind=%s producer=%s", req.Kind, req.ProducerID)
		return nil
	}

	if b.recvTransport == nil {
		log.Printf("[msbridge] ERROR: recvTransport is nil, cannot consume")
		return fmt.Errorf("recvTransport not ready")
	}

	consumer, err := b.recvTransport.Consume(b.ctx, mediasoupclient.ConsumeOptions{
		ID:            req.ConsumerID,
		ProducerID:    req.ProducerID,
		Kind:          mediasoupclient.MediaKindAudio,
		RtpParameters: req.RtpParameters,
		AppData:       mediasoupclient.AppData{"peerId": req.PeerID},
	})
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}

	b.consumersMu.Lock()
	b.consumers[req.ConsumerID] = consumer
	b.consumersMu.Unlock()

	log.Printf("[msbridge] consuming audio from peer=%s consumer=%s producer=%s", req.PeerID, req.ConsumerID, req.ProducerID)

	// Start reading audio from this consumer in a goroutine
	go b.readConsumerAudio(consumer, req.PeerID)

	return nil
}

// readConsumerAudio reads RTP from a consumer's track, decodes Opus to PCM,
// and pushes frames into the pipeline.
func (b *Bridge) readConsumerAudio(consumer *mediasoupclient.Consumer, peerID string) {
	// Wait for the track to become available
	var track *webrtc.TrackRemote
	for i := 0; i < 100; i++ { // up to 5s
		track = consumer.Track()
		if track != nil {
			break
		}
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	if track == nil {
		log.Printf("[msbridge] consumer %s: track never arrived for peer=%s", consumer.ID(), peerID)
		return
	}

	log.Printf("[msbridge] reading audio from peer=%s track=%s", peerID, track.ID())

	dec, err := audio.NewOpusDecoder()
	if err != nil {
		log.Printf("[msbridge] consumer %s: decoder error: %v", consumer.ID(), err)
		return
	}

	buf := make([]byte, 1500)
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		n, _, err := track.Read(buf)
		if err != nil {
			if b.ctx.Err() == nil {
				log.Printf("[msbridge] consumer %s read error: %v", consumer.ID(), err)
			}
			return
		}

		pkt := &rtp.Packet{}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}

		pcm, err := dec.Decode(pkt.Payload)
		if err != nil {
			continue
		}

		b.pipeline.PushPCM(pcm)
	}
}

// consumeExistingPeers processes the join response to consume producers
// from peers already in the room.
func (b *Bridge) consumeExistingPeers(joinResp json.RawMessage) {
	if joinResp == nil {
		return
	}

	// The join response contains { peers: [...] } with existing peers info.
	// The server will automatically send newConsumer requests for existing
	// producers, so we don't need to manually request them here.
	var resp struct {
		Peers []struct {
			ID string `json:"id"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(joinResp, &resp); err == nil && len(resp.Peers) > 0 {
		log.Printf("[msbridge] room has %d existing peers", len(resp.Peers))
	}
}

// createSendTransport creates a mediasoup WebRtcTransport for sending,
// returning the transport and a local Opus audio track.
func (b *Bridge) createSendTransport(source string) (*mediasoupclient.SendTransport, *webrtc.TrackLocalStaticSample, error) {
	reqData := map[string]interface{}{
		"forceTcp": false,
		"appData":  map[string]string{"direction": "producer"},
	}
	respRaw, err := b.protoo.Request("createWebRtcTransport", reqData)
	if err != nil {
		return nil, nil, fmt.Errorf("createWebRtcTransport: %w", err)
	}

	var resp createTransportResponse
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return nil, nil, fmt.Errorf("parse transport response: %w", err)
	}

	protoo := b.protoo

	transport, err := b.device.CreateSendTransport(mediasoupclient.SendTransportOptions{
		BaseTransportOptions: mediasoupclient.BaseTransportOptions{
			ID:             resp.TransportID,
			ICEParameters:  resp.IceParameters,
			ICECandidates:  resp.IceCandidates,
			DTLSParameters: resp.DtlsParameters,
			SCTPParameters: resp.SctpParameters,
			OnConnect: func(ctx context.Context, req mediasoupclient.ConnectRequest) error {
				connectData := map[string]interface{}{
					"transportId":    resp.TransportID,
					"dtlsParameters": req.DtlsParameters,
				}
				_, err := protoo.Request("connectWebRtcTransport", connectData)
				return err
			},
		},
		OnProduce: func(ctx context.Context, req mediasoupclient.ProduceRequest) (string, error) {
			produceData := map[string]interface{}{
				"transportId":   resp.TransportID,
				"kind":          req.Kind,
				"rtpParameters": req.RtpParameters,
				"appData":       map[string]string{"source": source},
			}
			prodRaw, err := protoo.Request("produce", produceData)
			if err != nil {
				return "", err
			}
			var prodResp produceResponse
			if err := json.Unmarshal(prodRaw, &prodResp); err != nil {
				return "", err
			}
			return prodResp.ProducerID, nil
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create send transport: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		fmt.Sprintf("%s-audio", source),
		fmt.Sprintf("%s-stream", source),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create audio track: %w", err)
	}

	return transport, track, nil
}

// createRecvTransport creates a mediasoup WebRtcTransport for receiving.
func (b *Bridge) createRecvTransport() (*mediasoupclient.RecvTransport, error) {
	reqData := map[string]interface{}{
		"forceTcp": false,
		"appData":  map[string]string{"direction": "consumer"},
	}
	respRaw, err := b.protoo.Request("createWebRtcTransport", reqData)
	if err != nil {
		return nil, fmt.Errorf("createWebRtcTransport: %w", err)
	}

	var resp createTransportResponse
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return nil, fmt.Errorf("parse transport response: %w", err)
	}

	protoo := b.protoo

	transport, err := b.device.CreateRecvTransport(mediasoupclient.RecvTransportOptions{
		BaseTransportOptions: mediasoupclient.BaseTransportOptions{
			ID:             resp.TransportID,
			ICEParameters:  resp.IceParameters,
			ICECandidates:  resp.IceCandidates,
			DTLSParameters: resp.DtlsParameters,
			SCTPParameters: resp.SctpParameters,
			OnConnect: func(ctx context.Context, req mediasoupclient.ConnectRequest) error {
				connectData := map[string]interface{}{
					"transportId":    resp.TransportID,
					"dtlsParameters": req.DtlsParameters,
				}
				_, err := protoo.Request("connectWebRtcTransport", connectData)
				return err
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create recv transport: %w", err)
	}

	return transport, nil
}

// sendAgentOpus forwards an encoded Opus packet (TTS output) to mediasoup.
func (b *Bridge) sendAgentOpus(opusPayload []byte) {
	if b.closed.Load() || b.agentTrack == nil {
		return
	}
	if err := b.agentTrack.WriteSample(media.Sample{
		Data:     opusPayload,
		Duration: 20 * time.Millisecond,
	}); err != nil {
		if b.ctx.Err() == nil {
			log.Printf("[msbridge] write agent opus: %v", err)
		}
	}
}

// Close tears down the bridge: stops pipeline, closes consumers,
// transports, and protoo connection.
func (b *Bridge) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.cancel()

	if b.pipeline != nil {
		b.pipeline.Stop()
	}

	b.consumersMu.Lock()
	for _, c := range b.consumers {
		c.Close()
	}
	b.consumers = make(map[string]*mediasoupclient.Consumer)
	b.consumersMu.Unlock()

	if b.agentProducer != nil {
		b.agentProducer.Close()
	}
	if b.sendTransport != nil {
		b.sendTransport.Close()
	}
	if b.recvTransport != nil {
		b.recvTransport.Close()
	}
	if b.protoo != nil {
		b.protoo.Close()
	}
	log.Printf("[msbridge] closed")
}
