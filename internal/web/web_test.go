package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jitter/internal/aggregate"
	"jitter/internal/probe"
	"jitter/internal/store"
)

func setup(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	m := time.Now().UTC().Truncate(time.Minute).Add(-5 * time.Minute)
	if err := st.InsertAggregates([]aggregate.Aggregate{{
		Target: "waw-1", POP: "waw", Minute: m,
		Samples: 240, AvgMS: 20, P99MS: 30,
		JitterMS: 1.2, JitterP50MS: 0.9, JitterP99MS: 6.0, JitterMaxMS: 8.4,
	}}); err != nil {
		t.Fatal(err)
	}

	live := aggregate.NewLive(10)
	live.Add(probe.Sample{Target: "waw-1", SentAt: time.Now(), RTT: 21 * time.Millisecond})

	srv := httptest.NewServer(New(st, live, []string{"waw", "fra"}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// Targets come back in -pops priority order, extras last — not alphabetical.
func TestTargetsPriorityOrder(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := time.Now().UTC().Truncate(time.Minute)
	if err := st.InsertAggregates([]aggregate.Aggregate{
		{Target: "extra-1.1.1.1", POP: "extra", Minute: m, Samples: 240},
		{Target: "fra-2", POP: "fra", Minute: m, Samples: 240},
		{Target: "waw-9", POP: "waw", Minute: m, Samples: 240},
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, aggregate.NewLive(10), []string{"waw", "fra"}).Handler())
	t.Cleanup(srv.Close)

	var targets []store.TargetInfo
	getJSON(t, srv.URL+"/api/targets", &targets)
	if len(targets) != 3 ||
		targets[0].Target != "waw-9" || targets[1].Target != "fra-2" ||
		targets[2].Target != "extra-1.1.1.1" {
		t.Fatalf("wrong order: %+v", targets)
	}
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp
}

func TestHealthz(t *testing.T) {
	srv := setup(t)
	resp := getJSON(t, srv.URL+"/healthz", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestTargets(t *testing.T) {
	srv := setup(t)
	var targets []store.TargetInfo
	getJSON(t, srv.URL+"/api/targets", &targets)
	if len(targets) != 1 || targets[0].Target != "waw-1" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestHistoryDefaultsAndFilter(t *testing.T) {
	srv := setup(t)
	var aggs []aggregate.Aggregate
	getJSON(t, srv.URL+"/api/history?target=waw-1", &aggs)
	if len(aggs) != 1 || aggs[0].AvgMS != 20 || aggs[0].JitterMaxMS != 8.4 {
		t.Fatalf("history = %+v", aggs)
	}
	resp := getJSON(t, srv.URL+"/api/history", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing target: status = %d, want 400", resp.StatusCode)
	}
}

func TestLive(t *testing.T) {
	srv := setup(t)
	var pts []aggregate.LivePoint
	getJSON(t, srv.URL+"/api/live?target=waw-1", &pts)
	if len(pts) != 1 || pts[0].RTTms != 21 {
		t.Fatalf("live = %+v", pts)
	}
}

func TestIndexServed(t *testing.T) {
	srv := setup(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(strings.Builder)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(buf.String(), "<title>jitter</title>") {
		t.Fatalf("index not served, status %d", resp.StatusCode)
	}
}
