// Command relaycheck fetches Valve's live SDR relay list and pings a few
// relays once, printing a table. Manual smoke/debug tool.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"jitter/internal/probe"
	"jitter/internal/sdrconfig"
)

func main() {
	pops := flag.String("pops", "waw,fra,vie", "comma-separated POP codes")
	count := flag.Int("count", 5, "pings per relay")
	privileged := flag.Bool("icmp-privileged", false, "use raw ICMP sockets")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cache := filepath.Join(os.TempDir(), "relaycheck-sdr.json")
	all, err := sdrconfig.Fetch(ctx, &http.Client{Timeout: 30 * time.Second}, sdrconfig.DefaultURL, cache)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch sdr config:", err)
		os.Exit(1)
	}
	fmt.Printf("SDR config: %d POPs available\n\n", len(all))

	relays := sdrconfig.Select(all, splitList(*pops), 1)
	if len(relays) == 0 {
		fmt.Fprintln(os.Stderr, "no relays for pops:", *pops)
		os.Exit(1)
	}

	fmt.Printf("%-6s %-18s %-9s %-9s %-9s %s\n", "POP", "RELAY", "MIN", "AVG", "MAX", "LOSS")
	for _, r := range relays {
		pinger := &probe.ICMP{Addr: r.IP, Privileged: *privileged}
		var rtts []time.Duration
		lost := 0
		for i := 0; i < *count; i++ {
			rtt, err := pinger.Ping(ctx, time.Second)
			if err != nil {
				lost++
			} else {
				rtts = append(rtts, rtt)
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(rtts) == 0 {
			fmt.Printf("%-6s %-18s %-9s %-9s %-9s %d/%d\n", r.POP, r.IP, "-", "-", "-", lost, *count)
			continue
		}
		lo, hi, sum := rtts[0], rtts[0], time.Duration(0)
		for _, v := range rtts {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
			sum += v
		}
		avg := sum / time.Duration(len(rtts))
		fmt.Printf("%-6s %-18s %-9s %-9s %-9s %d/%d\n",
			r.POP, r.IP, fmtMS(lo), fmtMS(avg), fmtMS(hi), lost, *count)
	}
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func fmtMS(d time.Duration) string {
	return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond))
}
