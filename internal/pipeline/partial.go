package pipeline

import (
	"encoding/json"
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/plugin"
)

// Firing a tool from a half-spoken sentence.
//
// A brake is the case that pays for this. "Stop" routed the ordinary way waits
// for endpointing, then a model turn, then a tool call — the better part of a
// second spent deliberating the one instruction nobody wants deliberated. The
// packet a manifest would have produced is the same packet either way, so the
// early path sends it and the model reads the result like any other.
//
// A correction is the second case. "No, the blue one" arriving while the arm is
// already reaching has to change where it is going, not queue behind the turn
// that is still being transcribed. That one carries an argument, which is what
// capture and the settle window are for: an object name arrives a word at a
// time and acting on the first word sends the wrong label.
//
// Nothing here knows what the tools do. A manifest declares which phrases mean
// it, and this matches them.

// reflexDedupTTL is how long a packet sent from a partial suppresses an
// identical one from the model.
//
// The model reaches the same conclusion a moment later and calls the tool
// itself, and without this the arbiter on the other end sees a second command,
// preempts the motion already running and starts it again — a visible stutter
// for no change in destination. Long enough to cover endpointing plus a model
// turn, short enough that a genuine repeat still gets through.
const reflexDedupTTL = 10 * time.Second

// partialReflex matches partial transcripts against the tools that opted in.
//
// One per Pipeline. The tool list is snapshotted at construction because
// partials arrive several times a second and the registry lock is not free.
type partialReflex struct {
	entries []reflexEntry

	// send delivers a hit. Set by the Pipeline after construction, because a
	// settled capture fires from a timer rather than from the caller.
	send func(reflexHit)

	mu sync.Mutex
	// fired remembers which tools already went off during this utterance.
	// STT revises a partial several times a second and every revision still
	// contains the word that matched, so without this a single "stop" sends a
	// packet on each one.
	fired map[string]bool
	// firedAnchors counts, per capturing tool, how many anchor phrases in this
	// utterance have already been acted on. Firing again needs a new one, which
	// is what makes "grab the red one, no the blue one" two instructions while
	// "grab the red mug please" stays one.
	firedAnchors map[string]int
	// pending holds captures still settling, keyed by tool name.
	pending map[string]*pendingCapture
}

type reflexEntry struct {
	tool       plugin.PartialTool
	match      []string
	startsWith []string
	after      []string
	arg        string
	settle     time.Duration
	once       bool
}

// reflexHit is one tool the partial asked for, with whatever it captured.
type reflexHit struct {
	tool plugin.PartialTool
	args json.RawMessage
}

// pendingCapture is a capture waiting out its quiet period.
type pendingCapture struct {
	text    string
	anchors int
	timer   *time.Timer
}

func newPartialReflex(mgr *plugin.Manager) *partialReflex {
	if mgr == nil {
		return nil
	}
	tools := mgr.PartialTools()
	if len(tools) == 0 {
		return nil
	}

	r := &partialReflex{
		fired:        make(map[string]bool, len(tools)),
		firedAnchors: make(map[string]int, len(tools)),
		pending:      make(map[string]*pendingCapture, len(tools)),
	}
	for _, tool := range tools {
		spec := tool.Partial()
		match, startsWith, after := spec.Phrases()
		r.entries = append(r.entries, reflexEntry{
			tool:       tool,
			match:      match,
			startsWith: startsWith,
			after:      after,
			arg:        spec.Arg(),
			settle:     spec.Settle(),
			once:       spec.OncePerTurn,
		})
		log.Printf("[reflex] %s fires on %v %v", tool.Name(), match, startsWith)
	}
	return r
}

// endUtterance clears the once-per-turn memory and abandons any capture still
// settling. Called when a final arrives: from here the ordinary path has the
// whole sentence, and a half-heard object name is no longer the best guess
// available.
func (r *partialReflex) endUtterance() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name := range r.fired {
		delete(r.fired, name)
	}
	for name, p := range r.pending {
		p.timer.Stop()
		delete(r.pending, name)
	}
	for name := range r.firedAnchors {
		delete(r.firedAnchors, name)
	}
}

