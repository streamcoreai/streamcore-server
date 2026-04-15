package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/plugin"
	"github.com/streamcoreai/server/internal/rag"
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
	inPCMCh      chan PCMFrame
	transcriptCh chan TranscriptEvent
	outPCMCh     chan PCMFrame
	interruptCh  chan struct{}

	// DataChannel messaging
	sendEvent func(interface{}) error

	// Agent state
	speaking       atomic.Bool
	responseMu     sync.Mutex
	responseCancel context.CancelFunc

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
		ctx:          pCtx,
		cancel:       cancel,
		cfg:          cfg,
		decoder:      dec,
		encoder:      enc,
		remoteTrack:  remoteTrack,
		localTrack:   localTrack,
		llmClient:    llmClient,
		ttsClient:    ttsClient,
		ragClient:    ragClient,
		pluginMgr:    pluginMgr,
		imageRecv:    imgRecv,
		vad:          vad.NewDefault(),
		bargeInVAD:   vad.NewBargeIn(),
		inPCMCh:      make(chan PCMFrame, inPCMChSize),
		transcriptCh: make(chan TranscriptEvent, transcriptChSize),
		outPCMCh:     make(chan PCMFrame, outPCMChSize),
		interruptCh:  make(chan struct{}, 1),
		sendEvent:    sendEvent,
		direction:    direction,
		ssrc:         12345678,
		markerNext:   true,
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
