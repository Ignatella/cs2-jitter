package sdrconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchSuccessWritesCache(t *testing.T) {
	body, err := os.ReadFile("testdata/sdrconfig.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	cache := filepath.Join(t.TempDir(), "sdr.json")
	relays, err := Fetch(context.Background(), srv.Client(), srv.URL, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(relays["waw"]) != 3 {
		t.Fatalf("waw relays = %d, want 3", len(relays["waw"]))
	}
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("cache file not written: %v", err)
	}
}

func TestFetchFallsBackToCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	body, _ := os.ReadFile("testdata/sdrconfig.json")
	cache := filepath.Join(t.TempDir(), "sdr.json")
	if err := os.WriteFile(cache, body, 0o644); err != nil {
		t.Fatal(err)
	}
	relays, err := Fetch(context.Background(), srv.Client(), srv.URL, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(relays["fra"]) != 1 {
		t.Fatalf("fra relays = %d, want 1", len(relays["fra"]))
	}
}

func TestFetchNoServerNoCache(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "missing.json")
	_, err := Fetch(context.Background(), http.DefaultClient, "http://127.0.0.1:1/nope", cache)
	if err == nil {
		t.Fatal("expected error when fetch fails and cache is absent")
	}
}
