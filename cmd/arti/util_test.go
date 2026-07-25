package main

import "testing"

func TestSniffStdinMIME(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The bug this fixes: `ls -l | arti add` was classified as markdown,
		// collapsing every line break. Permission strings like `-rw-r--r--`
		// must NOT read as list items (no space after the leading dash).
		{"ls -l dump", "total 704\ndrwxr-x---@ 73 tian staff 2.3K Jun 3 15:45 .\n-rw-------@ 1 tian staff 7.0K .backport\n", "text/plain"},
		{"plain prose", "Just some notes about the meeting.\nWe agreed to ship on Friday.\n", "text/plain"},
		{"empty", "", "text/plain"},

		{"atx heading", "# Title\n\nbody text\n", "text/markdown"},
		{"unordered list", "shopping:\n- milk\n- eggs\n", "text/markdown"},
		{"ordered list", "steps:\n1. clone\n2. build\n", "text/markdown"},
		{"blockquote", "> a quote\n", "text/markdown"},
		{"fenced code", "see below:\n```\ncode\n```\n", "text/markdown"},
		{"link", "see the [docs](https://example.com) for more\n", "text/markdown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffStdinMIME([]byte(c.in)); got != c.want {
				t.Errorf("sniffStdinMIME(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestGuessMIME_Binary(t *testing.T) {
	cases := map[string]string{
		"report.pdf": "application/pdf",
		"a.PNG":      "image/png",
		"b.jpeg":     "image/jpeg",
		"c.svg":      "image/svg+xml",
		"notes.md":   "text/markdown",
		"data.csv":   "text/csv",
		"weird.xyz":  "text/plain", // unknown → text/plain (unchanged default)
	}
	for path, want := range cases {
		if got := guessMIME(path); got != want {
			t.Errorf("guessMIME(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsTextualCT(t *testing.T) {
	for _, ct := range []string{"text/markdown", "text/plain; charset=utf-8", "application/json", "APPLICATION/YAML"} {
		if !isTextualCT(ct) {
			t.Errorf("isTextualCT(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"application/pdf", "image/png", "application/zip", ""} {
		if isTextualCT(ct) {
			t.Errorf("isTextualCT(%q) = true, want false", ct)
		}
	}
}
