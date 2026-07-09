// Package web serves the JSON API and the embedded dashboard.
package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	"jitter/internal/aggregate"
	"jitter/internal/store"
)

//go:embed static
var staticFS embed.FS

type Server struct {
	store    *store.Store
	live     *aggregate.Live
	popOrder map[string]int
}

// New builds the server. popPriority is the -pops order; /api/targets sorts
// by it so the dashboard shows POPs in the order the user cares about,
// with extra targets (and unknown POPs) last.
func New(st *store.Store, live *aggregate.Live, popPriority []string) *Server {
	order := make(map[string]int, len(popPriority))
	for i, p := range popPriority {
		order[p] = i
	}
	return &Server{store: st, live: live, popOrder: order}
}

func (s *Server) popRank(pop string) int {
	if r, ok := s.popOrder[pop]; ok {
		return r
	}
	return len(s.popOrder)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/targets", s.handleTargets)
	mux.HandleFunc("GET /api/history", s.handleHistory)
	mux.HandleFunc("GET /api/live", s.handleLive)
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServerFS(static))
	return mux
}

func (s *Server) handleTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.Targets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if targets == nil {
		targets = []store.TargetInfo{}
	}
	slices.SortStableFunc(targets, func(a, b store.TargetInfo) int {
		if d := s.popRank(a.POP) - s.popRank(b.POP); d != 0 {
			return d
		}
		return strings.Compare(a.Target, b.Target)
	})
	writeJSON(w, targets)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing target", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	from, err := parseTime(r.URL.Query().Get("from"), now.Add(-24*time.Hour))
	if err != nil {
		http.Error(w, "bad from", http.StatusBadRequest)
		return
	}
	to, err := parseTime(r.URL.Query().Get("to"), now)
	if err != nil {
		http.Error(w, "bad to", http.StatusBadRequest)
		return
	}
	aggs, err := s.store.History(target, from, to)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if aggs == nil {
		aggs = []aggregate.Aggregate{}
	}
	writeJSON(w, aggs)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "missing target", http.StatusBadRequest)
		return
	}
	pts := s.live.Recent(target)
	if pts == nil {
		pts = []aggregate.LivePoint{}
	}
	writeJSON(w, pts)
}

func parseTime(v string, def time.Time) (time.Time, error) {
	if v == "" {
		return def, nil
	}
	return time.Parse(time.RFC3339, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
