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

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/vad"
)

// telnyxSTTURL is the streaming transcription WebSocket. One endpoint
// fronts every engine; the engine rides the transcription_engine query
// parameter. It is a variable only so hermetic tests can point the client
// at a fake server, the same override pattern as grokDialURL.
var telnyxSTTURL = "wss://api.telnyx.com/v2/speech-to-text/transcription"

const (
	// telnyxInHouseEngine is Telnyx's own recognizer, reached only by
	// spelling the name exactly: the parameter is case-sensitive, and a
	// lowercase "telnyx" is rejected with a structured error frame listing
	// the supported engines.
	telnyxInHouseEngine = "Telnyx"
	// telnyxDefaultEngine is what an unset transcription_engine falls back
	// to. It streams partials, so barge-in and live captions work without
	// any extra configuration; a finals-only default would silently degrade
	// both (barge-in to VAD-only, captions to finals only) for anyone who
	// just sets the provider and starts talking.
	telnyxDefaultEngine = "Deepgram"

	// Client-side endpointing for the in-house engine, which transcribes
	// only a finished utterance. Windows are counted in pipeline frames:
	// the inbound loop delivers 20ms of audio per SendAudio call.
	telnyxVADThreshold    = 1200.0 // the same base the pipeline's VADs start from
	telnyxVADSpeechFrames = 10     // 200ms of speech to open an utterance
	telnyxVADSilentFrames = 30     // 600ms of silence to close one, the silence timeout openai.go uses for the same job

	// One frame of linear16 at the pipeline's native rate: 320 samples,
	// two bytes each.
	telnyxFrameBytes = audio.FrameSize * 2

	// Idle audio retained before onset, so the frames that lifted the
	// detector over its start threshold are not lost: onset fires on the
	// 10th loud frame, and 1s of lookback covers that with margin.
	telnyxOnsetLookbackBytes = 50 * telnyxFrameBytes

	// An utterance is capped like openai.go caps its buffer: past 30s the
	// audio is sent as-is and a fresh utterance opens, so a caller who
	// never pauses still gets finals.
	telnyxMaxUtteranceBytes = 1500 * telnyxFrameBytes

	// The utterance upload is paced at the fastest rate verified against
	// the live endpoint: 200ms of audio per write, a 30ms pause between
	// writes. Sending slower only delays the final; sending faster is
	// untested.
	telnyxSendBatchFrames = 10
	telnyxSendPace        = 30 * time.Millisecond

	// Safety net for a socket that never delivers its final: the server
	// normally answers within a few seconds of audio stopping.
	telnyxFinalWait = 30 * time.Second
)

// NewTelnyxClient returns the right client for the configured engine. The
// two engines have opposite socket lifecycles, because they stream
// different things:
//
//   - Every hosted engine (Deepgram, AssemblyAI, ...) streams interims,
//     so it gets one long-lived socket per session: partials flow into
//     onResult with IsFinal false, the same shape as deepgram.go, and
//     barge-in and live captions work.
//   - The in-house engine emits exactly one final per utterance, only
//     after audio stops, and holds the socket open afterwards. A
//     long-lived socket would be spent after the first sentence, so it
//     gets one socket per utterance instead: internal/vad.Detector
//     endpoints the speech client-side, the utterance is uploaded, the
//     single final is awaited, and the client closes the socket. This is
//     the same endpointing problem openai.go solves for its batch
//     transcription, reused here per utterance.
//
// The engine string is sent to the endpoint verbatim: it is
// case-sensitive on the server side, so it is never trimmed or
// normalised. Only "Deepgram" and "Telnyx" are verified; other engines
// pass through untested.
func NewTelnyxClient(ctx context.Context, cfg config.TelnyxConfig, onResult func(TranscriptResult)) (Client, error) {
	engine := cfg.TranscriptionEngine
	if engine == "" {
		engine = telnyxDefaultEngine
	}

	if engine == telnyxInHouseEngine {
		log.Printf("[stt] telnyx in-house engine emits finals only: no interim results, so live captions show finals only and barge-in runs VAD-only after the backchannel window")
		return newTelnyxUtteranceClient(ctx, cfg.APIKey, onResult), nil
	}
	return newTelnyxSessionClient(ctx, engine, cfg.APIKey, onResult)
}

// telnyxSessionClient is the streaming-engine half: one long-lived socket
// per session, the shape deepgram.go uses.
type telnyxSessionClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc

	// writeMu serialises writes: gorilla/websocket allows only one
	// concurrent writer, and Close races SendAudio when a call ends
	// mid-utterance.
	writeMu sync.Mutex
	closed  bool
}

func newTelnyxSessionClient(ctx context.Context, engine, apiKey string, onResult func(TranscriptResult)) (Client, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	conn, err := dialTelnyxSTT(sttCtx, engine, apiKey)
	if err != nil {
		cancel()
		return nil, err
	}

	c := &telnyxSessionClient{conn: conn, cancel: cancel}

	log.Printf("[stt] connected to Telnyx STT (engine %s)", engine)
	go c.readLoop(sttCtx, onResult)

	return c, nil
}

