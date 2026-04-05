package mediasoupclient

import (
	"sync"

	"github.com/pion/webrtc/v4"
)

type dataConsumerConfig struct {
	id                   string
	localID              string
	dataProducerID       string
	dataChannel          *webrtc.DataChannel
	sctpStreamParameters SctpStreamParameters
	appData              AppData
	transport            *transportCore
}

// DataConsumer mirrors mediasoup-client DataConsumer.
type DataConsumer struct {
	mu sync.Mutex

	id             string
	localID        string
	dataProducerID string
	closed         bool

	dataChannel          *webrtc.DataChannel
	sctpStreamParameters SctpStreamParameters
	appData              AppData

	transport *transportCore
}

func newDataConsumer(config dataConsumerConfig) *DataConsumer {
	return &DataConsumer{
		id:                   config.id,
		localID:              config.localID,
		dataProducerID:       config.dataProducerID,
		dataChannel:          config.dataChannel,
		sctpStreamParameters: cloneSctpStreamParameters(config.sctpStreamParameters),
		appData:              cloneAppData(config.appData),
		transport:            config.transport,
	}
}

// ID returns DataConsumer id.
func (c *DataConsumer) ID() string {
	return c.id
}

// LocalID returns transport-local DataConsumer id.
func (c *DataConsumer) LocalID() string {
	return c.localID
}

// DataProducerID returns the associated DataProducer id.
func (c *DataConsumer) DataProducerID() string {
	return c.dataProducerID
}

// Closed reports whether DataConsumer is closed.
func (c *DataConsumer) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// SctpStreamParameters returns SCTP stream parameters.
func (c *DataConsumer) SctpStreamParameters() SctpStreamParameters {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneSctpStreamParameters(c.sctpStreamParameters)
}

// ReadyState returns DataChannel ready state.
func (c *DataConsumer) ReadyState() webrtc.DataChannelState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return webrtc.DataChannelStateClosed
	}
	return c.dataChannel.ReadyState()
}

// Label returns DataChannel label.
func (c *DataConsumer) Label() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return ""
	}
	return c.dataChannel.Label()
}

// Protocol returns DataChannel protocol.
func (c *DataConsumer) Protocol() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return ""
	}
	return c.dataChannel.Protocol()
}

// DataChannel returns the underlying DataChannel.
func (c *DataConsumer) DataChannel() *webrtc.DataChannel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dataChannel
}

// AppData returns app data.
func (c *DataConsumer) AppData() AppData {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneAppData(c.appData)
}

// SetAppData replaces app data.
func (c *DataConsumer) SetAppData(appData AppData) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.appData = cloneAppData(appData)
}

// OnOpen registers a callback for open events.
func (c *DataConsumer) OnOpen(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return
	}
	c.dataChannel.OnOpen(handler)
}

// OnClose registers a callback for close events.
func (c *DataConsumer) OnClose(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return
	}
	c.dataChannel.OnClose(handler)
}

// OnError registers a callback for error events.
func (c *DataConsumer) OnError(handler func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return
	}
	c.dataChannel.OnError(handler)
}

// OnMessage registers a callback for incoming messages.
func (c *DataConsumer) OnMessage(handler func(webrtc.DataChannelMessage)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dataChannel == nil {
		return
	}
	c.dataChannel.OnMessage(handler)
}

// Close mirrors mediasoup-client DataConsumer.close().
func (c *DataConsumer) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	channel := c.dataChannel
	transport := c.transport
	localID := c.localID
	c.mu.Unlock()

	if channel != nil {
		_ = channel.Close()
	}
	if transport != nil {
		transport.removeDataConsumer(localID)
	}

	return nil
}

func (c *DataConsumer) transportClosed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.dataChannel != nil {
		_ = c.dataChannel.Close()
	}
}
