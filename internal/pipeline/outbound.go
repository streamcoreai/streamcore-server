package pipeline

import (
	"log"
	"time"

	"github.com/pion/rtp"
	"github.com/streamcoreai/server/internal/audio"
)

// runSender reads PCM frames from outPCMCh, encodes them to Opus, and writes
// RTP packets to the local WebRTC track with wall-clock pacing (20ms/frame).
func (p *Pipeline) runSender() {
	for {
		// Wait for the first frame of a talkspurt
		var frame PCMFrame
		select {
		case <-p.ctx.Done():
			return
		case frame = <-p.outPCMCh:
		}

		p.streamTalkspurt(frame)
	}
}

// streamTalkspurt sends a continuous run of PCM frames with wall-clock pacing.
// It returns when the channel is empty for >100ms (gap between utterances)
// or the context is cancelled.
func (p *Pipeline) streamTalkspurt(first PCMFrame) {
	if first.NewTalkspurt {
		p.markTalkspurt()
	}
	p.encodeAndSend(first)

	start := time.Now()
	idx := 1

	for {
		target := start.Add(time.Duration(idx) * 20 * time.Millisecond)
		wait := time.Until(target)

		// Use a short timer to detect gaps between TTS utterances.
		// If no frame arrives within 100ms, the talkspurt is over.
		timer := time.NewTimer(100 * time.Millisecond)

		select {
		case <-p.ctx.Done():
			timer.Stop()
			return
		case frame := <-p.outPCMCh:
			timer.Stop()
			if frame.NewTalkspurt {
				p.markTalkspurt()
				start = time.Now()
				idx = 0
			}
			if wait > 0 {
				time.Sleep(wait)
			}
			p.encodeAndSend(frame)
			idx++
		case <-timer.C:
			// No frames for 100ms — talkspurt ended
			return
		}
	}
}

// encodeAndSend encodes a single PCM frame to Opus and writes it as an RTP packet.
func (p *Pipeline) encodeAndSend(frame PCMFrame) {
	samples := frame.Samples
	if len(samples) < audio.FrameSize {
		padded := make([]int16, audio.FrameSize)
		copy(padded, samples)
		samples = padded
	}

	opusData, err := p.encoder.Encode(samples)
	if err != nil {
		log.Printf("[sender] encode error: %v", err)
		return
	}

	// Forward raw Opus payload to bridge if hooked.
	if p.onOutboundOpus != nil {
		p.onOutboundOpus(opusData)
	}

	// When no local track (mediasoup mode), only the callback above is used.
	if p.localTrack == nil {
		return
	}

	p.rtpMu.Lock()
	p.seqNum++
	p.timestamp += audio.RTPTimestampIncr
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: p.seqNum,
			Timestamp:      p.timestamp,
			SSRC:           p.ssrc,
			Marker:         p.markerNext,
		},
		Payload: opusData,
	}
	p.markerNext = false
	p.rtpMu.Unlock()

	raw, err := pkt.Marshal()
	if err != nil {
		log.Printf("[sender] marshal error: %v", err)
		return
	}

	if _, err := p.localTrack.Write(raw); err != nil {
		if p.ctx.Err() == nil {
			log.Printf("[sender] write error: %v", err)
		}
	}
}

// markTalkspurt sets the RTP marker bit for the next packet.
func (p *Pipeline) markTalkspurt() {
	p.rtpMu.Lock()
	defer p.rtpMu.Unlock()
	p.markerNext = true
}
