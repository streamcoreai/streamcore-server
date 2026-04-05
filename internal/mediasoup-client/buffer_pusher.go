package mediasoupclient

import (
	"errors"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// EncodedMediaPusher is a small helper for feeding encoded audio/video payloads
// into Pion local tracks that are already produced via SendTransport.
//
// Important: payloads must already match the advertised codecs:
// - audio track: Opus frames
// - video track: VP8 frames
type EncodedMediaPusher struct {
	audioTrack *webrtc.TrackLocalStaticSample
	videoTrack *webrtc.TrackLocalStaticSample
}

// NewEncodedMediaPusher creates a pusher bound to the given local tracks.
func NewEncodedMediaPusher(
	audioTrack *webrtc.TrackLocalStaticSample,
	videoTrack *webrtc.TrackLocalStaticSample,
) (*EncodedMediaPusher, error) {
	if audioTrack == nil && videoTrack == nil {
		return nil, errors.New("at least one track must be provided")
	}

	return &EncodedMediaPusher{
		audioTrack: audioTrack,
		videoTrack: videoTrack,
	}, nil
}

// PushAudioOpus writes one encoded Opus frame to the audio track.
func (p *EncodedMediaPusher) PushAudioOpus(payload []byte, duration time.Duration) error {
	return p.PushAudioSample(media.Sample{
		Data:     payload,
		Duration: duration,
	})
}

// PushVideoVP8 writes one encoded VP8 frame to the video track.
func (p *EncodedMediaPusher) PushVideoVP8(frame []byte, duration time.Duration) error {
	return p.PushVideoSample(media.Sample{
		Data:     frame,
		Duration: duration,
	})
}

// PushAudioSample writes an encoded sample to the audio track.
func (p *EncodedMediaPusher) PushAudioSample(sample media.Sample) error {
	if p == nil {
		return errors.New("nil media pusher")
	}
	if p.audioTrack == nil {
		return errors.New("audio track is not configured")
	}
	return writeEncodedSample(p.audioTrack, sample)
}

// PushVideoSample writes an encoded sample to the video track.
func (p *EncodedMediaPusher) PushVideoSample(sample media.Sample) error {
	if p == nil {
		return errors.New("nil media pusher")
	}
	if p.videoTrack == nil {
		return errors.New("video track is not configured")
	}
	return writeEncodedSample(p.videoTrack, sample)
}

func writeEncodedSample(track *webrtc.TrackLocalStaticSample, sample media.Sample) error {
	if track == nil {
		return errors.New("nil track")
	}
	if len(sample.Data) == 0 {
		return errors.New("empty sample data")
	}
	if sample.Duration <= 0 {
		return errors.New("sample duration must be > 0")
	}

	cloned := media.Sample{
		Data:     append([]byte(nil), sample.Data...),
		Duration: sample.Duration,
	}

	return track.WriteSample(cloned)
}
