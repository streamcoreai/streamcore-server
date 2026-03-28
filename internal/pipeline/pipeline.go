package pipeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
	"github.com/streamcoreai/server/internal/audio"
	"github.com/streamcoreai/server/internal/config"
	"github.com/streamcoreai/server/internal/llm"
	"github.com/streamcoreai/server/internal/plugin"
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

	// Plugins
	pluginMgr *plugin.Manager

	// VAD
	vad *vad.Detector

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
		pluginMgr:    pluginMgr,
		vad:          vad.NewDefault(),
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
				tool, ok := pluginMgr.GetTool(call.Name)
				if !ok {
					return "", fmt.Errorf("unknown tool: %s", call.Name)
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

// Stop cancels the pipeline context, tearing down all goroutines.
func (p *Pipeline) Stop() {
	p.cancel()
}
