package sdrconfig

import (
	"os"
	"testing"
)

func loadFixture(t *testing.T) map[string][]Relay {
	t.Helper()
	data, err := os.ReadFile("testdata/sdrconfig.json")
	if err != nil {
		t.Fatal(err)
	}
	relays, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return relays
}

func TestParse(t *testing.T) {
	relays := loadFixture(t)
	if len(relays["waw"]) != 3 {
		t.Fatalf("waw relays = %d, want 3", len(relays["waw"]))
	}
	r := relays["waw"][0]
	if r.POP != "waw" || r.IP != "192.0.2.10" || r.PortMin != 27015 || r.PortMax != 27200 {
		t.Fatalf("unexpected first waw relay: %+v", r)
	}
	if len(relays["ordc"]) != 0 {
		t.Fatalf("pop without relays should yield none, got %d", len(relays["ordc"]))
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte(`{"pops": {}}`)); err == nil {
		t.Fatal("empty pops should error")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("invalid json should error")
	}
}

func TestSelect(t *testing.T) {
	all := loadFixture(t)
	got := Select(all, []string{"waw", "fra", "nope"}, 2)
	if len(got) != 3 { // 2 from waw, 1 from fra, 0 from unknown pop
		t.Fatalf("selected %d relays, want 3", len(got))
	}
	if got[0].POP != "waw" || got[2].POP != "fra" {
		t.Fatalf("unexpected selection order: %+v", got)
	}
}
