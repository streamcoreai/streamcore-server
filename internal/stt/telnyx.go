package stt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

const (
	// telnyxSTTURL is the streaming transcription WebSocket. One endpoint
	// fronts every engine; the engine rides the transcription_engine query
	// parameter.
	telnyxSTTURL = "wss://api.telnyx.com/v2/speech-to-text/transcription"
	// telnyxInHouseEngine is Telnyx's own recognizer, reached only by
	// spelling the name exactly: the parameter is case-sensitive, and a
	// lowercase "telnyx" is rejected with a structured error frame listing
	// the supported engines.
	telnyxInHouseEngine = "Telnyx"
	// telnyxFinalSpentReason is what SendAudio reports once the in-house
	// engine's single final has been delivered. Saying "connection closed"
	// here would read as a network failure when it is in fact the documented
	// one-final-per-connection contract.
	telnyxFinalSpentReason = "the in-house Telnyx engine emits one final per connection and this connection's final has been delivered; set stt_engine to a streaming engine (e.g. \"Deepgram\") for continuous transcription"
)

// TelnyxClient implements the Client interface against Telnyx's streaming
// speech-to-text WebSocket.
//
// The wire protocol is raw linear16 PCM upstream (binary frames at the
// pipeline's native 16 kHz mono, so nothing resamples) and JSON transcript
// frames downstream. One endpoint fronts a dozen engines — Telnyx's in-house
// recognizer plus hosted Deepgram, AssemblyAI, Azure, and others — selected
// by the transcription_engine query parameter, so one [telnyx] section and
// one API key cover both STT and TTS.
//
// The engines differ in what they stream, and the difference is not cosmetic:
//
//   - The in-house engine emits exactly one final frame after the caller
//     stops speaking — no interims, no timestamps, confidence null — and
//     then holds the socket open indefinitely; the server never closes it.
//     The client therefore tears the connection down itself once that final
//     is delivered, and the next SendAudio fails with the reason above
//     rather than pretending to accept audio. Barge-in and live captions
//     hard-depend on interim results (internal/pipeline/inbound.go gates
//     interruption on partial text), so this engine suits
//     final-transcript-only deployments; see the design discussion in
//     issue #75.
//   - Every other engine streams partials when asked, so the client appends
//     interim_results=true for them and barge-in works as with any other
//     provider. On the in-house engine the parameter is silently ignored —
//     the final still arrives — so it is simply not sent there.
type TelnyxClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc

	// closeOnFinal tears the connection down after the first final, the
	// in-house engine's one-shot contract above.
	closeOnFinal bool

	// writeMu serialises writes: gorilla/websocket allows only one concurrent
	// writer, and Close races SendAudio when a call ends mid-utterance.
	writeMu     sync.Mutex
	closed      bool
	closeReason string
}

