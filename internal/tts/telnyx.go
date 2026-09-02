package tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// telnyxTTSURL is the streaming speech synthesis WebSocket endpoint.
	telnyxTTSURL = "wss://api.telnyx.com/v2/text-to-speech/speech"
	// defaultTelnyxVoice is a verified en-US female catalog voice. Voice
	// availability varies by account, so this is a starting point rather
	// than a guarantee: the catalog lives at
	// GET /v2/text-to-speech/voices.
	defaultTelnyxVoice = "Telnyx.Bayan.Amanda"
)

// TelnyxClient implements the Client interface against Telnyx's streaming
// text-to-speech WebSocket API.
//
// The protocol has no per-utterance completion marker: the server emits its
// isFinal frame only after the client sends an empty-text teardown, and on a
// persistent connection there is no way to tell one utterance's audio from
// the next. The adapter therefore opens one connection per utterance: dial,
// init, text, teardown, collect audio until isFinal, server closes. Measured
// against the persistent alternative this costs nothing on the live path:
// synthesis outpaces playback ~2.4x, first audio lands well under a second
// after dial, and every completion heuristic is avoided.
type TelnyxClient struct {
	apiKey     string
	voice      string
	voiceSpeed float64
}

// NewTelnyxClient creates a Telnyx TTS client. Empty voice defaults to
// Telnyx.Bayan.Amanda; zero voiceSpeed defaults to 1.0.
func NewTelnyxClient(apiKey, voice string, voiceSpeed float64) Client {
	if voice == "" {
		voice = defaultTelnyxVoice
	}
	if voiceSpeed == 0 {
		voiceSpeed = 1.0
	}
	return &TelnyxClient{
		apiKey:     apiKey,
		voice:      voice,
		voiceSpeed: voiceSpeed,
	}
}

// telnyxInitFrame is the required opener: its single-space text carries no
// speech, and it is where the per-utterance voice settings ride.
type telnyxInitFrame struct {
	Text          string              `json:"text"`
	VoiceSettings telnyxVoiceSettings `json:"voice_settings"`
}

type telnyxVoiceSettings struct {
	VoiceSpeed float64 `json:"voice_speed"`
}

// telnyxTextFrame carries synthesis text. The server buffers plain text;
// sentence boundaries alone do not start synthesis, so the adapter always
// sets flush and the whole utterance is synthesized immediately.
type telnyxTextFrame struct {
	Text  string `json:"text"`
	Flush bool   `json:"flush"`
}

// telnyxTeardownFrame tells the server no more text is coming. It flushes
// remaining synthesis, makes the server emit its isFinal frame, and closes
// the connection: it is the only completion marker the protocol has.
type telnyxTeardownFrame struct {
	Text string `json:"text"`
}

// telnyxForceJSON is pre-marshaled so the barge-in interrupt path never
// depends on marshaling succeeding at cancel time.
var telnyxForceJSON = []byte(`{"force":true}`)

// telnyxServerFrame is one message from the Telnyx speech WebSocket. The
// protocol has no type discriminator; frames are routed by which keys are
// populated.
type telnyxServerFrame struct {
	// Audio is a base64 linear16 PCM chunk. Null on cache-status
	// notifications and on the final frame.
	Audio *string `json:"audio"`
	// IsFinal marks the synthesis-complete frame, emitted only after the
	// empty-text teardown.
	IsFinal bool `json:"isFinal"`
	// Error carries a server-side failure.
	Error string `json:"error"`
}

// routeTelnyxFrame classifies one server frame: pcm is non-nil for audio
// frames, final reports the teardown-complete frame, and err is set for
// error frames and undecodable messages. Frames with no audio and isFinal
// false (cache-status notifications) come back all zero; the caller
// skips them silently.
func routeTelnyxFrame(msg []byte) (pcm []byte, final bool, err error) {
	var f telnyxServerFrame
	if jerr := json.Unmarshal(msg, &f); jerr != nil {
		return nil, false, fmt.Errorf("telnyx frame decode: %w", jerr)
	}
	if f.Error != "" {
		return nil, false, fmt.Errorf("telnyx: %s", f.Error)
	}
	if f.IsFinal {
		return nil, true, nil
	}
	if f.Audio != nil && *f.Audio != "" {
		pcm, derr := base64.StdEncoding.DecodeString(*f.Audio)
		if derr != nil {
			return nil, false, fmt.Errorf("telnyx audio decode: %w", derr)
		}
		return pcm, false, nil
	}
	return nil, false, nil
}

// Synthesize produces the full audio for text in one shot by collecting all
// chunks from SynthesizeStream.
func (c *TelnyxClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	stream, err := c.SynthesizeStream(ctx, text)
	if err != nil {
		return nil, err
	}

	var buf []byte
	for chunk := range stream {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		buf = append(buf, chunk.PCM...)
	}

	log.Printf("[tts:telnyx] synthesized %d bytes for %d chars of text", len(buf), len(text))
	return buf, nil
}

// SynthesizeStream produces PCM audio incrementally via a channel. One
// utterance maps to one WebSocket connection: dial, init with the resolved
// voice speed, the full text with flush so synthesis starts immediately,
// then the empty-text teardown that makes the server emit isFinal and
// close.
func (c *TelnyxClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	return c.SynthesizeStreamWithControls(ctx, text, VoiceControls{})
}

