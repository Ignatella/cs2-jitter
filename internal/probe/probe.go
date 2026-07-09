// Package probe turns targets into a stream of RTT samples.
package probe

import (
	"context"
	"sync"
	"time"
)

// Sample is one probe result. Lost=true means no reply within the timeout;
// RTT is zero in that case. Seq is a per-prober monotonic counter assigned at
// send time, so the consumer can reconstruct true send order even though
// probes run concurrently and complete (hence arrive) out of order.
type Sample struct {
	Target string
	POP    string
	Seq    uint64
	SentAt time.Time
	RTT    time.Duration
	Lost   bool
}

// Pinger performs a single round-trip measurement.
type Pinger interface {
	Ping(ctx context.Context, timeout time.Duration) (time.Duration, error)
}

// Prober probes one target on a fixed interval and emits Samples.
type Prober struct {
	target   string
	pop      string
	pinger   Pinger
	interval time.Duration
	timeout  time.Duration
	out      chan<- Sample
}

func New(target, pop string, p Pinger, interval, timeout time.Duration, out chan<- Sample) *Prober {
	return &Prober{target: target, pop: pop, pinger: p, interval: interval, timeout: timeout, out: out}
}

// Run blocks until ctx is done and every in-flight probe has finished.
// Each tick launches the probe in its own goroutine so a slow/lost probe
// (up to timeout) never delays the next tick. Waiting for in-flight probes
// before returning is what makes it safe for the owner to close the out
// channel after Run returns.
func (p *Prober) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s := seq
			seq++
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.probeOnce(ctx, s)
			}()
		}
	}
}

func (p *Prober) probeOnce(ctx context.Context, seq uint64) {
	sent := time.Now()
	rtt, err := p.pinger.Ping(ctx, p.timeout)
	if ctx.Err() != nil {
		return // shutting down: a cancelled probe is not a real loss
	}
	s := Sample{Target: p.target, POP: p.pop, Seq: seq, SentAt: sent}
	if err != nil {
		s.Lost = true
	} else {
		s.RTT = rtt
	}
	select {
	case p.out <- s:
	case <-ctx.Done():
	}
}
