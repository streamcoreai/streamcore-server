package stt

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
)

// The in-house engine's final frame carries confidence null. A nil pointer
// must map to 0, which TranscriptResult defines as "unknown", so a naive
// dereference would panic, and a misread null would look like a failed
// recognition.
func TestTelnyxSTTFinalFrameMapsNullConfidence(t *testing.T) {
	frame := `{"transcript":" The quick brown fox jumps over the lazy dog, testing one, two, three.","confidence":null,"is_final":true}`

	result, emit, err := routeTelnyxSTTFrame([]byte(frame))
	if err != nil {
		t.Fatalf("final frame rejected: %v", err)
	}
	if !emit {
		t.Fatal("final frame not emitted")
	}
	if !result.IsFinal {
		t.Error("final frame reported partial, want final")
	}
	if result.Text != "The quick brown fox jumps over the lazy dog, testing one, two, three." {
		t.Errorf("text = %q, want the leading space trimmed", result.Text)
	}
	if result.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 (null maps to unknown)", result.Confidence)
	}
}

// Hosted engines stream partials with a real confidence. Dropping the partial
// path would kill barge-in, which gates interruption on partial text.
func TestTelnyxSTTRoutesPartialFrame(t *testing.T) {
	frame := `{"transcript":"The quick brown","confidence":0.8442383,"is_final":false,"speech_final":false}`

	result, emit, err := routeTelnyxSTTFrame([]byte(frame))
	if err != nil {
		t.Fatalf("partial frame rejected: %v", err)
	}
	if !emit {
		t.Fatal("partial frame not emitted")
	}
	if result.IsFinal {
		t.Error("partial frame reported final, want partial")
	}
	if result.Text != "The quick brown" {
		t.Errorf("text = %q, want %q", result.Text, "The quick brown")
	}
	if result.Confidence != 0.8442383 {
		t.Errorf("confidence = %v, want 0.8442383", result.Confidence)
	}
}

// End-of-utterance markers arrive with an empty transcript. Emitting one
// would surface an empty final turn and re-answer the sentence before it.
func TestTelnyxSTTSkipsEmptyTranscript(t *testing.T) {
	_, emit, err := routeTelnyxSTTFrame([]byte(`{"transcript":"","is_final":true,"utterance_end":true}`))
	if err != nil {
		t.Fatalf("utterance-end frame rejected: %v", err)
	}
	if emit {
		t.Error("utterance-end frame emitted, want it skipped")
	}
}

// An unsupported engine is reported in a structured errors array, and the
// detail names the actual problem including the supported list. Surfacing
// only the code or title would leave an operator with "Invalid Parameter"
// and no idea what to fix.
func TestTelnyxSTTSurfacesErrorDetail(t *testing.T) {
	frame := `{"errors":[{"code":"40007","title":"Invalid Parameter","source":{"parameter":"transcription_engine"},"detail":"Unsupported transcription_engine 'telnyx'. Supported engines: AssemblyAI, Azure, Cohere, Deepgram, Google, Humain, Parakeet, Reson8, Soniox, Speechmatics, Telnyx, xAI"}]}`

	_, _, err := routeTelnyxSTTFrame([]byte(frame))
	if err == nil {
		t.Fatal("error frame not surfaced")
	}
	if !strings.Contains(err.Error(), "Unsupported transcription_engine 'telnyx'") {
		t.Errorf("error = %v, want it to carry the server detail", err)
	}
	if !strings.Contains(err.Error(), "Telnyx") {
		t.Errorf("error = %v, want the supported-engine list to survive", err)
	}
}

// A frame that is not JSON means the connection is misbehaving; continuing
// would silently drop whatever transcript followed.
func TestTelnyxSTTRejectsMalformedJSON(t *testing.T) {
	if _, _, err := routeTelnyxSTTFrame([]byte(`{not json`)); err == nil {
		t.Fatal("malformed frame accepted, want a decode error")
	}
}

