package mediasoupclient

import (
	"errors"
	"sync"

	"github.com/pion/webrtc/v4"
)

type dataProducerConfig struct {
	id                   string
	localID              string
	dataChannel          *webrtc.DataChannel
	sctpStreamParameters SctpStreamParameters
	appData              AppData
	transport            *transportCore
}

// DataProducer mirrors mediasoup-client DataProducer.
type DataProducer struct {
	mu sync.Mutex

	id      string
	localID string
	closed  bool

	dataChannel          *webrtc.DataChannel
	sctpStreamParameters SctpStreamParameters
	appData              AppData

	transport *transportCore
}

func newDataProducer(config dataProducerConfig) *DataProducer {
	return &DataProducer{
		id:                   config.id,
		localID:              config.localID,
		dataChannel:          config.dataChannel,
		sctpStreamParameters: cloneSctpStreamParameters(config.sctpStreamParameters),
		appData:              cloneAppData(config.appData),
		transport:            config.transport,
	}
}

// ID returns DataProducer id.
func (p *DataProducer) ID() string {
	return p.id
}

// LocalID returns transport-local DataProducer id.
func (p *DataProducer) LocalID() string {
	return p.localID
}

// Closed reports whether DataProducer is closed.
func (p *DataProducer) Closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// SctpStreamParameters returns SCTP stream parameters.
func (p *DataProducer) SctpStreamParameters() SctpStreamParameters {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneSctpStreamParameters(p.sctpStreamParameters)
}

// ReadyState returns DataChannel ready state.
func (p *DataProducer) ReadyState() webrtc.DataChannelState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return webrtc.DataChannelStateClosed
	}
	return p.dataChannel.ReadyState()
}

// Label returns DataChannel label.
func (p *DataProducer) Label() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return ""
	}
	return p.dataChannel.Label()
}

// Protocol returns DataChannel protocol.
func (p *DataProducer) Protocol() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return ""
	}
	return p.dataChannel.Protocol()
}

// BufferedAmount returns DataChannel buffered amount.
func (p *DataProducer) BufferedAmount() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return 0
	}
	return p.dataChannel.BufferedAmount()
}

// BufferedAmountLowThreshold returns DataChannel low threshold.
func (p *DataProducer) BufferedAmountLowThreshold() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return 0
	}
	return p.dataChannel.BufferedAmountLowThreshold()
}

// SetBufferedAmountLowThreshold sets DataChannel low threshold.
func (p *DataProducer) SetBufferedAmountLowThreshold(threshold uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return
	}
	p.dataChannel.SetBufferedAmountLowThreshold(threshold)
}

// OnBufferedAmountLow registers a callback for buffered amount low events.
func (p *DataProducer) OnBufferedAmountLow(handler func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return
	}
	p.dataChannel.OnBufferedAmountLow(handler)
}

// OnOpen registers a callback for open events.
func (p *DataProducer) OnOpen(handler func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return
	}
	p.dataChannel.OnOpen(handler)
}

// OnClose registers a callback for close events.
func (p *DataProducer) OnClose(handler func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return
	}
	p.dataChannel.OnClose(handler)
}

// OnError registers a callback for error events.
func (p *DataProducer) OnError(handler func(error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dataChannel == nil {
		return
	}
	p.dataChannel.OnError(handler)
}

// DataChannel returns the underlying DataChannel.
func (p *DataProducer) DataChannel() *webrtc.DataChannel {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dataChannel
}

// AppData returns app data.
func (p *DataProducer) AppData() AppData {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneAppData(p.appData)
}

// SetAppData replaces app data.
func (p *DataProducer) SetAppData(appData AppData) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appData = cloneAppData(appData)
}

// Send sends binary data over the DataChannel.
func (p *DataProducer) Send(data []byte) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("closed")
	}
	channel := p.dataChannel
	p.mu.Unlock()

	if channel == nil {
		return errors.New("data channel not available")
	}

	return channel.Send(data)
}

// SendText sends text data over the DataChannel.
func (p *DataProducer) SendText(text string) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("closed")
	}
	channel := p.dataChannel
	p.mu.Unlock()

	if channel == nil {
		return errors.New("data channel not available")
	}

	return channel.SendText(text)
}

// Close mirrors mediasoup-client DataProducer.close().
func (p *DataProducer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	channel := p.dataChannel
	transport := p.transport
	id := p.id
	p.mu.Unlock()

	if channel != nil {
		_ = channel.Close()
	}
	if transport != nil {
		transport.removeDataProducer(id)
	}

	return nil
}

func (p *DataProducer) transportClosed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.dataChannel != nil {
		_ = p.dataChannel.Close()
	}
}
