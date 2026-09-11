package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/llm"
	"github.com/streamcoreai/streamcore-server/internal/plugin"
	"github.com/streamcoreai/streamcore-server/internal/tts"
)

type lifecycleProbe struct {
	typ    string
	turnID string
	seq    uint64
}

func (e lifecycleProbe) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":     e.typ,
		"turn_id":  e.turnID,
		"turn_seq": e.seq,
	})
}

type lifecycleHandler struct {
	events chan plugin.PluginEvent
	output func(plugin.PluginEvent) ([]plugin.OutboundEvent, error)
}

func (h *lifecycleHandler) Events() []string {
	return []string{plugin.AssistantResponseCompletedEvent}
}

func (h *lifecycleHandler) HandleEvent(_ context.Context, event plugin.PluginEvent) ([]plugin.OutboundEvent, error) {
	if h.events != nil {
		h.events <- event
	}
	if h.output == nil {
		return nil, nil
	}
	return h.output(event)
}

func newLifecyclePipeline(t *testing.T, handler plugin.EventHandler, sent chan lifecycleProbe) (*Pipeline, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	manager := plugin.NewManager("")
	if handler != nil {
		manager.RegisterEventHandler("lifecycle-test", handler)
	}
	p := &Pipeline{
		ctx:           ctx,
		cancel:        cancel,
		cfg:           &config.Config{},
		pluginMgr:     manager,
		outPCMCh:      make(chan PCMFrame, outPCMChSize),
		realtimeAudio: newRealtimeAudioQueue(),
		conv: &ConversationState{
			SessionID:            "session-123",
			Log:                  &TranscriptLog{},
			rollingSummary:       &atomic.Value{},
			lastSummaryAtEntries: &atomic.Int32{},
		},
		sendEvent: func(msg any) error {
			event, ok := msg.(lifecycleProbe)
			if ok && sent != nil {
				sent <- event
			}
			return nil
		},
	}
	p.rollingSummary = p.conv.rollingSummary
	p.lastSummaryAtEntries = p.conv.lastSummaryAtEntries
	p.transcriptLog = p.conv.Log
	p.lastAgentText.Store("")
	p.interruptedText.Store("")
	return p, cancel
}

func waitLifecycle(t *testing.T, channel chan plugin.PluginEvent) plugin.PluginEvent {
	t.Helper()
	select {
	case event := <-channel:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle event was not dispatched")
		return plugin.PluginEvent{}
	}
}

func TestAssistantResponseCompletedEmittedOnceWithSessionRouting(t *testing.T) {
	received := make(chan plugin.PluginEvent, 1)
	sent := make(chan lifecycleProbe, 1)
	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{
		events: received,
		output: func(event plugin.PluginEvent) ([]plugin.OutboundEvent, error) {
			return []plugin.OutboundEvent{{
				Type:    "probe.event",
				TurnID:  event.TurnID,
				TurnSeq: event.TurnSeq,
				Payload: lifecycleProbe{typ: "probe.event", turnID: event.TurnID, seq: event.TurnSeq},
			}}, nil
		},
	}, sent)
	defer cancel()

	p.responseGen.Store(1)
	p.emitAssistantResponseCompleted(1, "what is the capital?", "Paris.")
	event := waitLifecycle(t, received)

	if event.Type != plugin.AssistantResponseCompletedEvent {
		t.Fatalf("event type = %q", event.Type)
	}
	if event.SessionID != "session-123" || event.TurnID != "turn_1" || event.TurnSeq != 1 {
		t.Fatalf("event = %+v", event)
	}
	payload, ok := event.Data.(plugin.AssistantResponseCompleted)
	if !ok {
		t.Fatalf("payload type = %T", event.Data)
	}
	if payload.Transcript != "what is the capital?" || payload.Response != "Paris." || payload.Interrupted {
		t.Fatalf("payload = %+v", payload)
	}

	select {
	case output := <-sent:
		if output.turnID != "turn_1" || output.seq != 1 {
			t.Fatalf("routed output = %+v", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plugin output was not sent to the session DataChannel")
	}

	p.emitAssistantResponseCompleted(1, "what is the capital?", "Paris.")
	select {
	case event := <-received:
		t.Fatalf("completion emitted twice: %+v", event)
	default:
	}
}

func TestSupersededResponseDoesNotEmitCompletion(t *testing.T) {
	received := make(chan plugin.PluginEvent, 1)
	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{events: received}, nil)
	defer cancel()

	p.responseGen.Store(2)
	p.emitAssistantResponseCompleted(1, "old question", "interrupted partial answer")
	select {
	case event := <-received:
		t.Fatalf("interrupted response emitted %+v", event)
	default:
	}
}