// SynthesizeStreamWithControls is SynthesizeStream with per-utterance voice
// controls. The speed rides the init frame of each per-utterance connection,
// so it can change from one utterance to the next.
func (c *TelnyxClient) SynthesizeStreamWithControls(ctx context.Context, text string, vc VoiceControls) (<-chan StreamChunk, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}

	init := telnyxInitFrame{
		Text:          " ",
		VoiceSettings: telnyxVoiceSettings{VoiceSpeed: c.resolveSpeed(vc)},
	}
	if err := writeTelnyxJSON(conn, init); err != nil {
		conn.Close()
		return nil, err
	}
	if err := writeTelnyxJSON(conn, telnyxTextFrame{Text: text, Flush: true}); err != nil {
		conn.Close()
		return nil, err
	}
	if err := writeTelnyxJSON(conn, telnyxTeardownFrame{}); err != nil {
		conn.Close()
		return nil, err
	}

	outCh := make(chan StreamChunk, 16)
	go c.pump(ctx, conn, outCh, len(text))
	return outCh, nil
}

// dial opens the per-utterance WebSocket. Audio format and sample rate are
// pinned to the pipeline's native linear16 16 kHz mono, so the audio path
// needs no resampling.
func (c *TelnyxClient) dial(ctx context.Context) (*websocket.Conn, error) {
	endpoint := fmt.Sprintf("%s?voice=%s&audio_format=linear16&sample_rate=16000",
		telnyxTTSURL, url.QueryEscape(c.voice))
	header := http.Header{}
	header.Set("Authorization", "Bearer "+c.apiKey)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusForbidden {
			// A 403 at the handshake is how Telnyx reports an unknown
			// voice or one the account is not provisioned for. Point
			// the operator at the voice catalog rather than leaving a
			// bare handshake failure.
			return nil, fmt.Errorf("telnyx dial: voice %q is not available for this account (HTTP 403); list valid voices with GET /v2/text-to-speech/voices: %w", c.voice, err)
		}
		return nil, fmt.Errorf("telnyx dial: %w", err)
	}
	return conn, nil
}

// resolveSpeed picks the voice_speed for one utterance: the per-utterance
// control when set, otherwise the configured baseline. Both are clamped to
// the same conversational band as Cartesia so a bad delivery tag can never
// produce comedy audio on a live call.
func (c *TelnyxClient) resolveSpeed(vc VoiceControls) float64 {
	speed := vc.Speed
	if speed == 0 {
		speed = c.voiceSpeed
	}
	if speed < 0.8 {
		speed = 0.8
	}
	if speed > 1.2 {
		speed = 1.2
	}
	return speed
}

// pump reads frames until the final frame or an error, forwarding audio
// chunks to outCh; it closes outCh and the connection on exit. The done
// channel stops the cancellation watcher once the pump has returned, so the
// watcher never outlives the stream.
func (c *TelnyxClient) pump(ctx context.Context, conn *websocket.Conn, outCh chan<- StreamChunk, textLen int) {
	defer close(outCh)
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)

	// Cancellation must unblock a stalled read: closing the connection
	// makes ReadMessage return, so a barge-in cannot leak a goroutine
	// parked on a silent server. The force frame first is best-effort
	// server-side cleanup, bounded by a short write deadline.
	go func() {
		select {
		case <-ctx.Done():
			c.sendForce(conn)
			conn.Close()
		case <-done:
		}
	}()

	totalBytes := 0
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				if !isNormalClose(err) {
					log.Printf("[tts:telnyx] read error: %v", err)
				}
				// A connection drop before the final frame is an
				// error, not a silently short utterance.
				select {
				case outCh <- StreamChunk{Err: fmt.Errorf("telnyx: connection lost: %w", err)}:
				case <-ctx.Done():
				}
			}
			return
		}

		pcm, final, ferr := routeTelnyxFrame(msg)
		if ferr != nil {
			select {
			case outCh <- StreamChunk{Err: ferr}:
			case <-ctx.Done():
			}
			return
		}
		if final {
			// Log total bytes and expected duration for diagnosing
			// speed/sample-rate issues. At 16 kHz mono 16-bit:
			// 32000 bytes/sec → 32 bytes/ms.
			log.Printf("[tts:telnyx] streamed %d bytes (%.0f ms expected) for %d chars of text",
				totalBytes, float64(totalBytes)/32.0, textLen)
			return
		}
		if len(pcm) > 0 {
			totalBytes += len(pcm)
			select {
			case outCh <- StreamChunk{PCM: pcm}:
			case <-ctx.Done():
				return
			}
		}
		// No audio and not final: a cache-status notification. Skip.
	}
}

// sendForce interrupts in-flight synthesis on the server. Best-effort with a
// short write deadline: it runs on the barge-in path, where the caller is
// already gone and a stalled server must not hold the goroutine.
func (c *TelnyxClient) sendForce(conn *websocket.Conn) {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, telnyxForceJSON); err != nil {
		log.Printf("[tts:telnyx] force send failed: %v", err)
	}
}

// writeTelnyxJSON marshals v and writes it as a text message.
func writeTelnyxJSON(conn *websocket.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("telnyx marshal: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return fmt.Errorf("telnyx write: %w", err)
	}
	return nil
}
