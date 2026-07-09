// Command jitter continuously probes Valve SDR relays (and optional extra
// targets) with ICMP, aggregates per-minute RTT/jitter/loss into SQLite,
// and serves a dashboard.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"jitter/internal/aggregate"
	"jitter/internal/probe"
	"jitter/internal/sdrconfig"
	"jitter/internal/store"
	"jitter/internal/web"
)

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Getenv)
	if err != nil {
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Resolve targets.
	url := cfg.SDRURL
	if url == "" {
		url = sdrconfig.DefaultURL
	}
	cachePath := filepath.Join(filepath.Dir(cfg.DB), "sdrconfig-cache.json")
	all, err := sdrconfig.Fetch(ctx, &http.Client{Timeout: 30 * time.Second}, url, cachePath)
	if err != nil {
		return err
	}
	relays := sdrconfig.Select(all, cfg.POPs, cfg.RelaysPerPOP)
	if len(relays) == 0 && len(cfg.ExtraTargets) == 0 {
		return errors.New("no targets to probe (check --pops)")
	}

	type target struct{ name, pop, ip string }
	var targets []target
	for _, r := range relays {
		targets = append(targets, target{name: r.POP + "-" + r.IP, pop: r.POP, ip: r.IP})
	}
	for _, ip := range cfg.ExtraTargets {
		targets = append(targets, target{name: "extra-" + ip, pop: "extra", ip: ip})
	}
	for _, t := range targets {
		log.Info("probing", "target", t.name)
	}

	st, err := store.Open(cfg.DB)
	if err != nil {
		return err
	}
	defer st.Close()

	samples := make(chan probe.Sample, 1024)
	// Keep a bit more than the dashboard's max live range (10 min) so that
	// window stays fully covered as the oldest samples age out.
	live := aggregate.NewLive(int(12 * time.Minute / cfg.Interval))
	bucketer := aggregate.NewBucketer(cfg.Interval, cfg.Timeout)

	// Probers.
	var wg sync.WaitGroup
	for _, t := range targets {
		pinger := &probe.ICMP{Addr: t.ip, Privileged: cfg.ICMPPrivileged}
		p := probe.New(t.name, t.pop, pinger, cfg.Interval, cfg.Timeout, samples)
		wg.Add(1)
		go func() { defer wg.Done(); p.Run(ctx) }()
	}

	// Aggregation consumer; exits when the samples channel is closed after
	// all probers have stopped.
	aggDone := make(chan struct{})
	go func() {
		defer close(aggDone)
		for s := range samples {
			live.Add(s)
			if closed := bucketer.Add(s); len(closed) > 0 {
				if err := st.InsertAggregates(closed); err != nil {
					log.Error("insert aggregates", "err", err)
				}
			}
		}
	}()

	// Retention, daily.
	go func() {
		tick := time.NewTicker(24 * time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				cutoff := time.Now().UTC().AddDate(0, 0, -cfg.RetentionDays)
				if n, err := st.DeleteBefore(cutoff); err != nil {
					log.Error("retention", "err", err)
				} else if n > 0 {
					log.Info("retention", "deleted", n)
				}
			}
		}
	}()

	// HTTP server.
	srv := &http.Server{Addr: cfg.Listen, Handler: web.New(st, live, cfg.POPs).Handler()}
	httpErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			httpErr <- err
		}
	}()

	select {
	case err := <-httpErr:
		return err
	case <-ctx.Done():
	}

	// Graceful shutdown: stop probers, drain, flush open buckets.
	log.Info("shutting down")
	wg.Wait()
	close(samples)
	<-aggDone
	if err := st.InsertAggregates(bucketer.FlushAll()); err != nil {
		log.Error("flush aggregates", "err", err)
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutCtx)
}
