package probe

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakePinger cycles through scripted results.
type fakePinger struct {
	mu   sync.Mutex
	rtts []time.Duration // 0 means "return error"
	i    int
}

func (f *fakePinger) Ping(ctx context.Context, timeout time.Duration) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rtts[f.i%len(f.rtts)]
	f.i++
	if r == 0 {
		return 0, errors.New("timeout")
	}
	return r, nil
}

func TestProberEmitsSamplesAndMarksLoss(t *testing.T) {
	out := make(chan Sample, 100)
	fp := &fakePinger{rtts: []time.Duration{10 * time.Millisecond, 0, 20 * time.Millisecond}}
	p := New("waw-192.0.2.10", "waw", fp, 2*time.Millisecond, 50*time.Millisecond, out)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p.Run(ctx) // blocks until ctx expires AND all in-flight probes finish
	close(out) // safe: Run waits for its probe goroutines

	var samples []Sample
	for s := range out {
		samples = append(samples, s)
	}
	if len(samples) < 3 {
		t.Fatalf("got %d samples, want >= 3", len(samples))
	}
	var sawLost, sawOK bool
	for _, s := range samples {
		if s.Target != "waw-192.0.2.10" || s.POP != "waw" {
			t.Fatalf("bad identity on sample: %+v", s)
		}
		if s.Lost {
			sawLost = true
			if s.RTT != 0 {
				t.Fatalf("lost sample has RTT: %+v", s)
			}
		} else {
			sawOK = true
			if s.RTT <= 0 {
				t.Fatalf("ok sample missing RTT: %+v", s)
			}
		}
	}
	if !sawLost || !sawOK {
		t.Fatalf("want both lost and ok samples, lost=%v ok=%v", sawLost, sawOK)
	}
}
