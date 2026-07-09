// Package sdrconfig fetches and parses Valve's SDR network config
// (relay POPs and their IPs) from the Steam Web API.
package sdrconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// DefaultURL is Valve's public, unauthenticated SDR config endpoint for CS2.
const DefaultURL = "https://api.steampowered.com/ISteamApps/GetSDRConfig/v1?appid=730"

// Relay is one Valve relay address inside a POP.
type Relay struct {
	POP     string
	IP      string
	PortMin int
	PortMax int
}

type relayJSON struct {
	IPv4      string `json:"ipv4"`
	PortRange []int  `json:"port_range"`
}

type popJSON struct {
	Desc   string      `json:"desc"`
	Relays []relayJSON `json:"relays"`
}

type configJSON struct {
	Pops map[string]popJSON `json:"pops"`
}

// Parse decodes a GetSDRConfig response body into relays grouped by POP code.
func Parse(data []byte) (map[string][]Relay, error) {
	var cfg configJSON
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse sdr config: %w", err)
	}
	if len(cfg.Pops) == 0 {
		return nil, errors.New("sdr config has no pops")
	}
	out := make(map[string][]Relay, len(cfg.Pops))
	for name, pop := range cfg.Pops {
		relays := make([]Relay, 0, len(pop.Relays))
		for _, r := range pop.Relays {
			if r.IPv4 == "" {
				continue
			}
			rel := Relay{POP: name, IP: r.IPv4}
			if len(r.PortRange) == 2 {
				rel.PortMin, rel.PortMax = r.PortRange[0], r.PortRange[1]
			}
			relays = append(relays, rel)
		}
		out[name] = relays
	}
	return out, nil
}

// Fetch downloads the SDR config and parses it. On success the raw body is
// cached at cachePath (best effort). On any failure it falls back to the
// cached copy so restarts survive Steam API outages.
func Fetch(ctx context.Context, client *http.Client, url, cachePath string) (map[string][]Relay, error) {
	relays, fetchErr := fetchLive(ctx, client, url, cachePath)
	if fetchErr == nil {
		return relays, nil
	}
	data, cacheErr := os.ReadFile(cachePath)
	if cacheErr != nil {
		return nil, fmt.Errorf("sdr config fetch failed (%v) and no cache: %w", fetchErr, cacheErr)
	}
	return Parse(data)
}

func fetchLive(ctx context.Context, client *http.Client, url, cachePath string) (map[string][]Relay, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sdr config: http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	relays, err := Parse(data)
	if err != nil {
		return nil, err
	}
	_ = os.WriteFile(cachePath, data, 0o644)
	return relays, nil
}

// Select picks up to perPOP relays from each requested POP, in the order
// the POPs were requested. Unknown POP codes are skipped silently.
func Select(all map[string][]Relay, pops []string, perPOP int) []Relay {
	var out []Relay
	for _, p := range pops {
		relays := all[p]
		n := min(perPOP, len(relays))
		out = append(out, relays[:n]...)
	}
	return out
}
