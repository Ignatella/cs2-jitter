package main

import (
	"testing"
	"time"
)

func TestParseConfigDefaults(t *testing.T) {
	cfg, err := parseConfig([]string{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.POPs[0] != "waw" || len(cfg.POPs) != 3 {
		t.Fatalf("default pops = %v", cfg.POPs)
	}
	if cfg.Interval != 250*time.Millisecond || cfg.RelaysPerPOP != 2 ||
		cfg.RetentionDays != 90 || cfg.Listen != ":8080" || cfg.DB != "./jitter.db" {
		t.Fatalf("bad defaults: %+v", cfg)
	}
}

func TestParseConfigEnvAndFlagPrecedence(t *testing.T) {
	env := map[string]string{
		"JITTER_POPS":     "sto,ams",
		"JITTER_INTERVAL": "500ms",
		"JITTER_LISTEN":   ":9999",
	}
	cfg, err := parseConfig([]string{"-listen", ":7777"}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.POPs) != 2 || cfg.POPs[0] != "sto" {
		t.Fatalf("env pops not applied: %v", cfg.POPs)
	}
	if cfg.Interval != 500*time.Millisecond {
		t.Fatalf("env interval not applied: %v", cfg.Interval)
	}
	if cfg.Listen != ":7777" { // flag beats env
		t.Fatalf("flag should beat env: %v", cfg.Listen)
	}
}

func TestParseConfigExtraTargets(t *testing.T) {
	cfg, err := parseConfig([]string{"-extra-targets", "192.168.1.1, 1.1.1.1"}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ExtraTargets) != 2 || cfg.ExtraTargets[1] != "1.1.1.1" {
		t.Fatalf("extra targets = %v", cfg.ExtraTargets)
	}
}
