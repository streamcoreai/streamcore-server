package pipeline

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/plugin"
	"github.com/streamcoreai/server/internal/rag"
	"github.com/streamcoreai/server/internal/tools"
	"github.com/streamcoreai/server/internal/tts"
	"github.com/streamcoreai/server/internal/vad"
)

const (
	inPCMChSize      = 50 // ~1s of 20ms frames
	transcriptChSize = 10
	outPCMChSize     = 100 // ~2s of 20ms frames
)

// Pipeline is a channel-based streaming media pipeline.
//
// Goroutine architecture:
//
//	runReader   — RTP read → Opus decode → inPCMCh
//	runInbound  — inPCMCh → STT feed + VAD barge-in detection
//	runAgent    — transcriptCh → LLM → TTS → outPCMCh
//	runSender   — outPCMCh → Opus encode → RTP write
type Pipeline struct {
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *config.Config

	// Audio codec
	decoder *audio.OpusDecoder
	encoder *audio.OpusEncoder

	// WebRTC tracks
	remoteTrack *webrtc.TrackRemote
	localTrack  *webrtc.TrackLocalStaticRTP

	// Providers
	llmClient llm.Client
	ttsClient tts.Client
	ragClient rag.Client

	// Plugins
	pluginMgr *plugin.Manager

	// VAD
	vad        *vad.Detector
	bargeInVAD *vad.Detector

	// Bounded channels
	inPCMCh chan PCMFrame
	// finalCh carries raw STT finals to the turn buffer, which merges them
	// into whole turns before they reach transcriptCh.
	finalCh      chan TranscriptEvent
	transcriptCh chan TranscriptEvent
	outPCMCh     chan PCMFrame
	interruptCh  chan struct{}

	// DataChannel messaging
	sendEvent func(interface{}) error

	// Agent state
	speaking atomic.Bool
	// speechEndedAt is the wall-clock nanosecond at which speaking last went
	// true → false, always written via setSpeaking. STT endpointing delays a
	// final by several hundred ms, so an acknowledgement spoken over the
	// agent's closing words routinely arrives after the flag has cleared;
	// agentSpeechRecent uses this to still classify it as talking-over.
	speechEndedAt atomic.Int64

	// --- Turn quality state (rolling summary, misunderstanding, RAG prefetch) ---

	// transcriptLog is the running record of the conversation. The rolling
	// summary and the low-confidence heuristics read it; it is not the
	// LLM's own history.
	transcriptLog *TranscriptLog

	// rollingSummary holds the most recent background-generated digest of
	// older turns, so long calls keep their early context after the LLM
	// history window has dropped it.
	rollingSummary       atomic.Value // string
	lastSummaryAtEntries atomic.Int32
	summaryGenerating    atomic.Bool

	// misunderstandingStreak counts consecutive low-confidence caller turns,
	// driving an escalating "could you repeat that" rather than a guess.
	misunderstandingStreak atomic.Int32
	// userConfidence is the STT confidence of the most recent final turn.
	userConfidence atomic.Value // float64

	// ragDisabled short-circuits retrieval for the rest of the call after a
	// hard failure, so every turn does not re-pay the timeout.
	ragDisabled atomic.Bool
	// ragPrefetch holds a speculative retrieval started during the turn-merge
	// window, so embedding and vector search overlap the debounce.
	ragPrefetchMu sync.Mutex
	ragPrefetch   *ragPrefetchState

	// duckChangeCh signals the outbound sender to attenuate or restore agent
	// audio while the caller is talking over it.
	duckChangeCh chan bool
	audioMuted   atomic.Bool
	processing   atomic.Bool

	// responseGen identifies the response that currently owns the audio path.
	// It is bumped every time a response is superseded (new turn or barge-in),
	// so a cancelled response unwinding late can tell that speaking,
	// responseCancel, and the client-facing state now belong to its successor
	// and must not be cleared. Without this, a late unwind strands the live
	// response with no cancel func and it talks over the next one.
	responseGen       atomic.Uint64
	responseMu        sync.Mutex
	responseCancel    context.CancelFunc
	responseCancelGen uint64

	// Interruption tracking
	lastAgentText   atomic.Value // string — accumulates current response text
	interruptedText atomic.Value // string — what agent was saying when interrupted

	// Vision
	imageRecv *imageReceiver

	// Call metadata
	direction string // "outbound" for outgoing SIP calls, empty otherwise

	// RTP outbound state
	rtpMu      sync.Mutex
	seqNum     uint16
	timestamp  uint32
	ssrc       uint32
	markerNext bool
}

