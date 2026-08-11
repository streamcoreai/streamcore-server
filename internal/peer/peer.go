package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
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

	// restartMu serialises ICE restarts so two concurrent PATCHes can't
	// interleave SetRemoteDescription / CreateAnswer on the same connection.
	restartMu sync.Mutex

	closed bool
	// disconnected tracks whether the transport is currently in the transient
	// disconnected state, so a return to connected can be reported as a
	// recovery rather than as a fresh connect.
	disconnected bool
	mu           sync.Mutex
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
	// Register both Opus variants so the server accepts:
	//   - Browser WHIP clients, which advertise `opus/48000/2` per WebRTC convention.
	//   - `streamcoreai` SDK clients (Go/Rust/Python/TS), which advertise `opus/48000/1`.
	// Pion's codec matcher is strict on Channels, so we register both.
	// PayloadType 111 is used by every browser; 109 is a commonly-free slot
	// we reserve for the mono variant.
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    2,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus/2 codec: %w", err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeOpus,
			ClockRate:   48000,
			Channels:    1,
			SDPFmtpLine: "minptime=10;useinbandfec=1",
		},
		PayloadType: 109,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register opus/1 codec: %w", err)
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
		// (SetNAT1To1IPs is deprecated in favour of address rewrite rules; the
		// default Mode for a srflx rule is Append, matching the old behavior.)
		if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{publicIP},
			AsCandidateType: webrtc.ICECandidateTypeSrflx,
		}); err != nil {
			return nil, fmt.Errorf("set ICE address rewrite rules: %w", err)
		}
		// Only use the primary network interface and loopback. Skip Docker
		// bridge interfaces (docker0, br-*) to avoid leaking 172.17.x/172.18.x.
		// Supports both Linux (eth0/ens5/lo) and macOS (en0/lo0).
		se.SetInterfaceFilter(func(iface string) bool {
			switch iface {
			case "eth0", "ens5", "en0", "lo", "lo0":
				return true
			default:
				return false
			}
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

	// NOTE: The outbound audio track is created inside HandleOffer once we
	// have inspected the client's offer and know which Opus channel layout
	// (`/1` for the SDK clients, `/2` for browsers) it's using. Building it
	// here with a single Channels value would break whichever client type we
	// didn't pick.

	peerCtx, peerCancel := context.WithCancel(ctx)

	p := &Peer{
		ID:            id,
		pc:            pc,
		ctx:           peerCtx,
		cancel:        peerCancel,
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

		switch state {
		case webrtc.PeerConnectionStateDisconnected:
			// Transient — connectivity checks are failing right now, but the
			// connection heals on its own for the events voice agents actually
			// see: a phone moving between Wi-Fi and cellular, a laptop changing
			// network, a NAT rebinding after an idle gap. Pion enters this
			// state after ~5s of silence and only escalates to Failed at ~25s.
			// Tearing down here throws away a call that was about to recover.
			p.markDisconnected()
			go p.SendEvent(connectionMsg{Type: "connection", State: "reconnecting"})
		case webrtc.PeerConnectionStateConnected:
			// Only announce a recovery — the first connect is not news.
			if p.clearDisconnected() {
				log.Printf("[peer:%s] recovered from disconnected", id)
				go p.SendEvent(connectionMsg{Type: "connection", State: "connected"})
			}
		default:
			if isTerminalState(state) {
				go p.Close() // must not call pc.Close() from within a Pion callback
			}
		}
	})

	return p, nil
}

// connectionMsg is the DataChannel event that tells a client the transport
// dropped and is being re-established, so it can surface "reconnecting…"
// instead of a silent gap.
type connectionMsg struct {
	Type  string `json:"type"`
	State string `json:"state"`
}

// isTerminalState reports whether a connection state means the peer is gone
// for good. Disconnected is deliberately absent: it is recoverable, and
// closing on it is what turns a 10-second network blip into a lost
// conversation.
func isTerminalState(state webrtc.PeerConnectionState) bool {
	return state == webrtc.PeerConnectionStateFailed ||
		state == webrtc.PeerConnectionStateClosed
}

func (p *Peer) markDisconnected() {
	p.mu.Lock()
	p.disconnected = true
	p.mu.Unlock()
}

// clearDisconnected reports whether the peer was in the disconnected state and
// resets the flag, so a recovery is announced exactly once.
func (p *Peer) clearDisconnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	was := p.disconnected
	p.disconnected = false
	return was
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
	log.Printf("[peer:%s] offer m-lines: %s", p.ID, summarizeMLines(sdp))

	// Create the outbound audio track now that we can see the offer.
	// The track's Channels MUST match the remote's offered Opus variant;
	// otherwise Pion's codec matcher returns "RTPSender created with no codecs".
	// Browsers always offer `opus/48000/2`; the `streamcoreai` SDK clients
	// offer `opus/48000/1`.
	channels := detectOpusChannels(sdp)
	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  channels,
		},
		"audio-agent",
		"streamcoreai",
	)
	if err != nil {
		return "", fmt.Errorf("create local track: %w", err)
	}
	if _, err := p.pc.AddTrack(track); err != nil {
		return "", fmt.Errorf("add track: %w", err)
	}
	p.localTrack = track
	log.Printf("[peer:%s] local track created: opus/48000/%d", p.ID, channels)

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}
	if err := p.pc.SetRemoteDescription(offer); err != nil {
		return "", fmt.Errorf("set remote description: %w", err)
	}

	// Log transceiver directions as Pion resolved them from the offer.
	for i, t := range p.pc.GetTransceivers() {
		log.Printf("[peer:%s] transceiver[%d] kind=%s mid=%s direction=%s",
			p.ID, i, t.Kind().String(), t.Mid(), t.Direction().String())
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

	answerSDP := p.pc.LocalDescription().SDP
	log.Printf("[peer:%s] answer m-lines: %s", p.ID, summarizeMLines(answerSDP))
	return answerSDP, nil
}

// opusRtpmapRe matches `a=rtpmap:<pt> opus/48000/<channels>` lines in SDP.
var opusRtpmapRe = regexp.MustCompile(`(?i)a=rtpmap:\d+\s+opus/48000/(\d+)`)

// detectOpusChannels inspects the offer SDP and returns the Opus channel count
// the remote advertised (1 or 2). Defaults to 2 (browser convention) if no
// Opus rtpmap is found. When both are offered, `/2` wins — browsers always
// offer `/2`, so picking it keeps the outbound track compatible.
func detectOpusChannels(sdp string) uint16 {
	matches := opusRtpmapRe.FindAllStringSubmatch(sdp, -1)
	if len(matches) == 0 {
		return 2
	}
	sawOne := false
	for _, m := range matches {
		switch m[1] {
		case "2":
			return 2
		case "1":
			sawOne = true
		}
	}
	if sawOne {
		return 1
	}
	return 2
}

// summarizeMLines returns a compact one-line summary of the SDP's m-lines
// and their directions (sendrecv/sendonly/recvonly/inactive).
func summarizeMLines(sdp string) string {
	var out []string
	var currentM string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "m=") {
			if currentM != "" {
				out = append(out, currentM)
			}
			currentM = line
		} else if currentM != "" &&
			(strings.HasPrefix(line, "a=sendrecv") ||
				strings.HasPrefix(line, "a=sendonly") ||
				strings.HasPrefix(line, "a=recvonly") ||
				strings.HasPrefix(line, "a=inactive")) {
			currentM += " [" + strings.TrimPrefix(line, "a=") + "]"
		}
	}
	if currentM != "" {
		out = append(out, currentM)
	}
	return strings.Join(out, " | ")
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
