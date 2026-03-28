package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// DataChannel label that the client must create before making the offer.
const EventChannelLabel = "events"

// Peer wraps a single WebRTC PeerConnection. It handles ICE/SDP negotiation,
// track setup, and DataChannel events. The audio pipeline (read/write/codec)
// is owned by the pipeline package — the Peer just exposes the raw tracks.
type Peer struct {
	ID     string
	pc     *webrtc.PeerConnection
	ctx    context.Context
	cancel context.CancelFunc

	localTrack    *webrtc.TrackLocalStaticRTP
	RemoteTrackCh chan *webrtc.TrackRemote

	// DataChannel used to send transcript / response / error events to the client.
	dcMu sync.Mutex
	dc   *webrtc.DataChannel

	OnClose func()

	closed bool
	mu     sync.Mutex
}

func New(ctx context.Context, id string) (*Peer, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    1,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus codec: %w", err)
	}

	i := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		return nil, fmt.Errorf("register interceptors: %w", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))

	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
	}

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  1,
		},
		"audio-agent",
		"streamcoreai",
	)
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("create local track: %w", err)
	}

	if _, err := pc.AddTrack(track); err != nil {
		pc.Close()
		return nil, fmt.Errorf("add track: %w", err)
	}

	peerCtx, peerCancel := context.WithCancel(ctx)

	p := &Peer{
		ID:            id,
		pc:            pc,
		ctx:           peerCtx,
		cancel:        peerCancel,
		localTrack:    track,
		RemoteTrackCh: make(chan *webrtc.TrackRemote, 1),
	}

	pc.OnTrack(func(remoteTrack *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		log.Printf("[peer:%s] got remote track: %s", id, remoteTrack.Codec().MimeType)
		select {
		case p.RemoteTrackCh <- remoteTrack:
		default:
		}
	})

	// Accept the DataChannel created by the client for event messages.
	pc.OnDataChannel(func(d *webrtc.DataChannel) {
		if d.Label() != EventChannelLabel {
			return
		}
		d.OnOpen(func() {
			log.Printf("[peer:%s] data channel '%s' open", id, d.Label())
			p.dcMu.Lock()
			p.dc = d
			p.dcMu.Unlock()
		})
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("[peer:%s] connection state: %s", id, state.String())
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateClosed {
			go p.Close() // must not call pc.Close() from within a Pion callback
		}
	})

	return p, nil
}

// LocalTrack returns the outbound audio track for the pipeline to write to.
func (p *Peer) LocalTrack() *webrtc.TrackLocalStaticRTP {
	return p.localTrack
}

// Context returns the peer's context, which is cancelled when the peer closes.
func (p *Peer) Context() context.Context {
	return p.ctx
}

// SendEvent JSON-encodes msg and sends it on the DataChannel.
// Returns nil silently if the channel is not yet open.
func (p *Peer) SendEvent(msg interface{}) error {
	p.dcMu.Lock()
	dc := p.dc
	p.dcMu.Unlock()

	if dc == nil {
		return nil
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	return dc.SendText(string(data))
}

func (p *Peer) HandleOffer(sdp string) (string, error) {
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}
	if err := p.pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("set remote description: %w", err)
	}

	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("create answer: %w", err)
	}

	if err := p.pc.SetLocalDescription(answer); err != nil {
		return "", fmt.Errorf("set local description: %w", err)
	}

	gatherDone := webrtc.GatheringCompletePromise(p.pc)
	select {
	case <-gatherDone:
	case <-time.After(5 * time.Second):
		log.Printf("[peer:%s] ICE gathering timed out, using partial candidates", p.ID)
	}

	return p.pc.LocalDescription().SDP, nil
}

func (p *Peer) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	p.cancel()

	// Close with a timeout so a stuck DTLS/ICE teardown doesn't block forever.
	done := make(chan struct{})
	go func() {
		p.pc.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		log.Printf("[peer:%s] pc.Close timed out", p.ID)
	}

	if p.OnClose != nil {
		p.OnClose()
	}
	log.Printf("[peer:%s] closed", p.ID)
}
