package stt

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/config"
)

// Volcengine (Doubao) streaming ASR — /api/v3/sauc/bigmodel
// --------------------------------------------------------
//
//	Endpoint:  wss://openspeech.bytedance.com/api/v3/sauc/bigmodel
//	Auth:      X-Api-Key: <api key>          (one header; the older
//	           X-Api-App-Key + X-Api-Access-Key pair is rejected by the
//	           seedasr resources with 400/401)
//	Resource:  X-Api-Resource-Id: volc.seedasr.sauc.duration   (hourly plan)
//	                              volc.seedasr.sauc.concurrent (concurrency plan)
//	Audio:     raw 16-bit signed little-endian PCM at 16kHz mono.
//
// Unlike the other providers here, the wire format is binary rather than JSON
// text frames. Every message is:
//
//	byte 0    protocol version (high nibble) and header size in 4-byte words
//	          (low nibble) — 0x11 throughout
//	byte 1    message type (high nibble), flags (low nibble)
//	byte 2    serialisation (high nibble), compression (low nibble)
//	byte 3    reserved
//	bytes 4-7 payload length, big-endian
//	bytes 8+  payload
//
// Responses carry a 4-byte sequence number between the header and the payload
// length, so their payload starts at byte 12 rather than byte 8.
type volcengineClient struct {
	conn   *websocket.Conn
	cancel context.CancelFunc

	// writeMu serialises writes: gorilla/websocket allows only one concurrent
	// writer, and Close races SendAudio when a call ends mid-utterance.
	writeMu sync.Mutex
	closed  bool
}

const (
	volcengineURL        = "wss://openspeech.bytedance.com/api/v3/sauc/bigmodel"
	volcengineSampleRate = 16000

	// Message types (high nibble of byte 1).
	volcTypeFullClient   = 0b0001 // config frame, JSON payload
	volcTypeAudioOnly    = 0b0010 // raw PCM payload
	volcTypeFullServer   = 0b1001 // transcript
	volcTypeErrorMessage = 0b1111

	// Flags (low nibble of byte 1).
	volcFlagNone        = 0b0000
	volcFlagLastPacket  = 0b0010
	volcSerialisationJS = 0x10 // JSON in the high nibble of byte 2
	volcCompressionGzip = 0b0001

	// defaultVolcengineEndWindowMs is deliberately longer than Deepgram's
	// 300ms endpointing: this service settles an utterance only in whole
	// phrases, so a short window buys no responsiveness and the pipeline's own
	// turn merging already covers mid-sentence pauses.
	defaultVolcengineEndWindowMs = 800

	// volcengineCloseGrace is how long Close waits for the flush triggered by
	// the last-packet frame before dropping the socket.
	volcengineCloseGrace = 2 * time.Second
)

