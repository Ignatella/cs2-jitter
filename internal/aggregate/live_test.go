package aggregate

import (
	"testing"
	"time"

	"jitter/internal/probe"
)

func TestLiveKeepsLastN(t *testing.T) {
	l := NewLive(5)
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		l.Add(probe.Sample{Target: "waw-1", SentAt: base.Add(time.Duration(i) * time.Second),
			RTT: time.Duration(i) * time.Millisecond})
	}
	got := l.Recent("waw-1")
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	if got[0].RTTms != 7 || got[4].RTTms != 11 {
		t.Fatalf("wrong window: first=%v last=%v", got[0].RTTms, got[4].RTTms)
	}
	if pts := l.Recent("unknown"); len(pts) != 0 {
		t.Fatal("unknown target should be empty")
	}
}
