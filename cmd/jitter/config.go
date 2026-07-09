package main

import (
	"flag"
	"strings"
	"time"
)

type config struct {
	POPs           []string
	RelaysPerPOP   int
	Interval       time.Duration
	Timeout        time.Duration
	DB             string
	Listen         string
	RetentionDays  int
	ICMPPrivileged bool
	ExtraTargets   []string
	SDRURL         string
}

// parseConfig resolves flags with env fallback (JITTER_<NAME>, dashes to
// underscores). Explicit flags win over env; env wins over defaults.
func parseConfig(args []string, getenv func(string) string) (config, error) {
	fs := flag.NewFlagSet("jitter", flag.ContinueOnError)
	pops := fs.String("pops", "waw,fra,vie", "comma-separated SDR POP codes to probe")
	relaysPerPOP := fs.Int("relays-per-pop", 2, "relays probed per POP")
	interval := fs.Duration("interval", 250*time.Millisecond, "probe interval per target")
	timeout := fs.Duration("timeout", time.Second, "probe reply timeout")
	db := fs.String("db", "./jitter.db", "SQLite database path")
	listen := fs.String("listen", ":8080", "HTTP listen address")
	retention := fs.Int("retention-days", 90, "days of history to keep")
	privileged := fs.Bool("icmp-privileged", false, "use raw ICMP sockets (needs CAP_NET_RAW)")
	extra := fs.String("extra-targets", "", "comma-separated extra IPs to probe (e.g. your router)")
	sdrURL := fs.String("sdr-url", "", "override SDR config URL (for testing)")

	// Apply env before parsing so explicit flags override.
	var envErr error
	fs.VisitAll(func(f *flag.Flag) {
		key := "JITTER_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v := getenv(key); v != "" {
			if err := fs.Set(f.Name, v); err != nil && envErr == nil {
				envErr = err
			}
		}
	})
	if envErr != nil {
		return config{}, envErr
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	return config{
		POPs:           splitList(*pops),
		RelaysPerPOP:   *relaysPerPOP,
		Interval:       *interval,
		Timeout:        *timeout,
		DB:             *db,
		Listen:         *listen,
		RetentionDays:  *retention,
		ICMPPrivileged: *privileged,
		ExtraTargets:   splitList(*extra),
		SDRURL:         *sdrURL,
	}, nil
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
