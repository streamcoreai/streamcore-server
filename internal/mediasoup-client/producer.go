package mediasoupclient

import (
	"errors"
	"sync"

	"github.com/pion/webrtc/v4"
)

type producerConfig struct {
	id             string
	localID        string
	track          webrtc.TrackLocal
	rtpSender      *webrtc.RTPSender
	rtpParameters  RtpParameters
	appData        AppData
	zeroRTPOnPause bool
	transport      *transportCore
}

// Producer mirrors mediasoup-client Producer.
type Producer struct {
	mu sync.Mutex

	id      string
	localID string
	kind    MediaKind

	closed bool
	paused bool

	track     webrtc.TrackLocal
	rtpSender *webrtc.RTPSender

	rtpParameters RtpParameters
	appData       AppData

	zeroRTPOnPause bool
	transport      *transportCore
}

func newProducer(config producerConfig) *Producer {
	kind, _ := pionKindToMediaKind(config.track.Kind())

	return &Producer{
		id:             config.id,
		localID:        config.localID,
		kind:           kind,
		track:          config.track,
		rtpSender:      config.rtpSender,
		rtpParameters:  cloneRtpParameters(config.rtpParameters),
		appData:        cloneAppData(config.appData),
		zeroRTPOnPause: config.zeroRTPOnPause,
		transport:      config.transport,
	}
}

// ID returns producer id.
func (p *Producer) ID() string {
	return p.id
}

// LocalID returns transport-local producer id.
func (p *Producer) LocalID() string {
	return p.localID
}

// Kind returns producer kind.
func (p *Producer) Kind() MediaKind {
	return p.kind
}

// Closed reports whether Producer is closed.
func (p *Producer) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Paused reports whether Producer is paused.
func (p *Producer) Paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

// Track returns the currently configured local track.
func (p *Producer) Track() webrtc.TrackLocal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.track
}

// RTPSender returns the underlying Pion RTPSender.
func (p *Producer) RTPSender() *webrtc.RTPSender {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rtpSender
}

// RTPParameters returns producer RTP parameters.
func (p *Producer) RTPParameters() RtpParameters {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneRtpParameters(p.rtpParameters)
}

// AppData returns app data.
func (p *Producer) AppData() AppData {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneAppData(p.appData)
}

// SetAppData replaces app data.
func (p *Producer) SetAppData(appData AppData) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appData = cloneAppData(appData)
}

// Pause mirrors mediasoup-client Producer.pause().
func (p *Producer) Pause() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("closed")
	}
	if p.paused {
		p.mu.Unlock()
		return nil
	}

	p.paused = true
	sender := p.rtpSender
	zeroRTPOnPause := p.zeroRTPOnPause
	p.mu.Unlock()

	if zeroRTPOnPause && sender != nil {
		return sender.ReplaceTrack(nil)
	}

	return nil
}

// Resume mirrors mediasoup-client Producer.resume().
func (p *Producer) Resume() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("closed")
	}
	if !p.paused {
		p.mu.Unlock()
		return nil
	}

	p.paused = false
	sender := p.rtpSender
	track := p.track
	zeroRTPOnPause := p.zeroRTPOnPause
	p.mu.Unlock()

	if zeroRTPOnPause && sender != nil {
		return sender.ReplaceTrack(track)
	}

	return nil
}

// ReplaceTrack mirrors mediasoup-client Producer.replaceTrack().
func (p *Producer) ReplaceTrack(track webrtc.TrackLocal) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("closed")
	}

	sender := p.rtpSender
	paused := p.paused
	zeroRTPOnPause := p.zeroRTPOnPause
	if sender == nil {
		p.track = track
		p.mu.Unlock()
		return nil
	}

	effectiveTrack := track
	if paused && zeroRTPOnPause {
		effectiveTrack = nil
	}

	if err := sender.ReplaceTrack(effectiveTrack); err != nil {
		p.mu.Unlock()
		return err
	}

	p.track = track
	if track != nil {
		if kind, ok := pionKindToMediaKind(track.Kind()); ok {
			p.kind = kind
		}
	}
	p.mu.Unlock()

	return nil
}

// Close mirrors mediasoup-client Producer.close().
func (p *Producer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	transport := p.transport
	localID := p.localID
	p.mu.Unlock()

	if transport != nil {
		return transport.removeProducer(localID)
	}

	return nil
}

func (p *Producer) transportClosed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
}