// New creates a pipeline wired to the given WebRTC tracks.
// Call Start() to launch the goroutine chain.
func New(
	ctx context.Context,
	cfg *config.Config,
	remoteTrack *webrtc.TrackRemote,
	localTrack *webrtc.TrackLocalStaticRTP,
	sendEvent func(interface{}) error,
	pluginMgr *plugin.Manager,
	ragClient rag.Client,
	direction string,
) (*Pipeline, error) {
	dec, err := audio.NewOpusDecoder()
	if err != nil {
		return nil, err
	}
	enc, err := audio.NewOpusEncoder()
	if err != nil {
		return nil, err
	}
	llmClient, err := llm.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	ttsClient, err := tts.NewClient(cfg)
	if err != nil {
		return nil, err
	}

	pCtx, cancel := context.WithCancel(ctx)

	imgRecv := newImageReceiver()

	p := &Pipeline{
		ctx:           pCtx,
		cancel:        cancel,
		cfg:           cfg,
		decoder:       dec,
		encoder:       enc,
		remoteTrack:   remoteTrack,
		localTrack:    localTrack,
		llmClient:     llmClient,
		ttsClient:     ttsClient,
		ragClient:     ragClient,
		pluginMgr:     pluginMgr,
		imageRecv:     imgRecv,
		vad:           vad.NewDefault(),
		bargeInVAD:    vad.NewBargeIn(),
		inPCMCh:       make(chan PCMFrame, inPCMChSize),
		finalCh:       make(chan TranscriptEvent, transcriptChSize),
		transcriptCh:  make(chan TranscriptEvent, transcriptChSize),
		outPCMCh:      make(chan PCMFrame, outPCMChSize),
		interruptCh:   make(chan struct{}, 1),
		duckChangeCh:  make(chan bool, 1),
		transcriptLog: &TranscriptLog{},
		sendEvent:     sendEvent,
		direction:     direction,
		ssrc:          12345678,
		markerNext:    true,
	}

	// Wire plugins into the LLM as function-calling tools.
	if pluginMgr != nil {
		tools := pluginMgr.Tools()
		if len(tools) > 0 {
			defs := make([]llm.ToolDefinition, 0, len(tools))
			for _, t := range tools {
				defs = append(defs, llm.ToolDefinition{
					Name:        t.Name(),
					Description: t.Description(),
					Parameters:  t.Parameters(),
				})
			}
			llmClient.SetTools(defs)
			llmClient.SetToolHandler(func(callCtx context.Context, call llm.ToolCall) (string, error) {
				// Intercept vision.analyze: capture image first, inject into params.
				if call.Name == visionToolName {
					return p.handleVisionToolCall(call)
				}
				// Intercept car.* — translate to a data-channel command for the
				// firmware's motor controller. No subprocess plugin involved.
				if strings.HasPrefix(call.Name, "car.") {
					return p.handleCarToolCall(call)
				}
				tool, ok := pluginMgr.GetTool(call.Name)
				if !ok {
					return "", fmt.Errorf("unknown tool: %s", call.Name)
				}

				// Play a soft thinking tone while the tool runs (opt-in via plugin.yaml).
				if tool.ThinkingSound() {
					done := make(chan struct{})
					go p.playThinkingSound(done)
					result, err := tool.Execute(call.Arguments)
					close(done)
					if err == nil {
						p.playSentSound()
					}
					return result, err
				}

				return tool.Execute(call.Arguments)
			})
			log.Printf("[pipeline] registered %d tools with LLM", len(defs))
		}

		// Append skill instructions to system prompt.
		skillsPrompt := pluginMgr.SkillsPrompt()
		if skillsPrompt != "" {
			llmClient.AppendSystemPrompt(skillsPrompt)
			log.Printf("[pipeline] injected %d skills into system prompt", len(pluginMgr.Skills()))
		}
	}

	// Teach the model the optional delivery tags. Without this rule the tag
	// vocabulary is never emitted and ParseVoiceTag simply never fires, so
	// voice controls stay at provider defaults.
	llmClient.AppendSystemPrompt(
		"\n\nOPTIONAL tone control: you may start a sentence with exactly one tag from " +
			"[warm] [empathetic] [calm] [excited] to shape how it is spoken — e.g. " +
			"\"[empathetic] I'm really sorry to hear that.\" Use it sparingly, only when the " +
			"moment calls for it (apologies, bad news, congratulations). The tag is never " +
			"spoken aloud. Never invent other tags.",
	)

	// Initialize atomic values with empty strings for type consistency.
	p.lastAgentText.Store("")
	p.interruptedText.Store("")

	return p, nil
}

