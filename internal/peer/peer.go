package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
)

// DataChannel label that the client must create before making the offer.
const EventChannelLabel = "events"

// TURNConfig holds the configuration for the built-in STUN/TURN server.
// When PublicIP and Secret are both set, peers include the built-in TURN
// server in their ICE server list.
type TURNConfig struct {
	PublicIP string
	Secret   string
}

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

	// OnDataChannelMessage is called for each incoming text message on the
	// "events" DataChannel. Set this before the channel opens (i.e. before
	// Start) so no messages are missed.
	OnDataChannelMessage func(msg string)

	closed bool
	mu     sync.Mutex
}

// Global UDPMux shared by all peers — created once on first use.
var (
	globalMux     ice.UDPMux
	globalMuxOnce sync.Once
	globalMuxErr  error
	globalMuxAddr net.Addr
)

func getOrCreateMux() (ice.UDPMux, net.Addr, error) {
	globalMuxOnce.Do(func() {
		udpListener, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 50000})
		if err != nil {
			globalMuxErr = fmt.Errorf("listen UDP for ICE mux: %w", err)
			return
		}
		globalMux = ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: udpListener})
		globalMuxAddr = udpListener.LocalAddr()
	})
	return globalMux, globalMuxAddr, globalMuxErr
}

func New(ctx context.Context, id string, publicIP string, turnCfg TURNConfig) (*Peer, error) {
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

	se := webrtc.SettingEngine{}
	if publicIP != "" {
		// Use a shared UDPMux so all peers multiplex over a single UDP socket.
		mux, addr, err := getOrCreateMux()
		if err != nil {
			return nil, err
		}
		se.SetICEUDPMux(mux)
		// Use Srflx so Pion keeps the private-IP host candidate AND adds
		// the public IP as srflx. The host candidate lets the TURN relay
		// (on the same machine) reach the server via the private IP,
		// bypassing EC2 Elastic IP hairpin issues.
		se.SetNAT1To1IPs([]string{publicIP}, webrtc.ICECandidateTypeSrflx)
		// Only use the primary network interface (eth0/ens5), skip Docker
		// bridge interfaces (docker0, br-*) to avoid leaking 172.17.x/172.18.x.
		se.SetInterfaceFilter(func(iface string) bool {
			return iface == "eth0" || iface == "ens5" || iface == "lo"
		})
		se.SetIPFilter(func(ip net.IP) bool {
			return ip.To4() != nil // IPv4 only
		})
		log.Printf("[peer:%s] UDPMux on %s, public IP: %s", id, addr, publicIP)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i), webrtc.WithSettingEngine(se))

	iceServers := []webrtc.ICEServer{}
	if turnCfg.PublicIP != "" && turnCfg.Secret != "" {
		// Use the built-in STUN/TURN server (replaces external coturn + Google STUN).
		iceServers = append(iceServers,
			webrtc.ICEServer{URLs: []string{"stun:" + turnCfg.PublicIP + ":3478"}},
			webrtc.ICEServer{
				URLs:       []string{"turn:" + turnCfg.PublicIP + ":3478"},
				Username:   "voiceagent",
				Credential: turnCfg.Secret,
			},
		)
	} else {
		// Local development — no TURN needed, use Google STUN as fallback.
		iceServers = append(iceServers,
			webrtc.ICEServer{URLs: []string{"stun:stun.l.google.com:19302"}},
		)
	}
	config := webrtc.Configuration{
		ICEServers: iceServers,
	}
	pc, err := api.NewPeerConnection(config)
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
		d.OnMessage(func(msg webrtc.DataChannelMessage) {
			if msg.IsString {
				if handler := p.OnDataChannelMessage; handler != nil {
					handler(string(msg.Data))
				}
			}
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
