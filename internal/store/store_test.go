package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"jitter/internal/aggregate"
)

var m0 = time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

func agg(target string, minute time.Time, avg float64) aggregate.Aggregate {
	return aggregate.Aggregate{
		Target: target, POP: "waw", Minute: minute,
		Samples: 240, Lost: 2, MinMS: avg - 5, AvgMS: avg, MaxMS: avg + 5,
		P50MS: avg, P95MS: avg + 4, P99MS: avg + 5,
		JitterMS: 1.5, JitterP10MS: 0.2, JitterP50MS: 1.1, JitterP99MS: 9.0,
		JitterMaxMS: 12.5, LossPct: 0.8,
	}
}

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertAndHistory(t *testing.T) {
	s := open(t)
	err := s.InsertAggregates([]aggregate.Aggregate{
		agg("waw-1", m0, 20),
		agg("waw-1", m0.Add(time.Minute), 25),
		agg("fra-1", m0, 30),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.History("waw-1", m0, m0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("history len = %d, want 2", len(got))
	}
	if !got[0].Minute.Equal(m0) || got[0].AvgMS != 20 || got[0].POP != "waw" {
		t.Fatalf("first row wrong: %+v", got[0])
	}
	if got[0].JitterP10MS != 0.2 || got[0].JitterP99MS != 9.0 || got[0].JitterMaxMS != 12.5 {
		t.Fatalf("jitter percentiles lost in round-trip: %+v", got[0])
	}
	if got[0].Partial {
		t.Fatal("partial should round-trip as false")
	}
}

func TestUpsertReplaces(t *testing.T) {
	s := open(t)
	if err := s.InsertAggregates([]aggregate.Aggregate{agg("waw-1", m0, 20)}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAggregates([]aggregate.Aggregate{agg("waw-1", m0, 99)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.History("waw-1", m0, m0)
	if len(got) != 1 || got[0].AvgMS != 99 {
		t.Fatalf("upsert failed: %+v", got)
	}
}

func TestTargets(t *testing.T) {
	s := open(t)
	if err := s.InsertAggregates([]aggregate.Aggregate{
		agg("waw-1", m0, 20),
		agg("waw-1", m0.Add(time.Minute), 21),
		agg("fra-1", m0, 30),
	}); err != nil {
		t.Fatal(err)
	}
	targets, err := s.Targets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	for _, ti := range targets {
		if ti.Target == "waw-1" && !ti.LastMinute.Equal(m0.Add(time.Minute)) {
			t.Fatalf("waw-1 last minute wrong: %+v", ti)
		}
	}
}

// TestOpenMigratesOldSchema simulates a database created before the
// jitter_min_ms/jitter_max_ms columns existed.
func TestOpenMigratesOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE aggregates (
		target TEXT NOT NULL, pop TEXT NOT NULL, minute INTEGER NOT NULL,
		samples INTEGER NOT NULL, lost INTEGER NOT NULL,
		min_ms REAL NOT NULL, avg_ms REAL NOT NULL, max_ms REAL NOT NULL,
		p50_ms REAL NOT NULL, p95_ms REAL NOT NULL, p99_ms REAL NOT NULL,
		jitter_ms REAL NOT NULL, loss_pct REAL NOT NULL, partial INTEGER NOT NULL,
		PRIMARY KEY (target, minute));
		INSERT INTO aggregates VALUES
		('waw-1','waw',1783599600,240,0,10,11,12,11,12,12,0.5,0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on old schema: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Old row readable, new columns default to zero.
	old := time.Unix(1783599600, 0).UTC()
	got, err := s.History("waw-1", old, old)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].JitterMaxMS != 0 {
		t.Fatalf("old row wrong after migration: %+v", got)
	}
	// New rows with the new fields insert fine.
	if err := s.InsertAggregates([]aggregate.Aggregate{agg("waw-1", m0, 20)}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.History("waw-1", m0, m0)
	if len(got) != 1 || got[0].JitterMaxMS != 12.5 {
		t.Fatalf("new row wrong after migration: %+v", got)
	}
}

func TestDeleteBefore(t *testing.T) {
	s := open(t)
	if err := s.InsertAggregates([]aggregate.Aggregate{
		agg("waw-1", m0, 20),
		agg("waw-1", m0.Add(24*time.Hour), 21),
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteBefore(m0.Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("deleted %d (err %v), want 1", n, err)
	}
	got, _ := s.History("waw-1", m0, m0.Add(48*time.Hour))
	if len(got) != 1 {
		t.Fatalf("remaining rows = %d, want 1", len(got))
	}
}