// Start launches all pipeline goroutines and blocks until the context is cancelled.
func (p *Pipeline) Start() {
	var wg sync.WaitGroup
	wg.Add(4)

	go func() { defer wg.Done(); p.runReader() }()
	go func() { defer wg.Done(); p.runInbound() }()
	go func() { defer wg.Done(); p.runAgent() }()
	go func() { defer wg.Done(); p.runSender() }()

	log.Println("[pipeline] started — reader, inbound, agent, sender")

	// Send initial greeting if configured.
	if g := p.greetingText(); g != "" {
		go p.greet(g)
	}

	wg.Wait()
	log.Println("[pipeline] stopped")
}

func (p *Pipeline) HandleDataChannelMessage(msg string) {
	if p.imageRecv.handleMessage(msg) {
		return
	}
}

// dcDataPacket is the topic-addressed envelope the firmware's SDK
// dispatches via its `on_data` callback. Payload is base64-encoded JSON.
type dcDataPacket struct {
	Type    string `json:"type"`    // always "data"
	Topic   string `json:"topic"`   // e.g. "car.command"
	Payload string `json:"payload"` // base64-encoded JSON
}

// carCommandPayload is the JSON the firmware decodes inside the data
// packet for a "car.command" topic.
type carCommandPayload struct {
	Action       string `json:"action"`
	DurationMs   uint32 `json:"duration_ms,omitempty"`
	SpeedPercent uint8  `json:"speed_percent,omitempty"`
}

// handleCarToolCall turns a "car.*" LLM tool invocation into a topic-
// addressed data-channel packet that the firmware's `on_data` handler
// will dispatch to its MotorController. Returns a short spoken-friendly
// confirmation for the LLM to read back.
func (p *Pipeline) handleCarToolCall(call llm.ToolCall) (string, error) {
	action := strings.TrimPrefix(call.Name, "car.")

	var args struct {
		DurationMs   *uint32 `json:"duration_ms,omitempty"`
		SpeedPercent *uint8  `json:"speed_percent,omitempty"`
	}
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}

	payload := carCommandPayload{Action: action}
	if args.DurationMs != nil {
		payload.DurationMs = clampU32(*args.DurationMs, 0, 10000)
	}
	if args.SpeedPercent != nil {
		payload.SpeedPercent = clampU8(*args.SpeedPercent, 0, 100)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal car payload: %w", err)
	}

	if err := p.sendEvent(dcDataPacket{
		Type:    "data",
		Topic:   tools.CarCommandTopic,
		Payload: base64.StdEncoding.EncodeToString(body),
	}); err != nil {
		return "", fmt.Errorf("send car command: %w", err)
	}

	log.Printf("[car] dispatched action=%s duration_ms=%d speed=%d%%",
		payload.Action, payload.DurationMs, payload.SpeedPercent)

	return carAck(payload), nil
}

func carAck(p carCommandPayload) string {
	switch p.Action {
	case "stop":
		return "Stopping."
	case "fancy":
		return "Fancy moves!"
	case "shake":
		return "Shaking."
	case "forward":
		return "Driving forward."
	case "backward":
		return "Backing up."
	case "turn_left":
		return "Turning left."
	case "turn_right":
		return "Turning right."
	default:
		return "OK, " + strings.ReplaceAll(p.Action, "_", " ") + "."
	}
}

func clampU32(v, lo, hi uint32) uint32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func clampU8(v, lo, hi uint8) uint8 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// handleVisionToolCall intercepts the vision.analyze tool call, captures an
// image from the ESP32 via data channel, and forwards the enriched params to
// the TypeScript plugin.
func (p *Pipeline) handleVisionToolCall(call llm.ToolCall) (string, error) {
	log.Println("[vision] intercepting vision.analyze — requesting image from client")

	res, err := p.imageRecv.requestAndWait(p.sendEvent)
	if err != nil {
		return fmt.Sprintf("Error capturing image: %v. Ask the user to try again.", err), nil
	}

	// Parse the original LLM arguments and inject the image.
	var params map[string]interface{}
	if err := json.Unmarshal(call.Arguments, &params); err != nil {
		params = make(map[string]interface{})
	}
	params["image_base64"] = res.Base64
	if res.Mime != "" {
		params["image_mime"] = res.Mime
	}

	enriched, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshal enriched params: %w", err)
	}

	tool, ok := p.pluginMgr.GetTool(visionToolName)
	if !ok {
		return "", fmt.Errorf("vision plugin %q not registered", visionToolName)
	}

	log.Printf("[vision] forwarding to plugin with %d bytes of base64", len(res.Base64))
	return tool.Execute(enriched)
}

// Stop cancels the pipeline context, tearing down all goroutines.
func (p *Pipeline) Stop() {
	p.cancel()
}
