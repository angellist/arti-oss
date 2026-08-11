package main

import (
	"testing"

	"github.com/angellist/arti-oss/internal/pkgzip"
)

// sniffCases is shared by TestSniffStdinMIME and the parity test below.
var sniffCases = []struct {
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

func TestSniffStdinMIME(t *testing.T) {
	for _, c := range sniffCases {
		t.Run(c.name, func(t *testing.T) {
			if got := sniffStdinMIME([]byte(c.in)); got != c.want {
				t.Errorf("sniffStdinMIME(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// sniffStdinMIME and pkgzip.SniffTextContentType answer the SAME question —
// "is this text markdown or plain?" — for two different inputs (piped stdin vs
// an extensionless zip entry). cmd/arti mirrors the rule instead of importing
// it, because the CLI deliberately depends on no internal package; this test is
// what keeps the mirror honest. It is a test-only import, so the shipped `arti`
// binary's dependency graph is unchanged.
//
// If this fails, the two implementations have drifted: port the change rather
// than adjusting the expectation.
func TestSniffStdinMIME_MatchesPkgzip(t *testing.T) {
	for _, c := range sniffCases {
		t.Run(c.name, func(t *testing.T) {
			cli, srv := sniffStdinMIME([]byte(c.in)), pkgzip.SniffTextContentType([]byte(c.in))
			if cli != srv {
				t.Errorf("drift on %q: cmd/arti=%q, pkgzip=%q", c.in, cli, srv)
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
