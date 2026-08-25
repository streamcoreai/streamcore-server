package tts

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Volcengine (Doubao) streaming TTS — /api/v3/tts/bidirection
// -----------------------------------------------------------
//
//	Endpoint:  wss://openspeech.bytedance.com/api/v3/tts/bidirection
//	Auth:      X-Api-Key: <api key>       (the same console key the ASR
//	           provider uses; the older app-id + access-token pair is
//	           rejected here too)
//	Resource:  X-Api-Resource-Id: seed-tts-2.0
//	Audio:     raw 16-bit signed little-endian PCM at 16kHz mono, so nothing
//	           is resampled on this path.
//
// The framing is the same binary family as the ASR provider in internal/stt,
// but not the same layout: these frames carry a 4-byte event number after the
// header, and session-scoped events carry a length-prefixed session id after
// that. Copying the ASR parser over lands the payload at the wrong offset,
// which fails as silence rather than as an error.
//
//	byte 0     protocol version (high nibble), header size in 4-byte words
//	byte 1     message type (high nibble), flags (low nibble)
//	byte 2     serialisation (high nibble), compression (low nibble)
//	byte 3     reserved
//	bytes 4-7  event number, big-endian          (present when flags has 0x04)
//	then       session id length + session id    (session-scoped events only)
//	then       payload length, big-endian
//	then       payload
//
// A synthesis is three nested scopes: StartConnection opens the connection,
// StartSession opens one utterance, TaskRequest submits the text, and
// FinishSession closes the utterance. That nesting is what makes connection
// reuse possible later — the connection outlives the utterance — though this
// first version opens one per utterance to match the other providers here.
const (
	volcengineTTSURL = "wss://openspeech.bytedance.com/api/v3/tts/bidirection"
	// volcengineTTSSampleRate is what the pipeline plays; the service honours
	// it, so no conversion is needed.
	volcengineTTSSampleRate = 16000

	// defaultVolcengineTTSResource is the 2.0 speech synthesis resource.
	// The 1.0 resource (volc.service_type.10029) is a separate entitlement and
	// answers 403 when it has not been activated.
	defaultVolcengineTTSResource = "seed-tts-2.0"
	// Voices belong to a model generation. The 2.0 voices carry the
	// `_uranus_bigtts` suffix; the widely-published `_moon_` / `_mars_` /
	// `_jupiter_` names are 1.0 and are refused under this resource with
	// "resource ID is mismatched with speaker related resource" — a message
	// that names neither the voice nor the entitlement.
	defaultVolcengineSpeaker = "zh_female_vv_uranus_bigtts"

	// Message type (high nibble of byte 1) and the flag that says an event
	// number follows the header.
	volcTTSTypeFullClient  = 0b0001
	volcTTSFlagWithEvent   = 0b0100
	volcTTSSerialisationJS = 0x10

	// Client events.
	volcEventStartConnection = 1
	volcEventStartSession    = 100
	volcEventFinishSession   = 102
	volcEventTaskRequest     = 200

	// Server events.
	volcEventConnectionStarted = 50
	volcEventSessionStarted    = 150
	volcEventSessionFinished   = 152
	volcEventSessionFailed     = 153
	volcEventTTSResponse       = 352
)

type volcengineClient struct {
	apiKey   string
	speaker  string
	resource string
	url      string
}

// NewVolcengineClient creates a Doubao TTS client. Empty speaker, resource and
// url fall back to the package defaults.
func NewVolcengineClient(apiKey, speaker, resource, url string) Client {
	if speaker == "" {
		speaker = defaultVolcengineSpeaker
	}
	if resource == "" {
		resource = defaultVolcengineTTSResource
	}
	if url == "" {
		url = volcengineTTSURL
	}
	return &volcengineClient{apiKey: apiKey, speaker: speaker, resource: resource, url: url}
}

