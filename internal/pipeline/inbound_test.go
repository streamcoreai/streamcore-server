package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/streamcoreai/streamcore-server/internal/audio"
	"github.com/streamcoreai/streamcore-server/internal/config"
	"github.com/streamcoreai/streamcore-server/internal/vad"
)

// Hermetic coverage for runInbound's barge-in gate (issue #75).
//
// A provider that streams partials opens the barge-in window only once
// STT has confirmed real speech with partial text; a finals-only provider
// (stt.PartialsEmitter returning false) opens it on VAD alone but cannot
// fire the interrupt until the 600ms backchannel window has fully
// elapsed, and a burst that ends inside the window is suppressed as
// backchannel — with no text, it cannot be told apart from "mm-hm".
//
// runInbound is driven directly with a hand-built Pipeline because the
// full constructor demands WebRTC tracks and live provider credentials.
// The STT client still comes from stt.NewClient, so the type assertion
// runs on the real provider values: a fake VibeVoice WebSocket sidecar
// plays the plain provider (no PartialsEmitter), and the OpenAI client —
// which dials nothing at construction and only flushes after 600ms of
// silence, a state these tests never reach before their context is
// cancelled — plays the finals-only one.

// fakeVibeVoiceASR stands in for the local VibeVoice ASR sidecar: it
// accepts the client's WebSocket, optionally sends one partial
// transcript, and discards the audio the pipeline streams at it.
func fakeVibeVoiceASR(t *testing.T, sendPartial bool) *httptest.Server {
	t.Helper()
	if !sendPartial {
		return fakeVibeVoiceASRSaying(t, "")
	}
	return fakeVibeVoiceASRSaying(t, "the billing question please")
}

// fakeVibeVoiceASRSaying sends partial as the one partial transcript, or
// nothing when it is empty.
func fakeVibeVoiceASRSaying(t *testing.T, partial string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if partial != "" {
			_ = conn.WriteMessage(websocket.TextMessage,
				[]byte(`{"text":"`+partial+`","is_final":false}`))
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// newInboundTestPipeline builds just enough of a Pipeline to run
// runInbound: frames arrive on inPCMCh, a confirmed barge-in lands on
// interruptCh, and the agent is marked speaking — the other half of the
// barge-in trigger. The barge-in VAD is the real detector so the 40ms
// onset and 300ms offset timings are the production ones.
func newInboundTestPipeline(t *testing.T, cfg *config.Config) *Pipeline {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &Pipeline{
		ctx:          ctx,
		cancel:       cancel,
		cfg:          cfg,
		vad:          vad.NewDefault(),
		bargeInVAD:   vad.NewBargeIn(),
		inPCMCh:      make(chan PCMFrame, 50),
		finalCh:      make(chan TranscriptEvent, 10),
		transcriptCh: make(chan TranscriptEvent, 10),
		interruptCh:  make(chan struct{}, 1),
	}
	p.speaking.Store(true)
	return p
}

// startInbound runs runInbound in the background and tears it down at the
// end of the test, so a hung inbound loop fails the test instead of
// hanging it.
func startInbound(t *testing.T, p *Pipeline) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.runInbound()
	}()
	t.Cleanup(func() {
		p.cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runInbound did not stop within 5s of cancel")
		}
	})
}

// pushPaced feeds one frame every pace. The backchannel window is
// wall-clock time, so an unpaced burst would never let the 600ms window
// elapse no matter how many frames cross.
func pushPaced(t *testing.T, p *Pipeline, samples []int16, count int, pace time.Duration) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case p.inPCMCh <- PCMFrame{Samples: samples}:
		case <-time.After(5 * time.Second):
			t.Fatalf("inbound stalled after %d frames — did the STT client start?", i)
		}
		time.Sleep(pace)
	}
}

