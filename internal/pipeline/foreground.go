package pipeline

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/streamcoreai/streamcore-server/internal/stt"
)

// Telling the caller from the room.
//
// STT transcribes whoever is audible, so a TV or the next table becomes turns
// the agent answers, and the VAD hears that chatter as the caller still
// talking, which stretches every wait that holds for the caller to finish.
// The caller is nearly always the loudest voice on the line, being the one at
// the microphone, so loudness separates them: each final says which audio it
// came from, the level of the caller's committed turns is learned, and speech
// well below that level is the room.

const (
	// speakerRing is how much per-frame loudness is kept to look a final's
	// audio up in: 30s at 20ms, longer than any one utterance.
	speakerRing = 1500
	// speakerFrame is the frame length the STT is fed in.
	speakerFrame = 20 * time.Millisecond
	// speakerHalfLife is how fast the learned level falls while no turn is
	// committed, so a caller quieter than one loud burst is not shut out for
	// long.
	speakerHalfLife = 20 * time.Second
	// speakerSpanPeak picks a span's loudness: its loudest 100ms (the fifth
	// loudest frame). The caller's words always have syllable peaks, so the
	// pauses between words inside one final cannot pull them under the bar,
	// and one click is not enough to lift the room over it.
	speakerSpanPeak = 5

	// The decay above would also let the room in: after 40s of silence in a
	// café the bar is 12dB lower and the chatter gets answered. So the bar
	// never relaxes to within speakerFloorMargin of the background, measured
	// as the 30th-percentile frame over the last 10s. Chatter's loudest 100ms
	// sits about 6dB over that; in a quiet room the background is near zero
	// and the decay works as before.
	speakerFloorWindow     = 500 // frames, 10s
	speakerFloorEvery      = 50  // frames between recomputing it
	speakerFloorPercentile = 0.3
	speakerFloorMargin     = 3.16 // 10dB
)

// speakerGate learns the caller's level and judges finals and frames against
// it. Every method is nil-safe and, when the gate is off or has learned
// nothing yet, lets everything through, so behaviour is unchanged until a
// final with an audio span has been committed.
type speakerGate struct {
	mu sync.Mutex
	// fraction is the lowest level, as a share of the caller's, that still
	// counts as the caller. 0 turns the gate off.
	fraction float64

	ring      [speakerRing]float32
	total     int64 // frames recorded since the call began
	connStart int64 // frame index where the current STT connection began

	level   float64 // caller level; 0 until a final has been committed
	levelAt time.Time

	floor   float64 // background level, see speakerFloorWindow
	scratch []float64
}

// newSpeakerGate builds a gate that treats speech more than db below the
// caller as the room. db <= 0 turns it off.
func newSpeakerGate(db float64) *speakerGate {
	if db <= 0 {
		return &speakerGate{}
	}
	return &speakerGate{fraction: math.Pow(10, -db/20)}
}

// frame records the loudness of one frame sent to the STT.
func (g *speakerGate) frame(rms float64) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.ring[g.total%speakerRing] = float32(rms)
	g.total++
	if g.total%speakerFloorEvery == 0 {
		n := min(g.total, speakerFloorWindow)
		g.scratch = g.scratch[:0]
		for i := g.total - n; i < g.total; i++ {
			g.scratch = append(g.scratch, float64(g.ring[i%speakerRing]))
		}
		sort.Float64s(g.scratch)
		g.floor = g.scratch[int(speakerFloorPercentile*float64(n-1))]
	}
	g.mu.Unlock()
}

// newConnection marks a new STT connection: its audio clock starts at zero.
func (g *speakerGate) newConnection() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.connStart = g.total
	g.mu.Unlock()
}

// spanLevelLocked is the loudness of the audio between start and end seconds
// on the current connection, if it is still in the ring.
func (g *speakerGate) spanLevelLocked(start, end float64) (float64, bool) {
	from := g.connStart + int64(start/speakerFrame.Seconds())
	to := g.connStart + int64(math.Ceil(end/speakerFrame.Seconds()))
	if lo := g.total - speakerRing; from < lo {
		from = lo
	}
	if to > g.total {
		to = g.total
	}
	if to <= from {
		return 0, false
	}
	levels := make([]float64, 0, to-from)
	for i := from; i < to; i++ {
		levels = append(levels, float64(g.ring[i%speakerRing]))
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(levels)))
	return levels[min(speakerSpanPeak, len(levels))-1], true
}

// currentLevelLocked applies the decay since the last committed turn.
func (g *speakerGate) currentLevelLocked(now time.Time) float64 {
	if g.level == 0 {
		return 0
	}
	decayed := g.level * math.Pow(0.5, now.Sub(g.levelAt).Seconds()/speakerHalfLife.Seconds())
	if g.fraction > 0 {
		decayed = max(decayed, min(g.level, g.floor*speakerFloorMargin/g.fraction))
	}
	return decayed
}

// background reports whether a final came from the room rather than the
// caller, and otherwise learns the caller's level from it. Finals with no
// audio span (other providers) are never background.
func (g *speakerGate) background(r stt.TranscriptResult, now time.Time) (span, caller float64, bg bool) {
	if g == nil || g.fraction == 0 || r.AudioEnd <= r.AudioStart {
		return 0, 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	span, ok := g.spanLevelLocked(r.AudioStart, r.AudioEnd)
	if !ok || span <= 0 {
		return 0, 0, false
	}
	caller = g.currentLevelLocked(now)
	if caller > 0 && span < caller*g.fraction {
		return span, caller, true
	}
	// Up at once to a louder turn, down slowly to a quieter one.
	if span > caller {
		g.level = span
	} else {
		g.level = 0.7*caller + 0.3*span
	}
	g.levelAt = now
	return span, g.level, false
}

// near reports whether a frame is loud enough to be the caller.
func (g *speakerGate) near(rms float64, now time.Time) bool {
	if g == nil || g.fraction == 0 {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	caller := g.currentLevelLocked(now)
	return caller == 0 || rms >= caller*g.fraction
}