// NewVolcengineClient dials the Volcengine streaming ASR endpoint and starts a
// goroutine that forwards transcripts to onResult.
func NewVolcengineClient(ctx context.Context, cfg config.VolcengineConfig, onResult func(TranscriptResult)) (Client, error) {
	sttCtx, cancel := context.WithCancel(ctx)

	resourceID := cfg.ResourceID
	if resourceID == "" {
		resourceID = "volc.seedasr.sauc.duration"
	}
	endpoint := cfg.URL
	if endpoint == "" {
		endpoint = volcengineURL
	}

	hdr := http.Header{}
	hdr.Set("X-Api-Key", cfg.APIKey)
	hdr.Set("X-Api-Resource-Id", resourceID)
	// Connect-Id is echoed in Volcengine's logs; sending one makes a failed
	// session traceable in the console rather than anonymous.
	hdr.Set("X-Api-Connect-Id", uuid.New().String())

	conn, resp, err := websocket.DefaultDialer.DialContext(sttCtx, endpoint, hdr)
	if err != nil {
		cancel()
		if resp != nil {
			// The body carries the actual reason ("requested grant not found"
			// for an unentitled resource id, for instance), which the status
			// code alone does not convey.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("volcengine dial: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("volcengine dial: %w", err)
	}

	c := &volcengineClient{conn: conn, cancel: cancel}

	if err := c.sendConfig(cfg); err != nil {
		c.Close()
		return nil, err
	}

	go c.readLoop(sttCtx, onResult)

	log.Printf("[stt] connected to Volcengine (%s)", resourceID)
	return c, nil
}

// volcengineRequest is the config frame sent once, before any audio.
type volcengineRequest struct {
	User    map[string]string `json:"user"`
	Audio   volcengineAudio   `json:"audio"`
	Request volcengineParams  `json:"request"`
}

type volcengineAudio struct {
	Format  string `json:"format"`
	Codec   string `json:"codec"`
	Rate    int    `json:"rate"`
	Bits    int    `json:"bits"`
	Channel int    `json:"channel"`
}

type volcengineParams struct {
	ModelName string `json:"model_name"`
	// EnableITN turns "one hundred" into "100"; EnablePUNC adds punctuation.
	// Both matter here because the transcript is fed to an LLM, and an
	// unpunctuated wall of text reads as one run-on sentence.
	EnableITN  bool `json:"enable_itn"`
	EnablePUNC bool `json:"enable_punc"`
	// ShowUtterances is what produces the `definite` flag the read loop uses
	// to tell a final transcript from a partial one. Without it every result
	// looks provisional and no turn ever completes.
	ShowUtterances bool `json:"show_utterances"`
	// EndWindowSize is the silence (ms) after which an utterance is settled,
	// the same role Deepgram's endpointing plays. It belongs at the top level
	// of `request`: the vendor samples nest it under a `vad` object, and
	// nested it is silently ignored — the service then waits out its own
	// default, which measured 5.3 seconds of silence on live audio and reads
	// on a call as the agent having hung up.
	//
	// Do not reach for `vad_segment_duration` to make this faster. It is not
	// an endpointing window but a hard segment length, and shortening it
	// chops the caller mid-thought. On one sentence of Mandarin:
	//
	//	end_window_size = 800        "你好，请用一句话介绍乒乓球。"
	//	vad_segment_duration = 300   "你好。" "请。" "用。" "一句话。" "介绍。" "乒乓球。"
	EndWindowSize int `json:"end_window_size"`
}

func (c *volcengineClient) sendConfig(cfg config.VolcengineConfig) error {
	model := cfg.Model
	if model == "" {
		model = "bigmodel"
	}
	endWindow := cfg.EndWindowMs
	if endWindow <= 0 {
		endWindow = defaultVolcengineEndWindowMs
	}
	body, err := json.Marshal(volcengineRequest{
		User: map[string]string{"uid": "streamcore"},
		Audio: volcengineAudio{
			Format: "pcm", Codec: "raw",
			Rate: volcengineSampleRate, Bits: 16, Channel: 1,
		},
		Request: volcengineParams{
			ModelName:      model,
			EnableITN:      true,
			EnablePUNC:     true,
			ShowUtterances: true,
			EndWindowSize:  endWindow,
		},
	})
	if err != nil {
		return fmt.Errorf("volcengine marshal config: %w", err)
	}
	return c.writeFrame(volcTypeFullClient, volcFlagNone, body)
}

// writeFrame prepends the 8-byte header and writes one binary message.
func (c *volcengineClient) writeFrame(msgType, flags byte, payload []byte) error {
	buf := make([]byte, 8+len(payload))
	buf[0] = 0x11
	buf[1] = msgType<<4 | flags
	buf[2] = volcSerialisationJS
	buf[3] = 0x00
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.closed {
		return fmt.Errorf("volcengine: connection closed")
	}
	return c.conn.WriteMessage(websocket.BinaryMessage, buf)
}

// SendAudio forwards a chunk of PCM. The pipeline calls this every 20ms.
func (c *volcengineClient) SendAudio(data []byte) error {
	return c.writeFrame(volcTypeAudioOnly, volcFlagNone, data)
}

// volcengineResponse is the transcript payload.
type volcengineResponse struct {
	Result struct {
		Text       string `json:"text"`
		Utterances []struct {
			Text string `json:"text"`
			// Definite marks an utterance the model considers settled; it is
			// the only signal separating a final from a partial.
			Definite bool `json:"definite"`
		} `json:"utterances"`
	} `json:"result"`
	Error string `json:"error"`
}

// readLoop turns the service's cumulative view into the incremental one the
// pipeline expects.
//
// Volcengine repeats the whole conversation on every message: result.text is
// everything heard so far, and utterances[] carries each segment with a
// `definite` flag that stays true once set. Deepgram's contract is the
// opposite — one final per utterance, emitted once. Bridging the two is what
// emittedFinals below tracks: anything past that index has newly settled and
// is emitted as its own final; everything still open goes out as a partial.
//
// Reading it the obvious way (any definite ⇒ the whole text is final) fires a
// final on every subsequent message, so the agent answers "你好。", then
// "你好。请", then "你好。请。用" — one reply per word.
func (c *volcengineClient) readLoop(ctx context.Context, onResult func(TranscriptResult)) {
	emittedFinals := 0

	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("[stt] volcengine read: %v", err)
			}
			return
		}

		msgType, payload, ok := parseVolcengineFrame(data)
		if !ok {
			continue
		}
		if msgType == volcTypeErrorMessage {
			log.Printf("[stt] volcengine error: %s", truncateBytes(payload, 200))
			continue
		}
		if msgType != volcTypeFullServer || len(payload) == 0 {
			continue
		}

		var parsed volcengineResponse
		if err := json.Unmarshal(payload, &parsed); err != nil {
			continue
		}
		if parsed.Error != "" {
			log.Printf("[stt] volcengine error: %s", parsed.Error)
			continue
		}

		// Emit any utterance that settled since the last message. They are
		// only ever appended, so an index is enough to track what has gone out.
		definite := 0
		for _, u := range parsed.Result.Utterances {
			if !u.Definite {
				break
			}
			definite++
		}
		for i := emittedFinals; i < definite; i++ {
			settled := strings.TrimSpace(parsed.Result.Utterances[i].Text)
			if settled == "" {
				continue
			}
			log.Printf("[stt] final: %q", settled)
			onResult(TranscriptResult{Text: settled, IsFinal: true})
		}
		emittedFinals = definite

		// The partial is the tail that has not settled — never result.text.
		//
		// result.text is the whole conversation so far, settled parts
		// included, so forwarding it re-sends as a partial what already went
		// out as a final. On screen the finished sentence appears a second
		// time, and every partial for the next sentence carries the previous
		// one glued to its front, which is what makes the transcript stutter
		// where Deepgram's flows.
		var pending strings.Builder
		for i := definite; i < len(parsed.Result.Utterances); i++ {
			pending.WriteString(parsed.Result.Utterances[i].Text)
		}
		text := strings.TrimSpace(pending.String())
		if text == "" && len(parsed.Result.Utterances) == 0 {
			// Before the first utterance is opened there is nothing to slice,
			// and result.text carries no settled text yet either.
			text = strings.TrimSpace(parsed.Result.Text)
		}
		if text == "" {
			continue
		}
		onResult(TranscriptResult{Text: text, IsFinal: false})
	}
}