// readLoop forwards transcript frames to onResult until the connection
// ends. Partials and finals both flow: barge-in and live captions read
// the partials, the turn buffer reads the finals.
func (c *telnyxSessionClient) readLoop(ctx context.Context, onResult func(TranscriptResult)) {
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
			// The server closes the connection right after an error
			// frame; there is nothing left to read.
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
	}
}

// SendAudio forwards a chunk of PCM. The pipeline calls this every 20ms.
func (c *telnyxSessionClient) SendAudio(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("telnyx stt: connection closed")
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// Close tears down the session connection.
func (c *telnyxSessionClient) Close() {
	c.writeMu.Lock()
	if c.closed {
		c.writeMu.Unlock()
		return
	}
	c.closed = true
	c.writeMu.Unlock()

	_ = c.conn.Close()
	c.cancel()
}

// EmitsPartials reports whether this client streams interim results. Every
// hosted engine streams interims exactly like deepgram.go does, which is
// what barge-in's partial-text gate and live captions read.
func (c *telnyxSessionClient) EmitsPartials() bool { return true }

// telnyxUtteranceClient is the in-house-engine half: one socket per
// utterance with client-side endpointing.
type telnyxUtteranceClient struct {
	ctx      context.Context
	cancel   context.CancelFunc
	apiKey   string
	onResult func(TranscriptResult)

	// mu guards every field below it and the in-flight socket registry.
	mu           sync.Mutex
	detector     *vad.Detector
	speechActive bool
	// utterance holds the frames of the utterance being spoken, from the
	// onset lookback to the frame that ends it.
	utterance     [][]byte
	uttsBytes     int
	lookback      [][]byte
	lookbackBytes int
	// conns holds the utterance sockets still in flight so Close can tear
	// them down and unblock their reads.
	conns map[*websocket.Conn]struct{}
	wg    sync.WaitGroup
}

func newTelnyxUtteranceClient(ctx context.Context, apiKey string, onResult func(TranscriptResult)) Client {
	sttCtx, cancel := context.WithCancel(ctx)
	return &telnyxUtteranceClient{
		ctx:      sttCtx,
		cancel:   cancel,
		apiKey:   apiKey,
		onResult: onResult,
		detector: vad.New(telnyxVADThreshold, telnyxVADSpeechFrames, telnyxVADSilentFrames),
		conns:    make(map[*websocket.Conn]struct{}),
	}
}

// SendAudio runs the endpointing and never fails across utterance
// boundaries: an error would stop the pipeline's inbound loop, so a spent
// socket (there is one per utterance now) must simply be replaced.
//
// The frames are copied because the pipeline reuses its send buffer, and
// they are buffered rather than written through: the utterance socket is
// only dialed after the detector reports the end of speech, the pattern
// openai.go uses for the same problem. Streaming the audio live instead
// would buy nothing, since this engine emits no partials, and it would
// mean dialing (about 600ms) against the first syllable.
func (c *telnyxUtteranceClient) SendAudio(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ctx.Err() != nil {
		return fmt.Errorf("telnyx stt: client closed")
	}

	started, ended := c.detector.Process(audio.Linear16BytesToPCM(data))
	if started {
		c.speechActive = true
		// The lookback becomes the utterance prefix, so the onset
		// frames the detector needed to see before firing are
		// transcribed too.
		c.utterance = append(c.utterance, c.lookback...)
		c.uttsBytes += c.lookbackBytes
		c.lookback = nil
		c.lookbackBytes = 0
	}

	frame := append([]byte(nil), data...)
	if c.speechActive {
		c.utterance = append(c.utterance, frame)
		c.uttsBytes += len(frame)
	} else {
		c.lookback = append(c.lookback, frame)
		c.lookbackBytes += len(frame)
		for c.lookbackBytes > telnyxOnsetLookbackBytes {
			c.lookbackBytes -= len(c.lookback[0])
			c.lookback = c.lookback[1:]
		}
	}

	var frames [][]byte
	if ended {
		c.speechActive = false
		frames = c.utterance
		c.utterance = nil
		c.uttsBytes = 0
	} else if c.uttsBytes >= telnyxMaxUtteranceBytes {
		// Force-flush mid-speech, like openai.go's buffer cap: speech
		// continues into a fresh utterance, and the detector keeps its
		// state.
		frames = c.utterance
		c.utterance = nil
		c.uttsBytes = 0
	}
	if len(frames) > 0 {
		c.wg.Add(1)
		go c.transcribe(frames)
	}
	return nil
}

// transcribe dials one socket, uploads the finished utterance at the
// verified pace, waits for the engine's single final, and closes the
// socket: the server holds it open after the final, so the client has to
// end the conversation itself.
func (c *telnyxUtteranceClient) transcribe(frames [][]byte) {
	defer c.wg.Done()

	conn, err := dialTelnyxSTT(c.ctx, telnyxInHouseEngine, c.apiKey)
	if err != nil {
		if c.ctx.Err() == nil {
			log.Printf("[stt] telnyx utterance dial: %v", err)
		}
		return
	}
	defer conn.Close()

	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.conns[conn] = struct{}{}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.conns, conn)
		c.mu.Unlock()
	}()

	for i, frame := range frames {
		if c.ctx.Err() != nil {
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			if c.ctx.Err() == nil {
				log.Printf("[stt] telnyx utterance write: %v", err)
			}
			return
		}
		if (i+1)%telnyxSendBatchFrames == 0 {
			select {
			case <-time.After(telnyxSendPace):
			case <-c.ctx.Done():
				return
			}
		}
	}

	// The final only lands after the audio stops, which it just did: the
	// deadline only guards a socket that never answers.
	_ = conn.SetReadDeadline(time.Now().Add(telnyxFinalWait))
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if c.ctx.Err() == nil {
				log.Printf("[stt] telnyx utterance read: %v", err)
			}
			return
		}
		result, emit, rerr := routeTelnyxSTTFrame(msg)
		if rerr != nil {
			log.Printf("[stt] telnyx: %v", rerr)
			return
		}
		if !emit || !result.IsFinal {
			// The engine's contract is one final and nothing before
			// it; an empty marker frame is skipped, and an interim,
			// which the contract says cannot arrive, is dropped
			// rather than double-echoed inside the final.
			continue
		}
		log.Printf("[stt] final: %q", result.Text)
		c.onResult(result)
		return
	}
}