// NewTelnyxClient dials the Telnyx transcription endpoint and starts a
// goroutine that forwards transcripts to onResult. Empty stt_engine defaults
// to the in-house Telnyx engine.
func NewTelnyxClient(ctx context.Context, cfg config.TelnyxConfig, onResult func(TranscriptResult)) (Client, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	engine := strings.TrimSpace(cfg.STTEngine)
	if engine == "" {
		engine = telnyxInHouseEngine
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+cfg.APIKey)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(sttCtx, telnyxSTTEndpoint(engine), header)
	if err != nil {
		cancel()
		if resp != nil {
			// The body carries the actual reason — a rejected key reads as a
			// bare 401 — which the status code alone does not convey.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("telnyx stt dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("telnyx stt dial: %w", err)
	}

	c := &TelnyxClient{
		conn:         conn,
		cancel:       cancel,
		closeOnFinal: engine == telnyxInHouseEngine,
	}

	log.Printf("[stt] connected to Telnyx STT (engine %s)", engine)
	go c.readLoop(sttCtx, onResult)

	return c, nil
}

// telnyxSTTEndpoint builds the dial URL. The engine value is URL-encoded
// because it reaches the server as a query parameter, and interim_results is
// appended for every engine except the in-house one: only the hosted engines
// emit partials, and the pipeline's barge-in hard-depends on them.
func telnyxSTTEndpoint(engine string) string {
	endpoint := fmt.Sprintf("%s?transcription_engine=%s&input_format=linear16&sample_rate=16000",
		telnyxSTTURL, url.QueryEscape(engine))
	if engine != telnyxInHouseEngine {
		endpoint += "&interim_results=true"
	}
	return endpoint
}

// telnyxSTTFrame is one transcript message. The in-house engine reports
// confidence as null; hosted engines report a 0-1 float.
type telnyxSTTFrame struct {
	Transcript string `json:"transcript"`
	// Confidence is a pointer so null — the in-house engine — stays
	// distinguishable from a genuine 0.0. Null maps to 0, which
	// TranscriptResult defines as "unknown", not "low".
	Confidence *float64 `json:"confidence"`
	IsFinal    bool     `json:"is_final"`
	// Errors carries a rejected request (an unsupported engine, say) instead
	// of a transcript; the server closes the connection right after.
	Errors []telnyxSTTError `json:"errors"`
}

type telnyxSTTError struct {
	Code   string `json:"code"`
	Title  string `json:"title"`
	Source struct {
		Parameter string `json:"parameter"`
	} `json:"source"`
	Detail string `json:"detail"`
}

// routeTelnyxSTTFrame classifies one server frame: emit reports whether the
// frame carries a transcript the pipeline should see, and err is set for
// error frames and undecodable messages. Frames with no transcript text —
// utterance-end markers, keepalives — come back with emit false so the
// caller skips them; emitting one would surface an empty final turn.
func routeTelnyxSTTFrame(msg []byte) (result TranscriptResult, emit bool, err error) {
	var f telnyxSTTFrame
	if jerr := json.Unmarshal(msg, &f); jerr != nil {
		return TranscriptResult{}, false, fmt.Errorf("telnyx stt frame decode: %w", jerr)
	}
	if len(f.Errors) > 0 {
		// The detail names the actual problem ("Unsupported
		// transcription_engine 'telnyx'. Supported engines: …"), which beats
		// the code and title for an operator reading the log. It is the
		// first entry: the service reports one error per frame.
		e := f.Errors[0]
		detail := e.Detail
		if detail == "" {
			detail = e.Title
		}
		return TranscriptResult{}, false, fmt.Errorf("telnyx stt: %s", detail)
	}
	text := strings.TrimSpace(f.Transcript)
	if text == "" {
		return TranscriptResult{}, false, nil
	}
	var confidence float64
	if f.Confidence != nil {
		confidence = *f.Confidence
	}
	return TranscriptResult{Text: text, IsFinal: f.IsFinal, Confidence: confidence}, true, nil
}

// readLoop forwards transcript frames to onResult until the connection ends.
func (c *TelnyxClient) readLoop(ctx context.Context, onResult func(TranscriptResult)) {
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[stt] telnyx read: %v", err)
			}
			return
		}

		result, emit, rerr := routeTelnyxSTTFrame(msg)
		if rerr != nil {
			// The server closes the connection right after an error frame;
			// there is nothing left to read.
			log.Printf("[stt] telnyx: %v", rerr)
			return
		}
		if !emit {
			continue
		}
		if result.IsFinal {
			log.Printf("[stt] final: %q", result.Text)
		}
		onResult(result)

		// The in-house engine's contract is one final per connection, and
		// the server holds the socket open afterwards — it never closes.
		// Tear it down from this side so the spent session does not linger
		// until call teardown, and let SendAudio report why.
		if result.IsFinal && c.closeOnFinal {
			c.shutdown(telnyxFinalSpentReason)
			return
		}
	}
}

// SendAudio forwards a chunk of PCM. The pipeline calls this every 20ms.
func (c *TelnyxClient) SendAudio(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("telnyx stt: %s", c.closeReason)
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close tears down the connection. There is no flush to wait for: the
// service settles an utterance on its own silence detection, so a
// client-side close mid-utterance simply drops the stream.
func (c *TelnyxClient) Close() {
	c.shutdown("connection closed")
}

// shutdown closes the connection once, recording why so SendAudio can tell a
// spent finals-only session apart from a normal teardown.
func (c *TelnyxClient) shutdown(reason string) {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return
	}
	c.closed = true
	c.closeReason = reason
	c.writeMu.Unlock()

	_ = c.conn.Close()
	c.cancel()
}
