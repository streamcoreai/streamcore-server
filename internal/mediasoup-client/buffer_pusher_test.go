package mediasoupclient

import (
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestNewEncodedMediaPusher(t *testing.T) {
	t.Parallel()

	if _, err := NewEncodedMediaPusher(nil, nil); err == nil {
		t.Fatalf("expected error with nil tracks")
	}

	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio-track",
		"stream-1",
	)
	if err != nil {
		t.Fatalf("create audio track: %v", err)
	}

	pusher, err := NewEncodedMediaPusher(audioTrack, nil)
	if err != nil {
		t.Fatalf("create pusher: %v", err)
	}
	if pusher == nil {
		t.Fatalf("expected non-nil pusher")
	}
}

func TestEncodedMediaPusherPushValidation(t *testing.T) {
	t.Parallel()

	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		"audio-track",
		"stream-1",
	)
	if err != nil {
		t.Fatalf("create audio track: %v", err)
	}
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000},
		"video-track",
		"stream-1",
	)
	if err != nil {
		t.Fatalf("create video track: %v", err)
	}

	pusher, err := NewEncodedMediaPusher(audioTrack, videoTrack)
	if err != nil {
		t.Fatalf("create pusher: %v", err)
	}

	if err := pusher.PushAudioOpus(nil, 20*time.Millisecond); err == nil {
		t.Fatalf("expected error for empty audio payload")
	}
	if err := pusher.PushVideoVP8([]byte{0x00}, 0); err == nil {
		t.Fatalf("expected error for zero video duration")
	}

	if err := pusher.PushAudioOpus([]byte{0xf8, 0xff, 0xfe}, 20*time.Millisecond); err != nil {
		t.Fatalf("push audio payload: %v", err)
	}
	if err := pusher.PushVideoVP8([]byte{0x90, 0x00, 0x00, 0x00}, 33*time.Millisecond); err != nil {
		t.Fatalf("push video frame: %v", err)
	}
}
