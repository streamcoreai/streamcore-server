package mediasoupclient

import (
	"errors"
	"sync"

	"github.com/pion/webrtc/v4"
)

type consumerConfig struct {
	id            string
	localID       string
	producerID    string
	kind          MediaKind
	rtpReceiver   *webrtc.RTPReceiver
	transceiver   *webrtc.RTPTransceiver
	rtpParameters RtpParameters
	appData       AppData
	transport     *transportCore
}

// Consumer mirrors mediasoup-client Consumer.
type Consumer struct {
	mu sync.Mutex

	id         string
	localID    string
	producerID string
	kind       MediaKind

	closed bool
	paused bool

	rtpReceiver *webrtc.RTPReceiver
	transceiver *webrtc.RTPTransceiver
	track       *webrtc.TrackRemote

	rtpParameters RtpParameters
	appData       AppData

	transport *transportCore
}

func newConsumer(config consumerConfig) *Consumer {
	return &Consumer{
		id:            config.id,
		localID:       config.localID,
		producerID:    config.producerID,
		kind:          config.kind,
		rtpReceiver:   config.rtpReceiver,
		transceiver:   config.transceiver,
		rtpParameters: cloneRtpParameters(config.rtpParameters),
		appData:       cloneAppData(config.appData),
		transport:     config.transport,
	}
}

// ID returns consumer id.
func (c *Consumer) ID() string {
	return c.id
}

// LocalID returns transport-local consumer id.
func (c *Consumer) LocalID() string {
	return c.localID
}

// ProducerID returns associated producer id.
func (c *Consumer) ProducerID() string {
	return c.producerID
}

// Kind returns consumer kind.
func (c *Consumer) Kind() MediaKind {
	return c.kind
}

// Closed reports whether Consumer is closed.
func (c *Consumer) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Paused reports whether Consumer is paused.
func (c *Consumer) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// RTPReceiver returns the underlying Pion RTPReceiver.
func (c *Consumer) RTPReceiver() *webrtc.RTPReceiver {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rtpReceiver
}

// Track returns the remote track once available.
func (c *Consumer) Track() *webrtc.TrackRemote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.track
}

// RTPParameters returns consumer RTP parameters.
func (c *Consumer) RTPParameters() RtpParameters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneRtpParameters(c.rtpParameters)
}

// AppData returns app data.
func (c *Consumer) AppData() AppData {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneAppData(c.appData)
}

// SetAppData replaces app data.
func (c *Consumer) SetAppData(appData AppData) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appData = cloneAppData(appData)
}

// Pause mirrors mediasoup-client Consumer.pause().
func (c *Consumer) Pause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	if c.paused {
		return nil
	}
	c.paused = true
	return nil
}

// Resume mirrors mediasoup-client Consumer.resume().
func (c *Consumer) Resume() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	if !c.paused {
		return nil
	}
	c.paused = false
	return nil
}

// Close mirrors mediasoup-client Consumer.close().
func (c *Consumer) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	transport := c.transport
	localID := c.localID
	c.mu.Unlock()

	if transport != nil {
		return transport.removeConsumer(localID)
	}

	return nil
}

func (c *Consumer) setTrack(track *webrtc.TrackRemote) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.track = track
}

func (c *Consumer) transportClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}
