// Package store persists per-minute aggregates in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"jitter/internal/aggregate"
)

const schema = `
CREATE TABLE IF NOT EXISTS aggregates (
	target    TEXT    NOT NULL,
	pop       TEXT    NOT NULL,
	minute    INTEGER NOT NULL, -- unix seconds, UTC minute boundary
	samples   INTEGER NOT NULL,
	lost      INTEGER NOT NULL,
	min_ms    REAL NOT NULL,
	avg_ms    REAL NOT NULL,
	max_ms    REAL NOT NULL,
	p50_ms    REAL NOT NULL,
	p95_ms    REAL NOT NULL,
	p99_ms    REAL NOT NULL,
	jitter_ms REAL NOT NULL,
	jitter_p10_ms REAL NOT NULL DEFAULT 0,
	jitter_p50_ms REAL NOT NULL DEFAULT 0,
	jitter_p99_ms REAL NOT NULL DEFAULT 0,
	jitter_max_ms REAL NOT NULL DEFAULT 0,
	loss_pct  REAL NOT NULL,
	partial   INTEGER NOT NULL,
	PRIMARY KEY (target, minute)
);
CREATE INDEX IF NOT EXISTS idx_aggregates_minute ON aggregates (minute);
`

// migrations are additive ALTERs for databases created by older versions;
// "duplicate column name" errors mean the column already exists and are
// ignored. (jitter_min_ms from an even older version is left in place, unused.)
var migrations = []string{
	`ALTER TABLE aggregates ADD COLUMN jitter_max_ms REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE aggregates ADD COLUMN jitter_p10_ms REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE aggregates ADD COLUMN jitter_p50_ms REAL NOT NULL DEFAULT 0`,
	`ALTER TABLE aggregates ADD COLUMN jitter_p99_ms REAL NOT NULL DEFAULT 0`,
}

type Store struct {
	db *sql.DB
}

// TargetInfo summarizes one known target.
type TargetInfo struct {
	Target     string    `json:"target"`
	POP        string    `json:"pop"`
	LastMinute time.Time `json:"last_minute"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// modernc.org/sqlite is happiest with a single writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	for _, m := range migrations {
		if _, err := db.Exec(m); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			db.Close()
			return nil, fmt.Errorf("apply migration %q: %w", m, err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) InsertAggregates(aggs []aggregate.Aggregate) error {
	if len(aggs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO aggregates
			(target, pop, minute, samples, lost, min_ms, avg_ms, max_ms,
			 p50_ms, p95_ms, p99_ms, jitter_ms, jitter_p10_ms, jitter_p50_ms,
			 jitter_p99_ms, jitter_max_ms, loss_pct, partial)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT (target, minute) DO UPDATE SET
			pop=excluded.pop, samples=excluded.samples, lost=excluded.lost,
			min_ms=excluded.min_ms, avg_ms=excluded.avg_ms, max_ms=excluded.max_ms,
			p50_ms=excluded.p50_ms, p95_ms=excluded.p95_ms, p99_ms=excluded.p99_ms,
			jitter_ms=excluded.jitter_ms, jitter_p10_ms=excluded.jitter_p10_ms,
			jitter_p50_ms=excluded.jitter_p50_ms, jitter_p99_ms=excluded.jitter_p99_ms,
			jitter_max_ms=excluded.jitter_max_ms, loss_pct=excluded.loss_pct,
			partial=excluded.partial`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, a := range aggs {
		if _, err := stmt.Exec(
			a.Target, a.POP, a.Minute.UTC().Unix(), a.Samples, a.Lost,
			a.MinMS, a.AvgMS, a.MaxMS, a.P50MS, a.P95MS, a.P99MS,
			a.JitterMS, a.JitterP10MS, a.JitterP50MS, a.JitterP99MS, a.JitterMaxMS,
			a.LossPct, boolToInt(a.Partial),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) History(target string, from, to time.Time) ([]aggregate.Aggregate, error) {
	rows, err := s.db.Query(`
		SELECT target, pop, minute, samples, lost, min_ms, avg_ms, max_ms,
		       p50_ms, p95_ms, p99_ms, jitter_ms, jitter_p10_ms, jitter_p50_ms,
		       jitter_p99_ms, jitter_max_ms, loss_pct, partial
		FROM aggregates
		WHERE target = ? AND minute BETWEEN ? AND ?
		ORDER BY minute ASC`,
		target, from.UTC().Unix(), to.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []aggregate.Aggregate
	for rows.Next() {
		var a aggregate.Aggregate
		var minute int64
		var partial int
		if err := rows.Scan(&a.Target, &a.POP, &minute, &a.Samples, &a.Lost,
			&a.MinMS, &a.AvgMS, &a.MaxMS, &a.P50MS, &a.P95MS, &a.P99MS,
			&a.JitterMS, &a.JitterP10MS, &a.JitterP50MS, &a.JitterP99MS, &a.JitterMaxMS,
			&a.LossPct, &partial); err != nil {
			return nil, err
		}
		a.Minute = time.Unix(minute, 0).UTC()
		a.Partial = partial != 0
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) Targets() ([]TargetInfo, error) {
	rows, err := s.db.Query(`
		SELECT target, pop, MAX(minute) FROM aggregates
		GROUP BY target ORDER BY target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TargetInfo
	for rows.Next() {
		var ti TargetInfo
		var minute int64
		if err := rows.Scan(&ti.Target, &ti.POP, &minute); err != nil {
			return nil, err
		}
		ti.LastMinute = time.Unix(minute, 0).UTC()
		out = append(out, ti)
	}
	return out, rows.Err()
}

func (s *Store) DeleteBefore(cutoff time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM aggregates WHERE minute < ?`, cutoff.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