// The engine rides the dial URL as a query parameter, so it must be
// encoded, and interim_results decides whether barge-in works at all. The
// in-house engine ignores the parameter, so it is not sent there; every
// hosted engine streams partials only when it is present.
func TestTelnyxSTTEndpointBuildsQuery(t *testing.T) {
	cases := []struct {
		name   string
		engine string
		want   string
	}{
		{
			"in-house engine, no interims",
			"Telnyx",
			telnyxSTTURL + "?transcription_engine=Telnyx&input_format=linear16&sample_rate=16000",
		},
		{
			"hosted engine gets interims",
			"Deepgram",
			telnyxSTTURL + "?transcription_engine=Deepgram&input_format=linear16&sample_rate=16000&interim_results=true",
		},
		{
			"engine value is URL-encoded with casing preserved",
			"Two Words",
			telnyxSTTURL + "?transcription_engine=Two+Words&input_format=linear16&sample_rate=16000&interim_results=true",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := telnyxSTTEndpoint(tc.engine); got != tc.want {
				t.Errorf("endpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

// fakeTelnyxSTT is a stand-in for the Telnyx transcription endpoint. It
// records the query string of every dial, checks the auth header, and
// hands each connection to a test-supplied handler, so both socket
// lifecycles can be exercised without network access or an API key.
type fakeTelnyxSTT struct {
	t       *testing.T
	server  *httptest.Server
	queries chan string
	handler func(conn *websocket.Conn)
}

func newFakeTelnyxSTT(t *testing.T, handler func(conn *websocket.Conn)) *fakeTelnyxSTT {
	t.Helper()
	f := &fakeTelnyxSTT{
		t:       t,
		queries: make(chan string, 16),
		handler: handler,
	}

	upgrader := websocket.Upgrader{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("Authorization = %q, want %q", got, "Bearer k")
		}
		select {
		case f.queries <- r.URL.RawQuery:
		default:
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		f.handler(conn)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelnyxSTT) wsURL() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http")
}

func (f *fakeTelnyxSTT) dialQuery(t *testing.T) string {
	t.Helper()
	select {
	case q := <-f.queries:
		return q
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a dial")
		return ""
	}
}

// overrideTelnyxSTTURL points the client at the fake server for the
// duration of one test.
func overrideTelnyxSTTURL(t *testing.T, url string) {
	t.Helper()
	orig := telnyxSTTURL
	telnyxSTTURL = url
	t.Cleanup(func() { telnyxSTTURL = orig })
}

// pcmFrame returns one 20ms frame of constant linear16 PCM at the given
// amplitude: 20000 sits far above the VAD threshold, 0 is silence.
func pcmFrame(amp int16) []byte {
	b := make([]byte, audio.FrameSize*2)
	for i := range audio.FrameSize {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(amp))
	}
	return b
}

func waitResult(t *testing.T, ch <-chan TranscriptResult) TranscriptResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a transcript result")
		return TranscriptResult{}
	}
}

// The hosted engines stream over one long-lived socket: partials and finals
// flow, and the final does not end the session, a later partial still
// arrives on the same connection.
func TestTelnyxSessionClientStreamsPartialsThenFinal(t *testing.T) {
	results := make(chan TranscriptResult, 8)
	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"transcript":"The quick","confidence":0.8442383,"is_final":false}`))
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"transcript":"The quick brown fox jumps over the lazy dog.","confidence":0.95,"is_final":true}`))
		// A second partial after the final proves the socket stayed open.
		_ = conn.WriteMessage(websocket.TextMessage,
			[]byte(`{"transcript":"Testing one","confidence":0.9,"is_final":false}`))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	client, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "Deepgram"}, func(r TranscriptResult) {
			results <- r
		})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	if got, want := f.dialQuery(t), "transcription_engine=Deepgram&input_format=linear16&sample_rate=16000&interim_results=true"; got != want {
		t.Errorf("dial query = %q, want %q", got, want)
	}

	r := waitResult(t, results)
	if r.IsFinal || r.Text != "The quick" || r.Confidence != 0.8442383 {
		t.Errorf("first result = %+v, want the partial", r)
	}
	r = waitResult(t, results)
	if !r.IsFinal || r.Text != "The quick brown fox jumps over the lazy dog." || r.Confidence != 0.95 {
		t.Errorf("second result = %+v, want the final", r)
	}
	r = waitResult(t, results)
	if r.IsFinal || r.Text != "Testing one" {
		t.Errorf("third result = %+v, want the post-final partial on the same socket", r)
	}

	if err := client.SendAudio(pcmFrame(0)); err != nil {
		t.Errorf("session SendAudio: %v", err)
	}
}

// The engine string is sent verbatim: no trimming, no case folding. It is
// case-sensitive on the server side, so any normalization here would turn
// a working configuration into a rejected dial.
func TestTelnyxEngineStringReachesURLVerbatim(t *testing.T) {
	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	client, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "MixedCaseEngine"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	if got, want := f.dialQuery(t), "transcription_engine=MixedCaseEngine&input_format=linear16&sample_rate=16000&interim_results=true"; got != want {
		t.Errorf("dial query = %q, want %q", got, want)
	}
}

// An unset engine defaults to Deepgram, the streaming engine, so barge-in
// and live captions work without any extra configuration.
func TestTelnyxEmptyEngineDefaultsToDeepgram(t *testing.T) {
	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	client, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	if got, want := f.dialQuery(t), "transcription_engine=Deepgram&input_format=linear16&sample_rate=16000&interim_results=true"; got != want {
		t.Errorf("dial query = %q, want %q", got, want)
	}
}