// Close cancels the client and waits for in-flight utterances to finish
// uploading or give up. The sockets are closed explicitly so a blocked
// read ends immediately instead of waiting out its deadline.
func (c *telnyxUtteranceClient) Close() {
	c.cancel()

	c.mu.Lock()
	for conn := range c.conns {
		_ = conn.Close()
	}
	c.mu.Unlock()

	c.wg.Wait()
}

// EmitsPartials reports whether this client streams interim results. The
// in-house engine emits exactly one final per utterance and nothing before
// it, so this is false: the pipeline falls back to VAD-only barge-in
// (issue #75).
func (c *telnyxUtteranceClient) EmitsPartials() bool { return false }

// telnyxSTTEndpoint builds the dial URL. The engine value is URL-encoded
// because it reaches the server as a query parameter, and interim_results
// is appended for every engine except the in-house one: only the hosted
// engines stream partials, and the pipeline's barge-in hard-depends on
// them. The in-house engine ignores the parameter, so it is not sent.
func telnyxSTTEndpoint(engine string) string {
	endpoint := fmt.Sprintf("%s?transcription_engine=%s&input_format=linear16&sample_rate=16000",
		telnyxSTTURL, url.QueryEscape(engine))
	if engine != telnyxInHouseEngine {
		endpoint += "&interim_results=true"
	}
	return endpoint
}

// dialTelnyxSTT opens one transcription socket with the API key in the
// Authorization header.
func dialTelnyxSTT(ctx context.Context, engine, apiKey string) (*websocket.Conn, error) {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+apiKey)

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, telnyxSTTEndpoint(engine), header)
	if err != nil {
		if resp != nil {
			// The body carries the actual reason, a rejected key reads as
			// a bare 401, which the status code alone does not convey.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("telnyx stt dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("telnyx stt dial: %w", err)
	}
	return conn, nil
}

// telnyxSTTFrame is one transcript message. The in-house engine reports
// confidence as null; hosted engines report a 0-1 float.
type telnyxSTTFrame struct {
	Transcript string `json:"transcript"`
	// Confidence is a pointer so null, the in-house engine's value,
	// stays distinguishable from a genuine 0.0. Null maps to 0, which
	// TranscriptResult defines as "unknown", not "low".
	Confidence *float64 `json:"confidence"`
	IsFinal    bool     `json:"is_final"`
	// Errors carries a rejected request (an unsupported engine, say)
	// instead of a transcript; the server closes the connection right
	// after.
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

// routeTelnyxSTTFrame classifies one server frame: emit reports whether
// the frame carries a transcript the pipeline should see, and err is set
// for error frames and undecodable messages. Frames with no transcript
// text, utterance-end markers, keepalives, come back with emit false so
// the caller skips them; emitting one would surface an empty final turn.
func routeTelnyxSTTFrame(msg []byte) (result TranscriptResult, emit bool, err error) {
	var f telnyxSTTFrame
	if jerr := json.Unmarshal(msg, &f); jerr != nil {
		return TranscriptResult{}, false, fmt.Errorf("telnyx stt frame decode: %w", jerr)
	}
	if len(f.Errors) > 0 {
		// The detail names the actual problem ("Unsupported
		// transcription_engine 'telnyx'. Supported engines: ..."),
		// which beats the code and title for an operator reading the
		// log. It is the first entry: the service reports one error per
		// frame.
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
