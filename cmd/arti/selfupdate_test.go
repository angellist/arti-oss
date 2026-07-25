package main

import "testing"

func TestStale(t *testing.T) {
	const full = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	cases := []struct {
		name          string
		built, latest string
		want          bool
	}{
		{"unknown build never nags", "", full, false},
		{"unknown latest is skipped", full, "", false},
		{"equal full SHAs are current", full, full, false},
		{"abbreviated build matches latest prefix", "a1b2c3d", full, false},
		{"different commit is stale", "deadbeefdeadbeef", full, true},
		{"abbrev of a different commit is stale", "deadbee", full, true},
	}
	for _, c := range cases {
		if got := stale(c.built, c.latest); got != c.want {
			t.Errorf("%s: stale(%q,%q)=%v, want %v", c.name, c.built, c.latest, got, c.want)
		}
	}
}
