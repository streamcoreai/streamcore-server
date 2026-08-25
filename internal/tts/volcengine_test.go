package tts

import (
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

// serverFrame builds a response the way the service does: the 4-byte header, a
// 4-byte event number, a length-prefixed session id for session-scoped events,
// then the payload.
func serverFrame(event int32, sessionID string, payload []byte) []byte {
	f := []byte{0x11, volcTTSTypeFullClient<<4 | volcTTSFlagWithEvent, volcTTSSerialisationJS, 0x00}
	ev := make([]byte, 4)
	binary.BigEndian.PutUint32(ev, uint32(event))
	f = append(f, ev...)
	if sessionID != "" {
		l := make([]byte, 4)
		binary.BigEndian.PutUint32(l, uint32(len(sessionID)))
		f = append(append(f, l...), sessionID...)
	}
	l := make([]byte, 4)
	binary.BigEndian.PutUint32(l, uint32(len(payload)))
	return append(append(f, l...), payload...)
}

// Session-scoped events carry a session id that connection-scoped ones do not.
// Reading a TTSResponse without skipping it prepends the id's length and bytes
// to the audio, which plays as a burst of noise at the start of every chunk
// rather than failing.
func TestVolcTTSSkipsSessionIDOnScopedEvents(t *testing.T) {
	audio := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	sid := "1f2e3d4c-5b6a-7988-9a0b-1c2d3e4f5061"

	event, payload, ok := parseVolcTTSFrame(serverFrame(volcEventTTSResponse, sid, audio))
	if !ok {
		t.Fatal("frame rejected, want it parsed")
	}
	if event != volcEventTTSResponse {
		t.Errorf("event = %d, want %d", event, volcEventTTSResponse)
	}
	if string(payload) != string(audio) {
		t.Errorf("payload = %v, want %v — the session id was not skipped", payload, audio)
	}
}

// ConnectionStarted carries no session id. Skipping one that is not there eats
// the first four bytes of the payload as a phantom length.
func TestVolcTTSConnectionScopedEventHasNoSessionID(t *testing.T) {
	body := []byte("6526d0c4-9426-4dd3-a91e-49caa67d85c7")

	event, payload, ok := parseVolcTTSFrame(serverFrame(volcEventConnectionStarted, "", body))
	if !ok {
		t.Fatal("frame rejected, want it parsed")
	}
	if event != volcEventConnectionStarted {
		t.Errorf("event = %d, want %d", event, volcEventConnectionStarted)
	}
	if string(payload) != string(body) {
		t.Errorf("payload = %q, want %q", payload, body)
	}
}

// The scoped/unscoped split is the whole hazard of this layout, so pin which
// events fall on which side.
func TestVolcTTSSessionScopedEventSet(t *testing.T) {
	for _, ev := range []int32{
		volcEventSessionStarted, volcEventSessionFinished,
		volcEventSessionFailed, volcEventTTSResponse,
	} {
		if !volcSessionScopedEvent(ev) {
			t.Errorf("event %d not treated as session-scoped, want it to be", ev)
		}
	}
	for _, ev := range []int32{volcEventConnectionStarted, volcEventStartConnection} {
		if volcSessionScopedEvent(ev) {
			t.Errorf("event %d treated as session-scoped, want it not to be", ev)
		}
	}
}

// A frame shorter than the header must be discarded rather than slicing out of
// range — the connection can deliver a truncated message on teardown.
func TestVolcTTSRejectsShortFrame(t *testing.T) {
	for _, n := range []int{0, 1, 4, 7} {
		if _, _, ok := parseVolcTTSFrame(make([]byte, n)); ok {
			t.Errorf("%d-byte frame accepted, want it rejected", n)
		}
	}
}

// The client frame layout is fixed by the service; a wrong byte fails as an
// opaque protocol error that names nothing.
func TestVolcTTSClientFrameLayout(t *testing.T) {
	sid := "abc"
	payload := []byte(`{"a":1}`)
	f := volcFrame(volcEventTaskRequest, sid, payload)

	if f[0] != 0x11 {
		t.Errorf("byte 0 = %#x, want 0x11", f[0])
	}
	if got := (f[1] >> 4) & 0x0F; got != volcTTSTypeFullClient {
		t.Errorf("message type = %d, want %d", got, volcTTSTypeFullClient)
	}
	// The event flag is what tells the service an event number follows;
	// without it the header is read as a plain frame and the event becomes
	// the first four bytes of the payload.
	if got := f[1] & 0x0F; got&volcTTSFlagWithEvent == 0 {
		t.Errorf("flags = %#b, want the event bit %#b set", got, volcTTSFlagWithEvent)
	}
	if got := int32(binary.BigEndian.Uint32(f[4:8])); got != volcEventTaskRequest {
		t.Errorf("event = %d, want %d", got, volcEventTaskRequest)
	}
	if got := int(binary.BigEndian.Uint32(f[8:12])); got != len(sid) {
		t.Errorf("session id length = %d, want %d", got, len(sid))
	}
	if got := string(f[12 : 12+len(sid)]); got != sid {
		t.Errorf("session id = %q, want %q", got, sid)
	}
	if got := int(binary.BigEndian.Uint32(f[12+len(sid) : 16+len(sid)])); got != len(payload) {
		t.Errorf("payload length = %d, want %d", got, len(payload))
	}
}

// A connection-scoped frame must omit the session id entirely, not send an
// empty one — a zero-length id shifts the payload by four bytes.
func TestVolcTTSConnectionFrameOmitsSessionID(t *testing.T) {
	payload := []byte(`{}`)
	f := volcFrame(volcEventStartConnection, "", payload)

	if got := int(binary.BigEndian.Uint32(f[8:12])); got != len(payload) {
		t.Errorf("bytes 8-12 = %d, want the payload length %d — a session id was written", got, len(payload))
	}
	if got := string(f[12:]); got != string(payload) {
		t.Errorf("payload = %q, want %q", got, payload)
	}
}

// The request body is a fixed shape; the service rejects the session outright
// when namespace or audio_params are wrong.
func TestVolcTTSRequestShape(t *testing.T) {
	c := NewVolcengineClient("k", "", "", "").(*volcengineClient)

	body, err := c.request(volcEventTaskRequest, "你好")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded["namespace"] != "BidirectionalTTS" {
		t.Errorf("namespace = %v, want BidirectionalTTS", decoded["namespace"])
	}
	if decoded["event"].(float64) != float64(volcEventTaskRequest) {
		t.Errorf("event = %v, want %d", decoded["event"], volcEventTaskRequest)
	}

	rp := decoded["req_params"].(map[string]any)
	if rp["text"] != "你好" {
		t.Errorf("text = %v, want 你好", rp["text"])
	}
	if rp["speaker"] != defaultVolcengineSpeaker {
		t.Errorf("speaker = %v, want %s", rp["speaker"], defaultVolcengineSpeaker)
	}

	ap := rp["audio_params"].(map[string]any)
	if ap["format"] != "pcm" {
		t.Errorf("format = %v, want pcm", ap["format"])
	}
	// The pipeline plays 16kHz linear16 and this provider honours the request,
	// so a wrong rate is not resampled anywhere — it plays back at the wrong
	// speed and pitch.
	if ap["sample_rate"].(float64) != 16000 {
		t.Errorf("sample_rate = %v, want 16000", ap["sample_rate"])
	}
}

// StartSession carries no text; leaving an empty field in would be read as a
// request to synthesize nothing.
func TestVolcTTSStartSessionOmitsText(t *testing.T) {
	c := NewVolcengineClient("k", "", "", "").(*volcengineClient)

	body, err := c.request(volcEventStartSession, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"text"`) {
		t.Errorf("body = %s, want no text field on StartSession", body)
	}
}

// Empty speaker, resource and url fall back rather than being sent blank.
func TestVolcTTSDefaults(t *testing.T) {
	c := NewVolcengineClient("k", "", "", "").(*volcengineClient)
	if c.speaker != defaultVolcengineSpeaker {
		t.Errorf("speaker = %q, want %q", c.speaker, defaultVolcengineSpeaker)
	}
	// The 2.0 resource is the default because the 1.0 one is a separate
	// entitlement that answers 403 until activated.
	if c.resource != defaultVolcengineTTSResource {
		t.Errorf("resource = %q, want %q", c.resource, defaultVolcengineTTSResource)
	}
	if c.url != volcengineTTSURL {
		t.Errorf("url = %q, want %q", c.url, volcengineTTSURL)
	}

	custom := NewVolcengineClient("k", "zh_male_m191_uranus_bigtts", "seed-icl-2.0", "wss://example/x").(*volcengineClient)
	if custom.speaker != "zh_male_m191_uranus_bigtts" || custom.resource != "seed-icl-2.0" || custom.url != "wss://example/x" {
		t.Errorf("explicit values overwritten: %+v", custom)
	}
}

// Writing after close must fail rather than touching the connection: a
// barge-in closes it while the send path may still be running.
func TestVolcTTSSessionRejectsWriteAfterClose(t *testing.T) {
	s := &volcSession{closed: true}
	if err := s.write(volcEventTaskRequest, "sid", []byte(`{}`)); err == nil {
		t.Error("write on a closed session succeeded, want an error instead of a nil-conn panic")
	}
}
