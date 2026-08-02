// Package procstat samples the process's own CPU usage from getrusage on a
// stable interval and stores the latest normalised percentage (0..100,
// averaged across all cores). Public reads are lock-free and never reset
// the baseline, so frequent callers (e.g. /health hit by an ALB) don't
// interfere with the rolling window.
//
// Typical use: one Sampler per process. New() starts the background loop.
package procstat

import (
	"math"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
)

// defaultInterval balances responsiveness against signal-to-noise. 5s is
// long enough that an idle Go process accumulates measurable CPU time,
// short enough that a sudden burst shows up within ~one heartbeat cycle.
const defaultInterval = 5 * time.Second

type Sampler struct {
	cores int
	pct   atomic.Uint64 // float64 bits via math.Float64bits
}

// New returns a Sampler with a background goroutine that updates the
// current CPU percentage every defaultInterval. The goroutine is intended
// to live for the life of the process — there is no Stop().
func New() *Sampler {
	s := &Sampler{cores: max(1, runtime.NumCPU())}
	go s.run(defaultInterval)
	return s
}

func (s *Sampler) run(interval time.Duration) {
	last, ok := readCPU()
	lastAt := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	for now := range t.C {
		cur, curOK := readCPU()
		if ok && curOK {
			elapsed := now.Sub(lastAt)
			delta := cur - last
			if elapsed > 0 && delta >= 0 {
				pct := float64(delta) / float64(elapsed) * 100.0
				pct = pct / float64(s.cores)
				if pct < 0 {
					pct = 0
				}
				s.pct.Store(math.Float64bits(pct))
			}
		}
		last, ok = cur, curOK
		lastAt = now
	}
}

// CPUPercent returns the most recent normalized CPU percent the background
// loop computed. Zero is returned before the first interval completes.
// This call is lock-free and safe to invoke from any goroutine.
func (s *Sampler) CPUPercent() float64 {
	return math.Float64frombits(s.pct.Load())
}

func readCPU() (time.Duration, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	return time.Duration(int64(ru.Utime.Sec)+int64(ru.Stime.Sec))*time.Second +
		time.Duration(int64(ru.Utime.Usec)+int64(ru.Stime.Usec))*time.Microsecond, true
}
