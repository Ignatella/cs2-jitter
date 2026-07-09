package aggregate

import (
	"testing"
	"time"

	"jitter/internal/probe"
)

// sample builds a probe.Sample. seq is the send-order sequence.
func sample(target string, seq uint64, t time.Time, rttMS int, lost bool) probe.Sample {
	return probe.Sample{
		Target: target, POP: "waw", Seq: seq, SentAt: t,
		RTT: time.Duration(rttMS) * time.Millisecond, Lost: lost,
	}
}

var m0 = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

// bucketer with a 1s completion grace (matches the default probe timeout).
func newTestBucketer() *Bucketer {
	return NewBucketer(250*time.Millisecond, time.Second) // expected 240/min
}

// past pushes the watermark far enough to finalize everything up to `mins`
// minutes after m0, and returns the aggregates that closed.
func flushViaWatermark(b *Bucketer, target string, at time.Time) []Aggregate {
	// A lone future sample for the target advances its watermark and harvests.
	return b.Add(sample(target, 1_000_000, at, 10, false))
}

func TestBucketFinalizesAfterGrace(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0.Add(1*time.Second), 10, false))
	b.Add(sample("waw-1", 1, m0.Add(2*time.Second), 20, true)) // lost
	b.Add(sample("waw-1", 2, m0.Add(3*time.Second), 20, false))
	b.Add(sample("waw-1", 3, m0.Add(4*time.Second), 36, false))

	// Watermark must reach m0+60s+1s(grace) to close the m0 bucket.
	// A sample at m0+60.9s should NOT yet close it.
	if got := b.Add(sample("waw-1", 4, m0.Add(60*time.Second+900*time.Millisecond), 15, false)); len(got) != 0 {
		t.Fatalf("bucket closed before grace elapsed: %v", got)
	}
	// A sample at m0+61s closes it.
	closed := b.Add(sample("waw-1", 5, m0.Add(61*time.Second), 15, false))
	if len(closed) != 1 {
		t.Fatalf("want 1 closed bucket, got %d", len(closed))
	}
	a := closed[0]
	if a.Target != "waw-1" || a.POP != "waw" || !a.Minute.Equal(m0) {
		t.Fatalf("bucket identity wrong: %+v", a)
	}
	if a.Samples != 4 || a.Lost != 1 {
		t.Fatalf("samples=%d lost=%d, want 4/1", a.Samples, a.Lost)
	}
	if a.MinMS != 10 || a.MaxMS != 36 || a.AvgMS != 22 {
		t.Fatalf("min/avg/max = %v/%v/%v, want 10/22/36", a.MinMS, a.AvgMS, a.MaxMS)
	}
	// deltas in send order: |20-10|=10, |36-20|=16 (loss doesn't reset chain).
	if a.JitterMS != 13 {
		t.Fatalf("jitter mean = %v, want 13", a.JitterMS)
	}
	if a.JitterMaxMS != 16 {
		t.Fatalf("jitter max = %v, want 16", a.JitterMaxMS)
	}
	if a.LossPct != 25 {
		t.Fatalf("loss = %v, want 25", a.LossPct)
	}
	if !a.Partial {
		t.Fatal("bucket should be partial")
	}
}

// Jitter must be computed in send (seq) order even when samples ARRIVE out of
// order — the core fix for concurrent probes completing out of order.
func TestJitterUsesSendOrderNotArrivalOrder(t *testing.T) {
	b := newTestBucketer()
	// True send order: seq0=10ms, seq1=600ms, seq2=20ms.
	// Feed them in ARRIVAL order 0, 2, 1 (the slow one lands last).
	b.Add(sample("waw-1", 0, m0.Add(1*time.Second), 10, false))
	b.Add(sample("waw-1", 2, m0.Add(3*time.Second), 20, false))
	b.Add(sample("waw-1", 1, m0.Add(2*time.Second), 600, false))
	closed := flushViaWatermark(b, "waw-1", m0.Add(62*time.Second))

	var a *Aggregate
	for i := range closed {
		if closed[i].Minute.Equal(m0) {
			a = &closed[i]
		}
	}
	if a == nil {
		t.Fatalf("m0 bucket not closed: %+v", closed)
	}
	// Correct (send-order) deltas: |600-10|=590, |20-600|=580 -> mean 585, max 590.
	if a.JitterMS != 585 {
		t.Fatalf("jitter mean = %v, want 585 (send order)", a.JitterMS)
	}
	if a.JitterMaxMS != 590 {
		t.Fatalf("jitter max = %v, want 590 (send order)", a.JitterMaxMS)
	}
}

