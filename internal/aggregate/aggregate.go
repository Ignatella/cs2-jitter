// Package aggregate folds probe samples into per-minute statistics.
package aggregate

import (
	"math"
	"slices"
	"time"

	"jitter/internal/probe"
)

// Aggregate is one minute of statistics for one target. RTT fields are
// milliseconds over successful samples only. Jitter fields describe the
// distribution of absolute differences between successive (send-ordered)
// RTT samples — the p99/max are the lag-spike signal.
type Aggregate struct {
	Target      string    `json:"target"`
	POP         string    `json:"pop"`
	Minute      time.Time `json:"minute"`
	Samples     int       `json:"samples"`
	Lost        int       `json:"lost"`
	MinMS       float64   `json:"min_ms"`
	AvgMS       float64   `json:"avg_ms"`
	MaxMS       float64   `json:"max_ms"`
	P50MS       float64   `json:"p50_ms"`
	P95MS       float64   `json:"p95_ms"`
	P99MS       float64   `json:"p99_ms"`
	JitterMS    float64   `json:"jitter_ms"` // mean successive delta (header stat)
	JitterP10MS float64   `json:"jitter_p10_ms"`
	JitterP50MS float64   `json:"jitter_p50_ms"`
	JitterP99MS float64   `json:"jitter_p99_ms"`
	JitterMaxMS float64   `json:"jitter_max_ms"`
	LossPct     float64   `json:"loss_pct"`
	Partial     bool      `json:"partial"`
}

// okSample is one successful RTT tagged with its send sequence.
type okSample struct {
	seq uint64
	rtt float64
}

type bucket struct {
	pop     string
	minute  time.Time
	oks     []okSample // successful samples only
	samples int        // total attempts (incl. losses)
	lost    int
}

type targetState struct {
	buckets map[int64]*bucket // keyed by minute unix-seconds
	// watermark is the max SentAt seen for this target — a logical clock that
	// advances as samples arrive and drives grace-based finalization.
	watermark time.Time
	// finalizedThrough is the highest minute key already emitted; samples for
	// a minute at or below it arrived after the grace window and are dropped.
	finalizedThrough int64
}

// Bucketer groups samples into (target, minute) buckets and finalizes a minute
// only after a completion grace window, so slow probes that arrive out of
// order still land in the correct minute. Not goroutine-safe: call from a
// single consumer goroutine.
type Bucketer struct {
	expected int
	window   time.Duration // 1 minute + probe timeout (completion grace)
	targets  map[string]*targetState
}

// NewBucketer builds a Bucketer. timeout is the probe reply timeout; a minute
// is held open for that long past its end so the slowest in-flight probe can
// still be counted.
func NewBucketer(interval, timeout time.Duration) *Bucketer {
	return &Bucketer{
		expected: int(time.Minute / interval),
		window:   time.Minute + timeout,
		targets:  make(map[string]*targetState),
	}
}

// Add folds one sample in and returns any buckets that the advancing watermark
// has now made safe to finalize (in minute order).
func (b *Bucketer) Add(s probe.Sample) []Aggregate {
	ts := b.targets[s.Target]
	if ts == nil {
		ts = &targetState{buckets: make(map[int64]*bucket), finalizedThrough: math.MinInt64}
		b.targets[s.Target] = ts
	}
	if s.SentAt.After(ts.watermark) {
		ts.watermark = s.SentAt
	}
	minute := s.SentAt.UTC().Truncate(time.Minute)
	key := minute.Unix()
	if key <= ts.finalizedThrough {
		return nil // arrived after its minute was finalized (beyond grace)
	}
	bk := ts.buckets[key]
	if bk == nil {
		bk = &bucket{pop: s.POP, minute: minute}
		ts.buckets[key] = bk
	}
	bk.samples++
	if s.Lost {
		bk.lost++
	} else {
		bk.oks = append(bk.oks, okSample{seq: s.Seq, rtt: float64(s.RTT) / float64(time.Millisecond)})
	}
	return b.harvest(s.Target, ts)
}

// harvest finalizes every bucket whose grace window has closed under the
// current watermark, in minute order.
func (b *Bucketer) harvest(target string, ts *targetState) []Aggregate {
	var ready []int64
	for key, bk := range ts.buckets {
		if !ts.watermark.Before(bk.minute.Add(b.window)) {
			ready = append(ready, key)
		}
	}
	if len(ready) == 0 {
		return nil
	}
	slices.Sort(ready)
	out := make([]Aggregate, 0, len(ready))
	for _, key := range ready {
		out = append(out, b.finalize(target, ts.buckets[key]))
		delete(ts.buckets, key)
		if key > ts.finalizedThrough {
			ts.finalizedThrough = key
		}
	}
	return out
}

// FlushAll closes and returns every open bucket (shutdown path), in
// (target, minute) order per target.
func (b *Bucketer) FlushAll() []Aggregate {
	var out []Aggregate
	for target, ts := range b.targets {
		keys := make([]int64, 0, len(ts.buckets))
		for k := range ts.buckets {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			out = append(out, b.finalize(target, ts.buckets[k]))
		}
	}
	b.targets = make(map[string]*targetState)
	return out
}

func (b *Bucketer) finalize(target string, cur *bucket) Aggregate {
	a := Aggregate{
		Target:  target,
		POP:     cur.pop,
		Minute:  cur.minute,
		Samples: cur.samples,
		Lost:    cur.lost,
		Partial: cur.samples < b.expected/2,
	}
	if cur.samples > 0 {
		a.LossPct = 100 * float64(cur.lost) / float64(cur.samples)
	}
	if len(cur.oks) == 0 {
		return a
	}

	// RTT stats over the (order-independent) value distribution.
	rttSorted := make([]float64, len(cur.oks))
	for i, o := range cur.oks {
		rttSorted[i] = o.rtt
	}
	slices.Sort(rttSorted)
	var sum float64
	for _, v := range rttSorted {
		sum += v
	}
	a.MinMS = rttSorted[0]
	a.MaxMS = rttSorted[len(rttSorted)-1]
	a.AvgMS = sum / float64(len(rttSorted))
	a.P50MS = percentile(rttSorted, 0.50)
	a.P95MS = percentile(rttSorted, 0.95)
	a.P99MS = percentile(rttSorted, 0.99)

	// Jitter = |successive RTT delta| in true send order (by seq), so that
	// concurrent probes completing out of order don't distort it.
	ordered := slices.Clone(cur.oks)
	slices.SortFunc(ordered, func(x, y okSample) int {
		switch {
		case x.seq < y.seq:
			return -1
		case x.seq > y.seq:
			return 1
		default:
			return 0
		}
	})
	if len(ordered) < 2 {
		return a // need two successes for a delta; leave jitter at zero
	}
	deltas := make([]float64, 0, len(ordered)-1)
	var dsum float64
	for i := 1; i < len(ordered); i++ {
		d := math.Abs(ordered[i].rtt - ordered[i-1].rtt)
		deltas = append(deltas, d)
		dsum += d
	}
	deltaSorted := slices.Clone(deltas)
	slices.Sort(deltaSorted)
	a.JitterMS = dsum / float64(len(deltas))
	a.JitterMaxMS = deltaSorted[len(deltaSorted)-1]
	a.JitterP10MS = percentile(deltaSorted, 0.10)
	a.JitterP50MS = percentile(deltaSorted, 0.50)
	a.JitterP99MS = percentile(deltaSorted, 0.99)
	return a
}

// percentile is nearest-rank on an ascending-sorted slice.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	idx = max(0, min(idx, len(sorted)-1))
	return sorted[idx]
}
