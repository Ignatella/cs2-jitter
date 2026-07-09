package aggregate

import (
	"slices"
	"sync"
	"time"

	"jitter/internal/probe"
)

// LivePoint is one raw sample for the live view.
type LivePoint struct {
	T     time.Time `json:"t"`
	RTTms float64   `json:"rtt_ms"`
	Lost  bool      `json:"lost"`
}

// Live keeps the most recent samples per target. Goroutine-safe.
type Live struct {
	mu       sync.Mutex
	capacity int
	data     map[string][]LivePoint
}

func NewLive(capacity int) *Live {
	return &Live{capacity: capacity, data: make(map[string][]LivePoint)}
}

func (l *Live) Add(s probe.Sample) {
	p := LivePoint{T: s.SentAt.UTC(), Lost: s.Lost}
	if !s.Lost {
		p.RTTms = float64(s.RTT) / float64(time.Millisecond)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := append(l.data[s.Target], p)
	if len(buf) > l.capacity {
		buf = slices.Clone(buf[len(buf)-l.capacity:])
	}
	l.data[s.Target] = buf
}

// Recent returns a copy of the buffered points for target, oldest first.
func (l *Live) Recent(target string) []LivePoint {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.data[target])
}
