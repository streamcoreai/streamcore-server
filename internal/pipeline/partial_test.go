package pipeline

import (
	"encoding/base64"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

func stopTool(t *testing.T) *plugin.DispatchTool {
	t.Helper()
	tool, err := plugin.NewDispatchTool(plugin.ToolSpec{
		Name: "arm.stop",
		Dispatch: &plugin.DispatchSpec{
			Topic:   "arm.command",
			Payload: map[string]any{"action": "stop"},
			Speak:   "Stopping.",
		},
		OnPartial: &plugin.PartialSpec{
			Match:       []string{"stop", "freeze", "no no"},
			StartsWith:  []string{"wait", "hold on"},
			OncePerTurn: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func reflexFor(t *testing.T, tool plugin.PartialTool) *partialReflex {
	t.Helper()
	match, startsWith, after := tool.Partial().Phrases()
	spec := tool.Partial()
	return &partialReflex{
		fired:        map[string]bool{},
		firedAnchors: map[string]int{},
		pending:      map[string]*pendingCapture{},
		entries: []reflexEntry{{
			tool: tool, match: match, startsWith: startsWith,
			after: after, arg: spec.Arg(), settle: spec.Settle(), once: spec.OncePerTurn,
		}},
	}
}

func TestReflexMatching(t *testing.T) {
	cases := []struct {
		partial string
		want    bool
		why     string
	}{
		{"stop", true, "the bare word"},
		{"no, stop", true, "whole-word match anywhere is the point; prefix-only would miss the most natural correction"},
		{"STOP!", true, "case and punctuation are normalised away"},
		{"wait", true, "starts_with"},
		{"hold on", true, "multi-word starts_with"},
		{"hold on to it", true, "starts_with commits on sight: this utterance passes through \"hold on\" as it is spoken, and no rule can see the future. It is why the shipped manifest does not list this phrase."},
		{"stopwatch", false, "substring must not fire"},
		{"the stopwatch is red", false, "substring mid-sentence must not fire"},
		{"i want you to wait", false, "wait is starts_with only, so it does not fire mid-sentence"},
		{"", false, "empty"},
		{"...", false, "punctuation only normalises to nothing"},
	}
	for _, c := range cases {
		r := reflexFor(t, stopTool(t))
		got := len(r.observe(c.partial)) == 1
		if got != c.want {
			t.Errorf("match(%q) = %v, want %v — %s", c.partial, got, c.want, c.why)
		}
	}
}

func TestReflexFiresOncePerUtterance(t *testing.T) {
	r := reflexFor(t, stopTool(t))

	// STT revises a partial several times a second and every revision still
	// carries the word, so without the guard one "stop" sends a burst.
	if len(r.observe("sto")) != 0 {
		t.Fatal("partial word should not fire")
	}
	if len(r.observe("stop")) != 1 {
		t.Fatal("first match should fire")
	}
	for _, revision := range []string{"stop", "stop it", "stop it now"} {
		if hits := r.observe(revision); len(hits) != 0 {
			t.Errorf("revision %q fired again; once_per_turn did not hold", revision)
		}
	}

	r.endUtterance()
	if len(r.observe("stop")) != 1 {
		t.Error("the next utterance must be able to fire it again")
	}
}

func TestReflexEmitsTheSameBytesAsAToolCall(t *testing.T) {
	// The whole design rests on this. If the early path sent a different
	// packet, the device would need to know which one it came from, and the
	// server would be back to knowing what an arm is.
	tool := stopTool(t)

	viaModel, err := tool.Execute(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	viaPartial, err := tool.Execute(nil)
	if err != nil {
		t.Fatal(err)
	}

	modelEmit := plugin.ParseResult(viaModel).Emit
	partialEmit := plugin.ParseResult(viaPartial).Emit
	if len(modelEmit) != 1 || len(partialEmit) != 1 {
		t.Fatalf("expected one emission each, got %d and %d", len(modelEmit), len(partialEmit))
	}
	if modelEmit[0].Topic != partialEmit[0].Topic {
		t.Errorf("topic differs: %q vs %q", modelEmit[0].Topic, partialEmit[0].Topic)
	}
	if string(modelEmit[0].Payload) != string(partialEmit[0].Payload) {
		t.Errorf("payload differs:\n  model:   %s\n  partial: %s", modelEmit[0].Payload, partialEmit[0].Payload)
	}
}

func TestNoReflexWithoutAManifestThatAsked(t *testing.T) {
	// Every deployment without a robot must be untouched by this.
	if r := newPartialReflex(nil); r != nil {
		t.Error("no plugin manager should mean no reflex")
	}
	var nilReflex *partialReflex
	if hits := nilReflex.observe("stop"); hits != nil {
		t.Error("a nil reflex must match nothing rather than panic")
	}
	nilReflex.endUtterance() // must not panic
}

func TestReflexSendsOnePacketPerFiring(t *testing.T) {
	var sent []dcDataPacket
	p := &Pipeline{
		reflex: reflexFor(t, stopTool(t)),
		sendEvent: func(v interface{}) error {
			pkt, ok := v.(dcDataPacket)
			if !ok {
				t.Fatalf("unexpected event %T", v)
			}
			sent = append(sent, pkt)
			return nil
		},
	}

	p.reflexOnPartial("no, stop")
	p.reflexOnPartial("no, stop it")

	if len(sent) != 1 {
		t.Fatalf("expected exactly one packet across a revised utterance, got %d", len(sent))
	}
	if sent[0].Topic != "arm.command" {
		t.Errorf("topic = %q", sent[0].Topic)
	}
	raw, err := base64.StdEncoding.DecodeString(sent[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["action"] != "stop" {
		t.Errorf("payload = %s, want action stop", raw)
	}
}

// --- captures ---------------------------------------------------------------

func pickTool(t *testing.T, settleMs int) *plugin.DispatchTool {
	t.Helper()
	tool, err := plugin.NewDispatchTool(plugin.ToolSpec{
		Name: "arm.pick",
		Dispatch: &plugin.DispatchSpec{
			Topic:   "arm.command",
			Payload: map[string]any{"action": "pick"},
			Speak:   "Picking it up.",
		},
		ParametersRaw: map[string]any{
			"type":       "object",
			"properties": map[string]any{"label": map[string]any{"type": "string"}},
		},
		OnPartial: &plugin.PartialSpec{
			Capture:  &plugin.CaptureSpec{Arg: "label", After: []string{"no the", "actually the"}},
			SettleMs: settleMs,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func TestCaptureAfterAnchor(t *testing.T) {
	anchors := []string{"grab the", "grab", "no the"}
	cases := []struct {
		text, want string
		count      int
		ok         bool
		why        string
	}{
		{"grab the blue one", "blue one", 1, true, "the ordinary case"},
		{"grab the red one no the blue one", "blue one", 2, true,
			"one sentence, two instructions: the last anchor is the live one"},
		{"grab the red mug please", "red mug please", 1, true,
			"still one anchor, so the caller knows not to act again"},
		{"grab the", "", 1, true, "anchored but nothing captured yet"},
		{"put down the blue one", "", 0, false, "no anchor"},
		{"grabbing the blue one", "", 0, false, "anchors match whole words"},
	}
	for _, c := range cases {
		got, count, ok := captureAfterAnchor(c.text, anchors)
		if ok != c.ok || got != c.want || count != c.count {
			t.Errorf("captureAfterAnchor(%q) = (%q, %d, %v), want (%q, %d, %v) — %s",
				c.text, got, count, ok, c.want, c.count, c.ok, c.why)
		}
	}
}

func TestCaptureWaitsForTheNameToFinish(t *testing.T) {
	// "no the blue one" is spoken a word at a time. Firing on the first
	// revision that has any text sends the label "blue", and the arm goes to
	// the wrong object with complete confidence.
	// The settle timer fires on its own goroutine, so everything it touches
	// needs a lock even in a test.
	var mu sync.Mutex
	var fired []string
	r := reflexFor(t, pickTool(t, 60))
	r.send = func(hit reflexHit) {
		var args map[string]string
		if err := json.Unmarshal(hit.args, &args); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		fired = append(fired, args["label"])
		mu.Unlock()
	}

	for _, revision := range []string{"no the", "no the blue", "no the blue one"} {
		if hits := r.observe(revision); len(hits) != 0 {
			t.Fatalf("a capture must not fire from observe; %q returned %d hits", revision, len(hits))
		}
		time.Sleep(20 * time.Millisecond) // faster than the settle window
	}

	time.Sleep(150 * time.Millisecond) // now let it go quiet

	mu.Lock()
	defer mu.Unlock()
	if len(fired) != 1 {
		t.Fatalf("expected exactly one firing, got %d: %v", len(fired), fired)
	}
	if fired[0] != "blue one" {
		t.Errorf("captured %q, want %q — an earlier revision won the race", fired[0], "blue one")
	}
}

func TestCaptureAbandonedWhenTheTurnEnds(t *testing.T) {
	// Once the final arrives the ordinary path has the whole sentence, so a
	// half-heard object name is no longer the best guess available.
	var mu sync.Mutex
	fired := 0
	r := reflexFor(t, pickTool(t, 60))
	r.send = func(reflexHit) { mu.Lock(); fired++; mu.Unlock() }

	r.observe("no the blue one")
	r.endUtterance()
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if fired != 0 {
		t.Errorf("a capture fired %d times after the utterance ended", fired)
	}
}

func TestCapturedToolSendsTheLabel(t *testing.T) {
	var mu sync.Mutex
	var sent []dcDataPacket
	p := &Pipeline{
		reflex: reflexFor(t, pickTool(t, 40)),
		sendEvent: func(v interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, v.(dcDataPacket))
			return nil
		},
	}
	p.reflex.send = func(hit reflexHit) { p.fireReflex(hit, "no the blue one") }

	p.reflexOnPartial("no the blue one")
	time.Sleep(150 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 {
		t.Fatalf("expected one packet, got %d", len(sent))
	}
	raw, err := base64.StdEncoding.DecodeString(sent[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["action"] != "pick" || payload["label"] != "blue one" {
		t.Errorf("payload = %s, want action pick with label \"blue one\"", raw)
	}
}

// --- dedup ------------------------------------------------------------------

func TestModelDoesNotRepeatWhatAPartialAlreadySent(t *testing.T) {
	// The model reaches the same conclusion a moment later and calls the tool
	// itself. Sending it again reads as a second command to the device, which
	// preempts the motion already running and starts it over — a visible
	// stutter for no change in destination.
	p := &Pipeline{sendEvent: func(interface{}) error { return nil }}
	emission := plugin.Emission{Topic: "arm.command", Payload: json.RawMessage(`{"action":"pick","label":"blue one"}`)}

	if p.reflexAlreadySent(emission) {
		t.Fatal("nothing sent yet")
	}
	p.rememberReflexEmission(emission)
	if !p.reflexAlreadySent(emission) {
		t.Error("the model's identical packet should have been recognised")
	}
	// Consumed: a deliberate repeat later still goes out, because a caller who
	// asks twice means it.
	if p.reflexAlreadySent(emission) {
		t.Error("the record should be consumed by the first match")
	}
}

func TestDedupIsExact(t *testing.T) {
	p := &Pipeline{sendEvent: func(interface{}) error { return nil }}
	p.rememberReflexEmission(plugin.Emission{
		Topic: "arm.command", Payload: json.RawMessage(`{"action":"pick","label":"blue one"}`)})

	different := plugin.Emission{
		Topic: "arm.command", Payload: json.RawMessage(`{"action":"pick","label":"red mug"}`)}
	if p.reflexAlreadySent(different) {
		t.Error("a different label is a different command and must go out")
	}
}

// --- claim 1: a verb starts the reach ---------------------------------------

// grabTool carries what the shipped arm manifest carries: one phrase list for
// both the instruction and the correction, and no once_per_turn.
func grabTool(t *testing.T, settleMs int) *plugin.DispatchTool {
	t.Helper()
	tool, err := plugin.NewDispatchTool(plugin.ToolSpec{
		Name: "arm.pick",
		Dispatch: &plugin.DispatchSpec{
			Topic:   "arm.command",
			Payload: map[string]any{"action": "pick"},
			Speak:   "Picking it up.",
		},
		ParametersRaw: map[string]any{
			"type":       "object",
			"properties": map[string]any{"label": map[string]any{"type": "string"}},
		},
		OnPartial: &plugin.PartialSpec{
			Capture: &plugin.CaptureSpec{Arg: "label", After: []string{
				"grab the", "grab", "pick up the", "no the", "actually the"}},
			SettleMs: settleMs,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

func collectFirings(t *testing.T, r *partialReflex) (*[]string, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	fired := []string{}
	r.send = func(hit reflexHit) {
		var args map[string]string
		if err := json.Unmarshal(hit.args, &args); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		fired = append(fired, args["label"])
		mu.Unlock()
	}
	return &fired, &mu
}

func TestVerbStartsTheReach(t *testing.T) {
	r := reflexFor(t, grabTool(t, 50))
	fired, mu := collectFirings(t, r)

	// "grab the red mug", spoken.
	for _, revision := range []string{"grab the", "grab the red", "grab the red mug"} {
		r.observe(revision)
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(*fired) != 1 || (*fired)[0] != "red mug" {
		t.Fatalf("fired %v, want exactly [red mug]", *fired)
	}
}

func TestGrabThenCorrectionInOneBreath(t *testing.T) {
	// The case once_per_turn used to break. "grab the red one, no the blue
	// one" is one utterance with no final between the two halves, and
	// suppressing the second leaves the arm heading for the wrong object.
	r := reflexFor(t, grabTool(t, 50))
	fired, mu := collectFirings(t, r)

	r.observe("grab the red one")
	time.Sleep(120 * time.Millisecond) // settles, fires "red one"

	for _, revision := range []string{"grab the red one no the", "grab the red one no the blue", "grab the red one no the blue one"} {
		r.observe(revision)
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(*fired) != 2 {
		t.Fatalf("fired %v, want two: the grab and the correction", *fired)
	}
	if (*fired)[0] != "red one" || (*fired)[1] != "blue one" {
		t.Errorf("fired %v, want [red one blue one]", *fired)
	}
}

func TestSameNameSettlingTwiceDoesNotResend(t *testing.T) {
	// A second identical command would preempt the motion already running and
	// restart it, for no change in destination.
	r := reflexFor(t, grabTool(t, 40))
	fired, mu := collectFirings(t, r)

	r.observe("grab the red mug")
	time.Sleep(100 * time.Millisecond)
	r.observe("grab the red mug please")
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(*fired) != 1 {
		t.Errorf("fired %v, want one — no second anchor, so no second instruction", *fired)
	}
}

func TestLongestAnchorWinsSoTheVerbIsNotCaptured(t *testing.T) {
	// "grab" and "grab the" both match at position 0. Taking the shorter one
	// would send the label "the red mug".
	got, count, ok := captureAfterAnchor("grab the red mug", []string{"grab", "grab the"})
	if !ok || got != "red mug" || count != 1 {
		t.Errorf("captureAfterAnchor = (%q, %d, %v), want (\"red mug\", 1, true)", got, count, ok)
	}
}

func TestRetractStopsWhatTheVerbStarted(t *testing.T) {
	// "grab the red mug — no no" has to reach the brake. The retract phrases
	// live on arm.stop, so this is two tools on one reflex.
	pick := grabTool(t, 40)
	stop := stopTool(t)

	r := &partialReflex{fired: map[string]bool{}, firedAnchors: map[string]int{}, pending: map[string]*pendingCapture{}}
	for _, tool := range []plugin.PartialTool{pick, stop} {
		spec := tool.Partial()
		match, startsWith, after := spec.Phrases()
		r.entries = append(r.entries, reflexEntry{
			tool: tool, match: match, startsWith: startsWith,
			after: after, arg: spec.Arg(), settle: spec.Settle(), once: spec.OncePerTurn,
		})
	}

	if hits := r.observe("grab the red mug"); len(hits) != 0 {
		t.Fatal("the pick is a capture and must not fire from observe")
	}
	hits := r.observe("grab the red mug no no")
	if len(hits) != 1 || hits[0].tool.Name() != "arm.stop" {
		t.Fatalf("retract did not reach the brake: %v", hits)
	}
}

func TestStaleDedupRecordDoesNotSwallowTheModel(t *testing.T) {
	// After a correction, only the latest command should be able to suppress
	// the model. A stale record for the abandoned object would silently drop a
	// model call for it — and the model, having heard the whole sentence, may
	// be the one that is right.
	p := &Pipeline{sendEvent: func(interface{}) error { return nil }}
	red := plugin.Emission{Topic: "arm.command", Payload: json.RawMessage(`{"action":"pick","label":"red one"}`)}
	blue := plugin.Emission{Topic: "arm.command", Payload: json.RawMessage(`{"action":"pick","label":"blue one"}`)}

	p.rememberReflexEmission(red)
	p.rememberReflexEmission(blue)

	if p.reflexAlreadySent(red) {
		t.Error("the superseded command must no longer suppress the model")
	}
	if !p.reflexAlreadySent(blue) {
		t.Error("the latest command should still suppress its duplicate")
	}
}

// --- regression: everything that did not ask for this is untouched ----------

func TestToolEmitUnaffectedWithoutAPartialManifest(t *testing.T) {
	// The dedup check runs before every dispatching tool's packet, including
	// the movement and gesture sets. On a deployment where no manifest
	// declares on_partial, reflexSent is never written and the check must be
	// a no-op rather than a way to lose packets.
	p := &Pipeline{sendEvent: func(interface{}) error { return nil }}

	for _, emission := range []plugin.Emission{
		{Topic: "movement.command", Payload: json.RawMessage(`{"action":"forward"}`)},
		{Topic: "movement.command", Payload: json.RawMessage(`{"action":"forward"}`)},
		{Topic: "gesture.play", Payload: json.RawMessage(`{"name":"wave"}`)},
		{Topic: "arm.command", Payload: json.RawMessage(`{"action":"home"}`)},
	} {
		if p.reflexAlreadySent(emission) {
			t.Errorf("%s %s was suppressed with nothing ever sent from a partial",
				emission.Topic, emission.Payload)
		}
	}
}

func TestDedupNeverCrossesTopics(t *testing.T) {
	// A packet the arm reflex sent must not be able to swallow a movement or
	// gesture packet, however the payloads line up.
	p := &Pipeline{sendEvent: func(interface{}) error { return nil }}
	p.rememberReflexEmission(plugin.Emission{
		Topic: "arm.command", Payload: json.RawMessage(`{"action":"stop"}`)})

	if p.reflexAlreadySent(plugin.Emission{
		Topic: "movement.command", Payload: json.RawMessage(`{"action":"stop"}`)}) {
		t.Error("an identical payload on another topic is a different command")
	}
	if !p.reflexAlreadySent(plugin.Emission{
		Topic: "arm.command", Payload: json.RawMessage(`{"action":"stop"}`)}) {
		t.Error("the arm packet should still dedup")
	}
}

func TestManifestsWithoutOnPartialStayValid(t *testing.T) {
	// Adding fields to the contract must not invalidate a manifest written
	// before they existed.
	m := plugin.Manifest{
		Name: "gesture",
		Tools: []plugin.ToolSpec{{
			Name:     "gesture.wave",
			Dispatch: &plugin.DispatchSpec{Topic: "gesture.play"},
		}},
	}
	if err := m.Validate(); err != nil {
		t.Errorf("a manifest with no on_partial must still load: %v", err)
	}
}
