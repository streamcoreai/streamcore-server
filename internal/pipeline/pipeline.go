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

	// Optional hooks for audio bridging (e.g. mediasoup)
	onOutboundOpus func(opusPayload []byte) // called for each outbound Opus packet
	onInboundOpus  func(opusPayload []byte) // called for each inbound Opus packet

	// RTP outbound state
	rtpMu      sync.Mutex
	seqNum     uint16
	timestamp  uint32
	ssrc       uint32
	markerNext bool
}

// PipelineOptions holds optional configuration for the pipeline.
type PipelineOptions struct {
	OnOutboundOpus func(opusPayload []byte) // called for each outbound Opus packet
	OnInboundOpus  func(opusPayload []byte) // called for each inbound Opus packet
}

func New(
	ctx context.Context,
	cfg *config.Config,
	remoteTrack *webrtc.TrackRemote,
	localTrack *webrtc.TrackLocalStaticRTP,
	sendEvent func(interface{}) error,
	pluginMgr *plugin.Manager,
	direction string,
	opts *PipelineOptions,
) (*Pipeline, error) {
	var dec *audio.OpusDecoder
	var enc *audio.OpusEncoder
	var err error

	// Only create decoder/encoder when we have WebRTC tracks.
	if remoteTrack != nil {
		dec, err = audio.NewOpusDecoder()
		if err != nil {
			return nil, err
		}
	}
	if localTrack != nil {
		enc, err = audio.NewOpusEncoder()
		if err != nil {
			return nil, err
		}
	} else {
		// Mediasoup mode: still need encoder for onOutboundOpus
		enc, err = audio.NewOpusEncoder()
		if err != nil {
			return nil, err
		}
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

	if opts != nil {
		p.onOutboundOpus = opts.OnOutboundOpus
		p.onInboundOpus = opts.OnInboundOpus
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

// PushPCM feeds decoded PCM frames into the pipeline from an external source
// (e.g. mediasoup consumer). Use this when remoteTrack is nil.
func (p *Pipeline) PushPCM(samples []int16) {
	select {
	case p.inPCMCh <- PCMFrame{Samples: samples}:
	case <-p.ctx.Done():
	}
}

// Start launches all pipeline goroutines and blocks until the context is cancelled.
func (p *Pipeline) Start() {
	goroutines := 2 // inbound + agent always run
	var wg sync.WaitGroup

	if p.remoteTrack != nil {
		goroutines++
		wg.Add(1)
		go func() { defer wg.Done(); p.runReader() }()
	}

	wg.Add(2)
	go func() { defer wg.Done(); p.runInbound() }()
	go func() { defer wg.Done(); p.runAgent() }()

	wg.Add(1)
	go func() { defer wg.Done(); p.runSender() }()

	log.Printf("[pipeline] started — %d goroutines", goroutines+2)

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