func TestRealtimeCompletedResponseEmitsLifecycleEvent(t *testing.T) {
	received := make(chan plugin.PluginEvent, 1)
	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{events: received}, nil)
	defer cancel()
	p.realtimeUserText.Store("")

	handlers := p.realtimeHandlers()
	handlers.OnUserTranscript("What is the capital of France?", true)
	handlers.OnResponseStarted()
	handlers.OnAgentTranscript("Paris.")
	handlers.OnResponseDone()

	event := waitLifecycle(t, received)
	payload, ok := event.Data.(plugin.AssistantResponseCompleted)
	if !ok {
		t.Fatalf("payload type = %T", event.Data)
	}
	if payload.Transcript != "What is the capital of France?" || payload.Response != "Paris." {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestRealtimeBargeInSuppressesLifecycleEvent(t *testing.T) {
	received := make(chan plugin.PluginEvent, 1)
	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{events: received}, nil)
	defer cancel()
	p.realtimeUserText.Store("")

	handlers := p.realtimeHandlers()
	handlers.OnUserTranscript("Actually Australia", true)
	handlers.OnResponseStarted()
	handlers.OnAgentTranscript("New Zealand's population is approximately")
	handlers.OnSpeechStarted()
	handlers.OnResponseDone()

	select {
	case event := <-received:
		t.Fatalf("interrupted realtime response emitted %+v", event)
	default:
	}
}

func TestOlderTurnOutputCannotOverwriteNewerTurn(t *testing.T) {
	received := make(chan plugin.PluginEvent, 4)
	sent := make(chan lifecycleProbe, 4)
	started := make(chan struct{})
	release := make(chan struct{})

	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{
		events: received,
		output: func(event plugin.PluginEvent) ([]plugin.OutboundEvent, error) {
			if event.TurnSeq == 1 {
				close(started)
				<-release
			}
			return []plugin.OutboundEvent{{
				Type:    "probe.event",
				TurnID:  event.TurnID,
				TurnSeq: event.TurnSeq,
				Payload: lifecycleProbe{typ: "probe.event", turnID: event.TurnID, seq: event.TurnSeq},
			}}, nil
		},
	}, sent)
	defer cancel()

	p.responseGen.Store(1)
	p.emitAssistantResponseCompleted(1, "first question", "first complete answer")
	waitLifecycle(t, received)
	<-started

	p.responseGen.Store(2)
	p.emitAssistantResponseCompleted(2, "second question", "second complete answer")
	second := waitLifecycle(t, received)
	select {
	case output := <-sent:
		if output.seq != 2 {
			t.Fatalf("newer card was not sent first: %+v", output)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("newer card was not sent")
	}

	close(release)
	select {
	case output := <-sent:
		t.Fatalf("old turn %d overwrote turn %d", output.seq, second.TurnSeq)
	case <-time.After(200 * time.Millisecond):
	}
}

type fakeChatLLM struct{}

func (fakeChatLLM) Chat(_ context.Context, _ llm.Turn, onChunk func(string), onSentence func(string)) (string, error) {
	onChunk("Paris.")
	onSentence("Paris.")
	return "Paris.", nil
}
func (fakeChatLLM) OneShot(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("unexpected OneShot call")
}
func (fakeChatLLM) SetTools([]llm.ToolDefinition)                                      {}
func (fakeChatLLM) SetToolHandler(func(context.Context, llm.ToolCall) (string, error)) {}
func (fakeChatLLM) AppendSystemPrompt(string)                                          {}
func (fakeChatLLM) Reset()                                                             {}

type fakeTTS struct {
	called chan struct{}
}

func (f *fakeTTS) Synthesize(context.Context, string) ([]byte, error) { return nil, nil }
func (f *fakeTTS) SynthesizeStream(context.Context, string) (<-chan tts.StreamChunk, error) {
	if f.called != nil {
		f.called <- struct{}{}
	}
	channel := make(chan tts.StreamChunk)
	close(channel)
	return channel, nil
}

func TestProjectionFailureOrLatencyDoesNotBlockVoiceResponse(t *testing.T) {
	projectorStarted := make(chan struct{})
	release := make(chan struct{})
	ttsCalled := make(chan struct{}, 1)

	p, cancel := newLifecyclePipeline(t, &lifecycleHandler{
		output: func(event plugin.PluginEvent) ([]plugin.OutboundEvent, error) {
			close(projectorStarted)
			<-release
			return nil, fmt.Errorf("projection failed")
		},
	}, nil)
	defer cancel()
	p.llmClient = fakeChatLLM{}
	p.ttsClient = &fakeTTS{called: ttsCalled}

	gen := p.supersedeResponse()
	_, responseCancel := context.WithCancel(context.Background())
	if !p.registerResponse(gen, responseCancel) {
		t.Fatal("test response could not claim the audio path")
	}

	respondDone := make(chan struct{})
	go func() {
		defer close(respondDone)
		p.respond(gen, "What is the capital of France?", time.Time{})
	}()

	select {
	case <-ttsCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("TTS did not start while projection was blocked")
	}
	select {
	case <-respondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("respond blocked on lifecycle projection")
	}

	<-projectorStarted
	if p.ctx.Err() != nil {
		t.Fatal("voice pipeline was cancelled by a projection failure path")
	}
	close(release)
}