// parseVolcengineFrame splits a server message into its type and payload,
// decompressing when the header says the payload is gzipped.
//
// Server frames carry a sequence number the client frames do not, so the
// payload starts 4 bytes later than the request format would suggest.
func parseVolcengineFrame(data []byte) (msgType byte, payload []byte, ok bool) {
	const serverHeaderLen = 12
	if len(data) < serverHeaderLen {
		return 0, nil, false
	}

	msgType = (data[1] >> 4) & 0x0F
	payload = data[serverHeaderLen:]

	if data[2]&0x0F == volcCompressionGzip {
		r, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return 0, nil, false
		}
		defer r.Close()
		decoded, err := io.ReadAll(r)
		if err != nil {
			return 0, nil, false
		}
		payload = decoded
	}
	return msgType, payload, true
}

// Close tells the service the stream ended and tears down the connection.
func (c *volcengineClient) Close() {
	c.writeMu.Lock()
	already := c.closed
	c.closed = true
	c.writeMu.Unlock()
	if already {
		return
	}

	// An empty last-packet frame flushes whatever the model is still holding.
	// Skipping it drops the tail of the final utterance.
	buf := make([]byte, 8)
	buf[0] = 0x11
	buf[1] = volcTypeAudioOnly<<4 | volcFlagLastPacket
	buf[2] = volcSerialisationJS
	c.writeMu.Lock()
	_ = c.conn.WriteMessage(websocket.BinaryMessage, buf)
	c.writeMu.Unlock()

	// Give the flush somewhere to land. Closing straight after the last-packet
	// frame tears the socket down before the response arrives, so the caller's
	// closing sentence is lost — the read loop just reports "use of closed
	// network connection" and the last thing said in the call never surfaces.
	// The deadline bounds the wait: an unresponsive service must not hold up
	// session teardown.
	_ = c.conn.SetReadDeadline(time.Now().Add(volcengineCloseGrace))
	go func() {
		time.Sleep(volcengineCloseGrace)
		_ = c.conn.Close()
		c.cancel()
	}()
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