// A slow sample near a minute boundary that arrives after a fast next-minute
// sample must NOT be dropped: it lands in its own minute (fix for finding 2).
func TestLateSampleWithinGraceNotDropped(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0.Add(59*time.Second), 10, false))       // minute m0
	b.Add(sample("waw-1", 2, m0.Add(60*time.Second+200*time.Millisecond), 20, false)) // minute m0+1 arrives
	// Late arrival: seq1 was sent at m0+59.9s (minute m0) but completes now.
	b.Add(sample("waw-1", 1, m0.Add(59*time.Second+900*time.Millisecond), 300, false))

	all := b.FlushAll()
	var m0agg *Aggregate
	for i := range all {
		if all[i].Minute.Equal(m0) {
			m0agg = &all[i]
		}
	}
	if m0agg == nil || m0agg.Samples != 2 {
		t.Fatalf("late sample dropped; m0 = %+v (all: %+v)", m0agg, all)
	}
	if m0agg.MaxMS != 300 {
		t.Fatalf("late high-RTT sample missing from stats: max = %v, want 300", m0agg.MaxMS)
	}
}

// A sample arriving after its minute's grace has fully elapsed is dropped
// (bounded, pathological), and must not resurrect a finalized bucket.
func TestSampleAfterGraceIsDropped(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0.Add(30*time.Second), 10, false))
	// Advance watermark well past m0's grace to finalize it.
	closed := flushViaWatermark(b, "waw-1", m0.Add(120*time.Second))
	if len(closed) != 1 || closed[0].Samples != 1 {
		t.Fatalf("expected m0 finalized with 1 sample, got %+v", closed)
	}
	// Now a very late m0 sample arrives — must be dropped, not recreate m0.
	if got := b.Add(sample("waw-1", 1, m0.Add(31*time.Second), 99, false)); len(got) != 0 {
		t.Fatalf("late-after-grace sample resurrected a bucket: %v", got)
	}
	if got := b.FlushAll(); len(got) != 1 { // only the m0+120 lone sample's bucket
		for _, a := range got {
			if a.Minute.Equal(m0) {
				t.Fatal("dropped sample resurrected the m0 bucket")
			}
		}
	}
}

func TestJitterPercentiles(t *testing.T) {
	b := newTestBucketer()
	// 11 successful samples -> 10 deltas. Craft deltas 1..10 by walking RTT.
	// RTTs: 0,1,3,6,10,15,21,28,36,45,55 -> deltas 1,2,3,4,5,6,7,8,9,10.
	rtts := []int{0, 1, 3, 6, 10, 15, 21, 28, 36, 45, 55}
	for i, r := range rtts {
		b.Add(sample("waw-1", uint64(i), m0.Add(time.Duration(i)*time.Second), r, false))
	}
	closed := flushViaWatermark(b, "waw-1", m0.Add(62*time.Second))
	var a *Aggregate
	for i := range closed {
		if closed[i].Minute.Equal(m0) {
			a = &closed[i]
		}
	}
	if a == nil {
		t.Fatal("m0 not closed")
	}
	// deltas sorted = 1..10. nearest-rank: p10 -> idx ceil(.1*10)-1=0 ->1;
	// p50 -> idx ceil(.5*10)-1=4 ->5; p99 -> idx ceil(.99*10)-1=9 ->10.
	if a.JitterP10MS != 1 || a.JitterP50MS != 5 || a.JitterP99MS != 10 || a.JitterMaxMS != 10 {
		t.Fatalf("jitter pcts p10/p50/p99/max = %v/%v/%v/%v, want 1/5/10/10",
			a.JitterP10MS, a.JitterP50MS, a.JitterP99MS, a.JitterMaxMS)
	}
}

func TestAllLostBucket(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0, 0, true))
	b.Add(sample("waw-1", 1, m0.Add(time.Second), 0, true))
	all := b.FlushAll()
	if len(all) != 1 {
		t.Fatalf("want 1 bucket, got %d", len(all))
	}
	a := all[0]
	if a.LossPct != 100 || a.MinMS != 0 || a.JitterMS != 0 || a.JitterMaxMS != 0 {
		t.Fatalf("all-lost bucket wrong: %+v", a)
	}
}

func TestSingleSampleMinuteHasZeroJitter(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0, 42, false)) // 1 success, no delta possible
	a := b.FlushAll()[0]
	if a.Samples != 1 || a.Lost != 0 {
		t.Fatalf("counts wrong: %+v", a)
	}
	if a.AvgMS != 42 {
		t.Fatalf("avg should be the single sample: %v", a.AvgMS)
	}
	if a.JitterMS != 0 || a.JitterMaxMS != 0 || a.JitterP50MS != 0 {
		t.Fatalf("single-sample jitter must be zero (unmeasurable): %+v", a)
	}
}

func TestFlushAllEmptiesState(t *testing.T) {
	b := newTestBucketer()
	b.Add(sample("waw-1", 0, m0, 10, false))
	b.Add(sample("fra-1", 0, m0, 20, false))
	if got := b.FlushAll(); len(got) != 2 {
		t.Fatalf("want 2 flushed buckets, got %d", len(got))
	}
	if got := b.FlushAll(); len(got) != 0 {
		t.Fatalf("second flush should be empty, got %d", len(got))
	}
}
