package engine

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Progress is shared between a running job and whoever displays it.
type Progress struct {
	done  atomic.Int64
	total atomic.Int64

	mu      sync.Mutex
	phase   string
	started time.Time
	log     []string
	// OnLog, if set, receives every log line (used by the CLI).
	OnLog func(string)
}

// Snapshot is a point-in-time view of a Progress.
type Snapshot struct {
	Phase   string   `json:"phase"`
	Done    int64    `json:"done"`
	Total   int64    `json:"total"`
	Percent float64  `json:"percent"`
	Rate    float64  `json:"rate"` // bytes/second
	ETA     float64  `json:"eta"`  // seconds, -1 unknown
	Elapsed float64  `json:"elapsed"`
	Log     []string `json:"log"`
}

// Start begins a new phase with the given byte total.
func (p *Progress) Start(phase string, total int64) {
	p.mu.Lock()
	p.phase = phase
	p.started = time.Now()
	p.mu.Unlock()
	p.done.Store(0)
	p.total.Store(total)
	p.Logf("%s", phase)
}

// Add records n more bytes processed.
func (p *Progress) Add(n int64) { p.done.Add(n) }

// Logf appends a line to the job log.
func (p *Progress) Logf(format string, a ...any) {
	line := time.Now().Format("15:04:05 ") + fmt.Sprintf(format, a...)
	p.mu.Lock()
	p.log = append(p.log, line)
	if len(p.log) > 500 {
		p.log = p.log[len(p.log)-500:]
	}
	cb := p.OnLog
	p.mu.Unlock()
	if cb != nil {
		cb(line)
	}
}

// Snapshot returns the current state.
func (p *Progress) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Snapshot{Phase: p.phase, Done: p.done.Load(), Total: p.total.Load(), ETA: -1,
		Log: append([]string(nil), p.log...)}
	if !p.started.IsZero() {
		s.Elapsed = time.Since(p.started).Seconds()
	}
	if s.Total > 0 {
		s.Percent = 100 * float64(s.Done) / float64(s.Total)
	}
	if s.Elapsed > 1 {
		s.Rate = float64(s.Done) / s.Elapsed
		if s.Rate > 0 && s.Total > 0 {
			s.ETA = float64(s.Total-s.Done) / s.Rate
		}
	}
	return s
}

// HumanBytes formats a byte count.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