// volcFrame assembles one client frame. sessionID is empty for
// connection-scoped events, which carry no session id at all.
func volcFrame(event int32, sessionID string, payload []byte) []byte {
	buf := []byte{
		0x11,
		volcTTSTypeFullClient<<4 | volcTTSFlagWithEvent,
		volcTTSSerialisationJS,
		0x00,
	}

	ev := make([]byte, 4)
	binary.BigEndian.PutUint32(ev, uint32(event))
	buf = append(buf, ev...)

	if sessionID != "" {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(sessionID)))
		buf = append(append(buf, l...), sessionID...)
	}

	l := make([]byte, 4)
	binary.BigEndian.PutUint32(l, uint32(len(payload)))
	return append(append(buf, l...), payload...)
}

// volcSessionScopedEvent reports whether a server event carries a session id
// between the event number and the payload length. Getting this wrong shifts
// every subsequent field and turns audio into garbage.
func volcSessionScopedEvent(event int32) bool {
	switch event {
	case volcEventSessionStarted, volcEventSessionFinished, volcEventSessionFailed,
		volcEventTTSResponse, 350, 351, 359:
		return true
	}
	return false
}

// parseVolcTTSFrame returns the event number and payload of a server frame.
func parseVolcTTSFrame(data []byte) (event int32, payload []byte, ok bool) {
	if len(data) < 8 {
		return 0, nil, false
	}

	p := 4
	if data[1]&0x0F&volcTTSFlagWithEvent != 0 {
		if p+4 > len(data) {
			return 0, nil, false
		}
		event = int32(binary.BigEndian.Uint32(data[p : p+4]))
		p += 4
	}

	if volcSessionScopedEvent(event) {
		if p+4 > len(data) {
			return event, nil, false
		}
		n := int(binary.BigEndian.Uint32(data[p : p+4]))
		p += 4
		if p+n > len(data) {
			return event, nil, false
		}
		p += n
	}

	if p+4 > len(data) {
		return event, nil, false
	}
	n := int(binary.BigEndian.Uint32(data[p : p+4]))
	p += 4
	if p+n > len(data) {
		n = len(data) - p
	}
	return event, data[p : p+n], true
}

// volcSession is one synthesis: a connection, its session id, and the write
// lock that keeps teardown from racing the send path.
type volcSession struct {
	conn      *websocket.Conn
	sessionID string
	writeMu   sync.Mutex
	closed    bool
}

func (s *volcSession) write(event int32, sessionID string, payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return fmt.Errorf("volcengine: connection closed")
	}
	return s.conn.WriteMessage(websocket.BinaryMessage, volcFrame(event, sessionID, payload))
}

func (s *volcSession) close() {
	s.writeMu.Lock()
	if s.closed {
		s.writeMu.Unlock()
		return
	}
	s.closed = true
	s.writeMu.Unlock()
	_ = s.conn.Close()
}

// volcAudioParams is the audio half of req_params.
type volcAudioParams struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
}

type volcReqParams struct {
	Text        string          `json:"text,omitempty"`
	Speaker     string          `json:"speaker"`
	AudioParams volcAudioParams `json:"audio_params"`
}

type volcRequest struct {
	User struct {
		UID string `json:"uid"`
	} `json:"user"`
	Event     int32         `json:"event"`
	Namespace string        `json:"namespace"`
	ReqParams volcReqParams `json:"req_params"`
}

func (c *volcengineClient) request(event int32, text string) ([]byte, error) {
	var r volcRequest
	r.User.UID = "streamcore"
	r.Event = event
	r.Namespace = "BidirectionalTTS"
	r.ReqParams = volcReqParams{
		Text:    text,
		Speaker: c.speaker,
		AudioParams: volcAudioParams{
			Format:     "pcm",
			SampleRate: volcengineTTSSampleRate,
		},
	}
	return json.Marshal(r)
}