// pushUntilInterrupt keeps feeding frames until a barge-in lands on
// interruptCh.
func pushUntilInterrupt(t *testing.T, p *Pipeline, samples []int16, pace time.Duration) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case p.inPCMCh <- PCMFrame{Samples: samples}:
		case <-deadline:
			t.Fatal("timed out waiting for the barge-in interrupt")
		}
		select {
		case <-p.interruptCh:
			return
		default:
		}
		select {
		case <-time.After(pace):
		case <-deadline:
			t.Fatal("timed out waiting for the barge-in interrupt")
		}
	}
}

// loudSamples is one 20ms frame of near-full-scale PCM: 20000 RMS sits
// far above the barge-in VAD's 1200 base threshold. silentSamples is the
// same frame at zero.
func loudSamples() []int16 {
	s := make([]int16, audio.FrameSize)
	for i := range s {
		s[i] = 20000
	}
	return s
}

func silentSamples() []int16 {
	return make([]int16, audio.FrameSize)
}

func interruptFired(p *Pipeline) bool {
	select {
	case <-p.interruptCh:
		return true
	default:
		return false
	}
}

// A provider that does not implement stt.PartialsEmitter keeps the
// current path: sustained loud speech with no partial text never opens
// the barge-in window, because hasPartialText still gates the trigger.
func TestInboundBargeInWaitsForPartialTextByDefault(t *testing.T) {
	server := fakeVibeVoiceASR(t, false)

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	// 700ms of loud speech — past the 600ms window — with STT never
	// reporting a word.
	pushPaced(t, p, loudSamples(), 70, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	if interruptFired(p) {
		t.Error("barge-in fired without partial text, want the partials gate to hold for a provider that does not implement PartialsEmitter")
	}
}

// With partials flowing, sustained speech over the agent opens the window
// and the interrupt fires once the speech has outlasted the backchannel
// window — the behaviour every partials provider already had.
func TestInboundBargeInFiresOnSustainedSpeechWithPartials(t *testing.T) {
	server := fakeVibeVoiceASR(t, true)

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
}

// "stop" cannot be a backchannel, so it cuts the agent off as soon as the
// partial shows it instead of after the 600ms window.
func TestInboundBargeInOnCommandSkipsBackchannelWindow(t *testing.T) {
	server := fakeVibeVoiceASRSaying(t, "stop")

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	// The partial lands on connect; give it a moment so the window opens
	// with the text already there, as it does when the caller says "stop".
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
	if took := time.Since(start); took >= backchannelWindowForTest {
		t.Errorf("command barge-in took %v, want it inside the %v backchannel window", took, backchannelWindowForTest)
	}
}

// A redirect with content ("actually, tell me about cats") is an
// interruption as soon as the partial shows it.
func TestInboundBargeInOnContentSkipsBackchannelWindow(t *testing.T) {
	server := fakeVibeVoiceASRSaying(t, "actually tell me about cats instead")

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
	if took := time.Since(start); took >= backchannelWindowForTest {
		t.Errorf("content barge-in took %v, want it inside the %v backchannel window", took, backchannelWindowForTest)
	}
}

// A backchannel never reads as meaningful, so it still has to outlast the
// window before it interrupts.
func TestInboundBackchannelStillWaitsOutWindow(t *testing.T) {
	server := fakeVibeVoiceASRSaying(t, "mm hmm")

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	time.Sleep(100 * time.Millisecond)
	// 400ms of speech: long enough to open the window, short of closing it.
	pushPaced(t, p, loudSamples(), 40, 10*time.Millisecond)
	if interruptFired(p) {
		t.Error("a backchannel partial fired barge-in inside the window")
	}
}

// backchannelWindowForTest mirrors the window in runInbound.
const backchannelWindowForTest = 600 * time.Millisecond

// A finals-only provider opens the window on VAD alone, but the interrupt
// must not fire before the full 600ms backchannel window has elapsed:
// there is no text to classify a short burst with, so the window is the
// only thing standing between "mm-hm" and a cut-off agent.
func TestInboundVADOnlyBargeInWaitsOutBackchannelWindow(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "openai"
	cfg.OpenAI.APIKey = "test-key"
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	// ~250ms of loud speech: the candidate is open, the window (600ms)
	// has not run out — nothing may fire yet.
	pushPaced(t, p, loudSamples(), 25, 10*time.Millisecond)
	if interruptFired(p) {
		t.Fatal("VAD-only barge-in fired inside the 600ms backchannel window")
	}

	// Keep talking past the window: the interrupt must fire now.
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
}

// A short burst over the agent, ending inside the window, is suppressed:
// without partial text there is no way to tell a real interruption from a
// backchannel, and the degraded mode must stay quiet rather than guess.
func TestInboundVADOnlyBargeInSuppressesShortBurst(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "openai"
	cfg.OpenAI.APIKey = "test-key"
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	// ~120ms of speech, then silence: the VAD's 300ms offset ends the
	// speech inside the window and the burst must classify as
	// backchannel.
	pushPaced(t, p, loudSamples(), 12, 10*time.Millisecond)
	pushPaced(t, p, silentSamples(), 20, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	if interruptFired(p) {
		t.Error("short unclassifiable burst fired barge-in, want it suppressed as backchannel")
	}
}

// The end-of-speech stamp is the last voiced frame, with the VAD's hangover
// backed out, and it is kept with barge-in off.
func TestInboundStampsCallerSpeechEnd(t *testing.T) {
	cfg := &config.Config{}
	cfg.STT.Provider = "openai"
	cfg.OpenAI.APIKey = "test-key"
	off := false
	cfg.Pipeline.BargeIn = &off

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	pushPaced(t, p, loudSamples(), 15, 10*time.Millisecond)
	lastLoud := time.Now()
	pushPaced(t, p, silentSamples(), 25, 10*time.Millisecond)
	time.Sleep(30 * time.Millisecond)

	at := p.callerVoiceAt.Load()
	if at == 0 {
		t.Fatal("no caller speech recorded")
	}
	// Frames are paced at 10ms while the hangover assumes 20ms, so the
	// stamp lands before the real last frame; it must not land after it.
	if end := time.Unix(0, at); end.After(lastLoud.Add(20 * time.Millisecond)) {
		t.Errorf("speech end stamped %v after the last voiced frame", end.Sub(lastLoud))
	}
}

func TestWaitCallerQuiet(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipeline.UserSpeechQuietMs = 120
	p := &Pipeline{cfg: cfg}

	if got := p.waitCallerQuiet(context.Background()); got > 20*time.Millisecond {
		t.Errorf("caller never spoke, gate held %v", got)
	}

	p.callerVoiceAt.Store(time.Now().UnixNano())
	if got := p.waitCallerQuiet(context.Background()); got < 100*time.Millisecond || got > 300*time.Millisecond {
		t.Errorf("caller just spoke, gate held %v, want about 120ms", got)
	}

	p.callerVoiceAt.Store(time.Now().Add(-time.Second).UnixNano())
	if got := p.waitCallerQuiet(context.Background()); got > 20*time.Millisecond {
		t.Errorf("caller quiet for 1s, gate held %v", got)
	}

	// A caller the VAD never hears stop cannot hold the agent forever.
	ctx, cancel := context.WithCancel(context.Background())
	stop := make(chan struct{})
	p.callerVoiceAt.Store(time.Now().UnixNano())
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				p.callerVoiceAt.Store(time.Now().UnixNano())
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()
	got := p.waitCallerQuiet(ctx)
	close(stop)
	cancel()
	if got < callerQuietMaxWait || got > callerQuietMaxWait+200*time.Millisecond {
		t.Errorf("endless speech held the gate %v, want the %v cap", got, callerQuietMaxWait)
	}

	p.callerVoiceAt.Store(time.Now().UnixNano())
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if got := p.waitCallerQuiet(ctx); got > 50*time.Millisecond {
		t.Errorf("cancelled gate held %v", got)
	}
}

// A caller who spoke again after their turn was committed is mid-sentence:
// the reply waits for their next final (which supersedes it) rather than the
// ordinary short quiet period.
func TestWaitCallerQuietHoldsForContinuation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipeline.UserSpeechQuietMs = 120
	p := &Pipeline{cfg: cfg}

	p.turnCommittedAt.Store(time.Now().Add(-time.Second).UnixNano())
	p.callerVoiceAt.Store(time.Now().Add(-200 * time.Millisecond).UnixNano())
	if got := p.waitCallerQuiet(context.Background()); got < callerContinuedQuiet-300*time.Millisecond {
		t.Errorf("caller continued after the commit, gate held only %v", got)
	}

	// Superseded by the continuation's final: the gate lets go at once.
	p.callerVoiceAt.Store(time.Now().UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	if got := p.waitCallerQuiet(ctx); got > 400*time.Millisecond {
		t.Errorf("superseded reply still held %v", got)
	}

	// Voice that ended before the commit is the turn itself, not more of it.
	p.turnCommittedAt.Store(time.Now().UnixNano())
	p.callerVoiceAt.Store(time.Now().Add(-time.Second).UnixNano())
	if got := p.waitCallerQuiet(context.Background()); got > 20*time.Millisecond {
		t.Errorf("finished turn held %v", got)
	}
}

// An STT connection that drops mid-call is replaced, so the caller is still
// heard afterwards.
func TestInboundReconnectsDroppedSTT(t *testing.T) {
	var conns atomic.Int32
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if conns.Add(1) == 1 {
			return // the provider drops the first connection straight away
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	off := false
	cfg.Pipeline.BargeIn = &off

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)

	deadline := time.Now().Add(5 * time.Second)
	for conns.Load() < 2 && time.Now().Before(deadline) {
		select {
		case p.inPCMCh <- PCMFrame{Samples: silentSamples()}:
		case <-time.After(100 * time.Millisecond):
		}
		time.Sleep(10 * time.Millisecond)
	}
	if conns.Load() < 2 {
		t.Fatalf("STT connections = %d, want a reconnect after the drop", conns.Load())
	}
}

// The check made just before the first frame plays only ever holds for a
// continuation, so a noisy line does not pay the quiet wait twice.
func TestFirstFrameCheckOnlyHoldsForContinuation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Pipeline.UserSpeechQuietMs = 600
	p := &Pipeline{cfg: cfg}

	p.turnCommittedAt.Store(time.Now().UnixNano())
	p.callerVoiceAt.Store(time.Now().Add(-50 * time.Millisecond).UnixNano())
	if got := p.waitCallerQuietUpTo(context.Background(), callerContinuedFrameMax, false); got > 20*time.Millisecond {
		t.Errorf("no continuation, second check held %v", got)
	}

	p.turnCommittedAt.Store(time.Now().Add(-time.Second).UnixNano())
	p.callerVoiceAt.Store(time.Now().UnixNano())
	if got := p.waitCallerQuietUpTo(context.Background(), 300*time.Millisecond, false); got < 280*time.Millisecond {
		t.Errorf("continuation, second check held only %v", got)
	}
}

// With short endpointing "stop" can arrive as a final before any partial
// does. It still cuts the agent off inside the window.
func TestInboundBargeInOnFinalCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(150 * time.Millisecond)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"text":"stop","is_final":true}`))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.STT.Provider = "vibevoice"
	cfg.VibeVoice.ASRURL = "ws" + strings.TrimPrefix(server.URL, "http")
	bar := true
	cfg.Pipeline.BargeIn = &bar

	p := newInboundTestPipeline(t, cfg)
	startInbound(t, p)
	go func() {
		for {
			select {
			case <-p.finalCh: // the turn buffer is not running here
			case <-p.ctx.Done():
				return
			}
		}
	}()

	start := time.Now()
	pushUntilInterrupt(t, p, loudSamples(), 10*time.Millisecond)
	if took := time.Since(start); took >= backchannelWindowForTest {
		t.Errorf("final-only command took %v, want it inside the %v window", took, backchannelWindowForTest)
	}
}