// observe matches a partial and returns the tools to fire now. A tool with a
// capture is not returned: it is staged, and fires from its own timer once the
// captured text has stopped changing.
func (r *partialReflex) observe(partial string) []reflexHit {
	if r == nil {
		return nil
	}
	text := plugin.NormalizePartial(partial)
	if text == "" {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var hits []reflexHit
	for i := range r.entries {
		entry := &r.entries[i]
		name := entry.tool.Name()
		if entry.once && r.fired[name] {
			continue
		}

		if entry.arg != "" {
			r.stageLocked(entry, text)
			continue
		}
		if !containsPhrase(text, entry.match) && !hasPrefixPhrase(text, entry.startsWith) {
			continue
		}
		if entry.once {
			r.fired[name] = true
		}
		hits = append(hits, reflexHit{tool: entry.tool})
	}
	return hits
}

// stageLocked holds a capture until it stops changing. Caller holds r.mu.
func (r *partialReflex) stageLocked(entry *reflexEntry, text string) {
	rest, anchors, ok := captureAfterAnchor(text, entry.after)
	if !ok || rest == "" {
		return
	}

	name := entry.tool.Name()
	if p, ok := r.pending[name]; ok {
		if p.text == rest && p.anchors == anchors {
			return // unchanged; its timer is already counting down
		}
		p.timer.Stop()
	}

	p := &pendingCapture{text: rest, anchors: anchors}
	// time.AfterFunc runs in a bare goroutine, so a panic here would take the
	// process with it rather than the call.
	p.timer = time.AfterFunc(entry.settle, func() {
		defer func() {
			if v := recover(); v != nil {
				log.Printf("[reflex] panic firing %s: %v\n%s", name, v, debug.Stack())
			}
		}()
		r.settled(entry, p)
	})
	r.pending[name] = p
}

// settled fires a capture whose quiet period elapsed without a revision.
func (r *partialReflex) settled(entry *reflexEntry, p *pendingCapture) {
	name := entry.tool.Name()

	r.mu.Lock()
	// endUtterance may have dropped this between the timer firing and here,
	// and a superseded capture must not fire after the one that replaced it.
	if r.pending[name] != p {
		r.mu.Unlock()
		return
	}
	delete(r.pending, name)
	// Acting again needs a new anchor. Without that, every word the speaker
	// adds after the object name settles into a different capture and sends
	// another command: "grab the red mug" then "grab the red mug please"
	// would be two picks with two different labels.
	if p.anchors <= r.firedAnchors[name] {
		r.mu.Unlock()
		return
	}
	if r.firedAnchors == nil {
		r.firedAnchors = make(map[string]int, 1)
	}
	r.firedAnchors[name] = p.anchors
	if entry.once {
		r.fired[name] = true
	}
	send := r.send
	r.mu.Unlock()

	if send == nil {
		return
	}
	args, err := json.Marshal(map[string]string{entry.arg: p.text})
	if err != nil {
		log.Printf("[reflex] %s: encode capture: %v", name, err)
		return
	}
	send(reflexHit{tool: entry.tool, args: args})
}

// captureAfterAnchor returns what follows the last anchor phrase, and how many
// anchors the utterance contains.
//
// The last one rather than the first, because one sentence can carry two
// instructions: "grab the red one no the blue one" anchors twice and the
// argument is what follows the second. The count is what tells the caller
// whether this is a new instruction or the same one with more words after it.
func captureAfterAnchor(text string, anchors []string) (rest string, count int, ok bool) {
	// Overlapping anchors at one position collapse to the longest, so listing
	// both "grab" and "grab the" captures "red mug" and not "the red mug".
	longestAt := map[int]int{}
	for _, anchor := range anchors {
		for offset := 0; ; {
			i := strings.Index(text[offset:], anchor)
			if i < 0 {
				break
			}
			start := offset + i
			end := start + len(anchor)
			atBoundary := (start == 0 || text[start-1] == ' ') &&
				(end == len(text) || text[end] == ' ')
			if atBoundary && end > longestAt[start] {
				longestAt[start] = end
			}
			offset = start + 1
		}
	}
	if len(longestAt) == 0 {
		return "", 0, false
	}

	lastEnd := 0
	for _, end := range longestAt {
		if end > lastEnd {
			lastEnd = end
		}
	}
	return strings.TrimSpace(text[lastEnd:]), len(longestAt), true
}

// containsPhrase reports whether any phrase appears as a whole word run.
// Whole-word rather than substring, so "stop" does not fire on "stopwatch",
// and anywhere rather than at the front, so "no, stop" counts.
func containsPhrase(text string, phrases []string) bool {
	for _, phrase := range phrases {
		for offset := 0; ; {
			i := strings.Index(text[offset:], phrase)
			if i < 0 {
				break
			}
			start := offset + i
			end := start + len(phrase)
			// Both text and phrase are normalised, so a word boundary is the
			// string edge or a single space.
			if (start == 0 || text[start-1] == ' ') && (end == len(text) || text[end] == ' ') {
				return true
			}
			offset = start + 1
		}
	}
	return false
}

// hasPrefixPhrase reports whether the utterance opens with any phrase.
func hasPrefixPhrase(text string, phrases []string) bool {
	for _, phrase := range phrases {
		if text == phrase || strings.HasPrefix(text, phrase+" ") {
			return true
		}
	}
	return false
}

// reflexOnPartial fires whatever this partial matched.
//
// Runs on the STT callback, so it must not block: emit writes to the data
// channel and returns. The model is told nothing — it reads the outcome in the
// device's own result event, the same as for a tool it called itself.
func (p *Pipeline) reflexOnPartial(partial string) {
	if p.reflex == nil {
		return
	}
	for _, hit := range p.reflex.observe(partial) {
		p.fireReflex(hit, partial)
	}
}

func (p *Pipeline) fireReflex(hit reflexHit, partial string) {
	started := time.Now()
	raw, err := hit.tool.Execute(hit.args)
	if err != nil {
		log.Printf("[reflex] %s: %v", hit.tool.Name(), err)
		return
	}
	for _, emission := range plugin.ParseResult(raw).Emit {
		if err := p.emit(emission); err != nil {
			log.Printf("[reflex] %s: %v", hit.tool.Name(), err)
			continue
		}
		p.rememberReflexEmission(emission)
		// The number worth measuring: how long from the words landing in a
		// partial to the packet being on the wire.
		log.Printf("[reflex] %s fired on %q in %s", hit.tool.Name(), partial, time.Since(started))
	}
}

// rememberReflexEmission records a packet so the model's own call to the same
// tool does not send it twice.
func (p *Pipeline) rememberReflexEmission(emission plugin.Emission) {
	key := emission.Topic + "\x00" + string(emission.Payload)
	p.reflexSentMu.Lock()
	defer p.reflexSentMu.Unlock()
	if p.reflexSent == nil {
		p.reflexSent = make(map[string]time.Time, 4)
	}
	now := time.Now()
	topic := emission.Topic + "\x00"
	for k, at := range p.reflexSent {
		// Drop expired records, and any earlier one for this topic: after a
		// correction only the latest command should be able to suppress the
		// model. Otherwise "grab the red one, no the blue one" leaves a stale
		// red record that silently swallows a model call for red — which,
		// having heard the whole sentence, might be the right one.
		if now.Sub(at) > reflexDedupTTL || strings.HasPrefix(k, topic) {
			delete(p.reflexSent, k)
		}
	}
	p.reflexSent[key] = now
}

// reflexAlreadySent reports whether a partial already sent this exact packet,
// and consumes the record so a deliberate repeat later still goes out.
func (p *Pipeline) reflexAlreadySent(emission plugin.Emission) bool {
	key := emission.Topic + "\x00" + string(emission.Payload)
	p.reflexSentMu.Lock()
	defer p.reflexSentMu.Unlock()
	at, ok := p.reflexSent[key]
	if !ok || time.Since(at) > reflexDedupTTL {
		return false
	}
	delete(p.reflexSent, key)
	return true
}