// The in-house engine emits one final per utterance, so the client runs
// one socket per utterance: the engine string rides the URL without
// interim_results, every frame of the utterance reaches the server, the
// final (confidence null) is forwarded exactly once, the client closes
// the socket afterwards because the server holds it open, and SendAudio
// keeps accepting audio for the next utterance instead of erroring a
// spent session.
func TestTelnyxUtteranceClientOneSocketPerUtterance(t *testing.T) {
	results := make(chan TranscriptResult, 4)
	// socketClosed fires when the server's read ends, which only happens
	// once the client has closed the socket after its single final.
	socketClosed := make(chan int, 4)
	var dials atomic.Int32

	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		dials.Add(1)
		frames := 0
		// Answer with the single final once the audio starts arriving;
		// the client reads it only after it has finished sending.
		sentFinal := false
		for {
			msgType, _, err := conn.ReadMessage()
			if err != nil {
				socketClosed <- frames
				return
			}
			if msgType != websocket.BinaryMessage {
				continue
			}
			frames++
			if !sentFinal {
				sentFinal = true
				_ = conn.WriteMessage(websocket.TextMessage,
					[]byte(`{"transcript":"The quick brown fox jumps over the lazy dog. Testing one two three, this is the Telnyx STT capture for StreamCore.","confidence":null,"is_final":true}`))
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	client, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "Telnyx"}, func(r TranscriptResult) {
			results <- r
		})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer client.Close()

	// Utterance one: 15 loud frames open it, 30 silent frames close it.
	for i := 0; i < 15; i++ {
		if err := client.SendAudio(pcmFrame(20000)); err != nil {
			t.Fatalf("send loud frame: %v", err)
		}
	}
	for i := 0; i < 30; i++ {
		if err := client.SendAudio(pcmFrame(0)); err != nil {
			t.Fatalf("send silent frame: %v", err)
		}
	}

	r := waitResult(t, results)
	if !r.IsFinal {
		t.Errorf("result = %+v, want the single final", r)
	}
	if r.Text != "The quick brown fox jumps over the lazy dog. Testing one two three, this is the Telnyx STT capture for StreamCore." {
		t.Errorf("text = %q, want the capture sentence", r.Text)
	}
	if r.Confidence != 0 {
		t.Errorf("confidence = %v, want 0 (null maps to unknown)", r.Confidence)
	}

	// Utterance two: SendAudio across the boundary must keep working, and
	// it must produce a second socket, not an error.
	for i := 0; i < 15; i++ {
		if err := client.SendAudio(pcmFrame(20000)); err != nil {
			t.Fatalf("send loud frame after first final: %v", err)
		}
	}
	for i := 0; i < 30; i++ {
		if err := client.SendAudio(pcmFrame(0)); err != nil {
			t.Fatalf("send silent frame after first final: %v", err)
		}
	}

	r = waitResult(t, results)
	if !r.IsFinal {
		t.Errorf("second result = %+v, want the second final", r)
	}

	if got := dials.Load(); got != 2 {
		t.Errorf("dials = %d, want one socket per utterance (2)", got)
	}
	for i := 0; i < 2; i++ {
		if got, want := f.dialQuery(t), "transcription_engine=Telnyx&input_format=linear16&sample_rate=16000"; got != want {
			t.Errorf("utterance dial query = %q, want %q", got, want)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case frames := <-socketClosed:
			// 15 loud + 30 silent frames = the 45-frame utterance, plus
			// the onset lookback when there is one.
			if frames < 45 {
				t.Errorf("socket closed after %d frames, want the full 45-frame utterance", frames)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the client to close the socket after its final")
		}
	}
}

// Silence alone must never dial: the socket only exists per utterance, and
// there is no utterance until the detector hears speech.
func TestTelnyxUtteranceClientSilenceNeverDials(t *testing.T) {
	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	client, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "Telnyx"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	for i := 0; i < 200; i++ {
		if err := client.SendAudio(pcmFrame(0)); err != nil {
			t.Fatalf("send silent frame: %v", err)
		}
	}
	client.Close()

	select {
	case q := <-f.queries:
		t.Errorf("dialed with query %q on silence, want no connection", q)
	default:
	}
}

// EmitsPartials splits along the same line as the socket lifecycle: the
// hosted engines stream interims, so barge-in keeps its partial-text gate;
// the in-house engine is finals-only, so the pipeline falls back to
// VAD-only barge-in. The exact-case engine string decides, consistent
// with how NewTelnyxClient routes.
func TestTelnyxClientEmitsPartialsByEngine(t *testing.T) {
	f := newFakeTelnyxSTT(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	overrideTelnyxSTTURL(t, f.wsURL())

	session, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "Deepgram"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("session client: %v", err)
	}
	defer session.Close()

	if ep, ok := session.(PartialsEmitter); !ok || !ep.EmitsPartials() {
		t.Error("hosted-engine client EmitsPartials = false, want true (it streams interims)")
	}

	utterance, err := NewTelnyxClient(context.Background(),
		config.TelnyxConfig{APIKey: "k", TranscriptionEngine: "Telnyx"}, func(TranscriptResult) {})
	if err != nil {
		t.Fatalf("utterance client: %v", err)
	}
	defer utterance.Close()

	if ep, ok := utterance.(PartialsEmitter); !ok || ep.EmitsPartials() {
		t.Error("in-house-engine client EmitsPartials = true, want false (one final per utterance, nothing before it)")
	}
}