// open dials and walks the connection and session handshakes, returning only
// once the service has accepted both. Sending a TaskRequest before
// SessionStarted is answered with a protocol error rather than audio.
func (c *volcengineClient) open(ctx context.Context) (*volcSession, error) {
	hdr := http.Header{}
	hdr.Set("X-Api-Key", c.apiKey)
	hdr.Set("X-Api-Resource-Id", c.resource)
	hdr.Set("X-Api-Connect-Id", uuid.New().String())

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.url, hdr)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("volcengine dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("volcengine dial: %w", err)
	}

	s := &volcSession{conn: conn, sessionID: uuid.New().String()}

	if err := s.write(volcEventStartConnection, "", []byte(`{}`)); err != nil {
		s.close()
		return nil, err
	}
	if err := c.expect(s, volcEventConnectionStarted); err != nil {
		s.close()
		return nil, fmt.Errorf("volcengine connection: %w", err)
	}

	start, err := c.request(volcEventStartSession, "")
	if err != nil {
		s.close()
		return nil, err
	}
	if err := s.write(volcEventStartSession, s.sessionID, start); err != nil {
		s.close()
		return nil, err
	}
	if err := c.expect(s, volcEventSessionStarted); err != nil {
		s.close()
		return nil, fmt.Errorf("volcengine session: %w", err)
	}

	return s, nil
}

// expect reads until the given event arrives, turning a rejection into an
// error that carries the service's own words.
func (c *volcengineClient) expect(s *volcSession, want int32) error {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			return err
		}
		event, payload, ok := parseVolcTTSFrame(data)
		if !ok {
			continue
		}
		if event == want {
			return nil
		}
		if event == volcEventSessionFailed || event == 0 {
			return fmt.Errorf("%s", strings.TrimSpace(string(payload)))
		}
	}
}

func (c *volcengineClient) SynthesizeStream(ctx context.Context, text string) (<-chan StreamChunk, error) {
	s, err := c.open(ctx)
	if err != nil {
		return nil, err
	}

	task, err := c.request(volcEventTaskRequest, text)
	if err != nil {
		s.close()
		return nil, err
	}
	if err := s.write(volcEventTaskRequest, s.sessionID, task); err != nil {
		s.close()
		return nil, err
	}
	// This client is handed a whole sentence, so the utterance is closed
	// immediately; the service still streams the audio back afterwards.
	fin, err := json.Marshal(map[string]any{"event": volcEventFinishSession, "namespace": "BidirectionalTTS"})
	if err != nil {
		s.close()
		return nil, err
	}
	if err := s.write(volcEventFinishSession, s.sessionID, fin); err != nil {
		s.close()
		return nil, err
	}

	ch := make(chan StreamChunk, 8)
	go func() {
		defer close(ch)
		defer s.close()
		c.pump(ctx, s, ch)
	}()
	return ch, nil
}

// pump forwards TTSResponse payloads as PCM until the session finishes.
//
// A barge-in cancels ctx mid-utterance and ReadMessage does not watch a
// context, so the connection is closed from a second goroutine to break the
// read. Without it the client keeps receiving audio for a turn that was
// already cancelled.
func (c *volcengineClient) pump(ctx context.Context, s *volcSession, ch chan<- StreamChunk) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.close()
		case <-done:
		}
	}()

	emit := func(chunk StreamChunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}

	total := 0
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			// A cancelled turn is the expected way this ends, not a fault.
			if ctx.Err() == nil {
				emit(StreamChunk{Err: fmt.Errorf("volcengine stream read: %w", err)})
			}
			return
		}

		event, payload, ok := parseVolcTTSFrame(data)
		if !ok {
			continue
		}

		switch event {
		case volcEventTTSResponse:
			total += len(payload)
			if !emit(StreamChunk{PCM: payload}) {
				return
			}
		case volcEventSessionFinished:
			log.Printf("[tts:volcengine] synthesized %d bytes (%.2fs of audio)",
				total, float64(total)/2/float64(volcengineTTSSampleRate))
			return
		case volcEventSessionFailed, 0:
			emit(StreamChunk{Err: fmt.Errorf("volcengine: %s", strings.TrimSpace(string(payload)))})
			return
		}
	}
}

// Synthesize drains the stream into one buffer, for callers that want the
// whole utterance at once.
func (c *volcengineClient) Synthesize(ctx context.Context, text string) ([]byte, error) {
	ch, err := c.SynthesizeStream(ctx, text)
	if err != nil {
		return nil, err
	}
	var pcm []byte
	for chunk := range ch {
		if chunk.Err != nil {
			return nil, chunk.Err
		}
		pcm = append(pcm, chunk.PCM...)
	}
	return pcm, nil
}
