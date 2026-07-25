package mcp

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestReadUpTo(t *testing.T) {
	const data = "hello world" // 11 bytes
	cases := []struct {
		name      string
		maxBytes  int
		wantBody  string
		wantTrunc bool
	}{
		{"no cap reads all", 0, "hello world", false},
		{"negative cap reads all", -5, "hello world", false},
		{"cap above size reads all", 50, "hello world", false},
		{"cap equal to size reads all, not truncated", 11, "hello world", false},
		{"cap below size truncates", 5, "hello", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, trunc, err := readUpTo(strings.NewReader(data), c.maxBytes)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if string(body) != c.wantBody {
				t.Errorf("body = %q, want %q", body, c.wantBody)
			}
			if trunc != c.wantTrunc {
				t.Errorf("truncated = %v, want %v", trunc, c.wantTrunc)
			}
		})
	}
}

// A byte cap can land mid-rune; trimPartialRune must drop the dangling bytes so
// the returned text is valid UTF-8 (no replacement char) — otherwise a peek of
// multibyte text would surface garbage.
func TestTrimPartialRune(t *testing.T) {
	// "é" is 0xC3 0xA9. Cut after the lead byte → must drop it.
	if got := trimPartialRune([]byte("h\xc3")); string(got) != "h" {
		t.Errorf("split rune: got %q, want %q", got, "h")
	}
	// Complete sequences are left untouched.
	if got := trimPartialRune([]byte("héllo")); string(got) != "héllo" {
		t.Errorf("valid utf8 altered: got %q", got)
	}
	// A 3-byte rune (€ = 0xE2 0x82 0xAC) cut after 2 bytes → drop both.
	if got := trimPartialRune([]byte("a\xe2\x82")); string(got) != "a" {
		t.Errorf("split 3-byte rune: got %q, want %q", got, "a")
	}
}

func TestContentReply(t *testing.T) {
	size := int64(42)
	sha := "deadbeef"

	// Textual content comes back as text, with full-artifact size + sha.
	r := contentReply([]byte("hi"), "text/markdown", &size, &sha, false)
	blocks := r["content"].([]map[string]any)
	if blocks[0]["text"] != "hi" {
		t.Errorf("text = %v, want hi", blocks[0]["text"])
	}
	if r["size_bytes"] != size {
		t.Errorf("size_bytes = %v, want %d", r["size_bytes"], size)
	}
	if r["sha256"] != sha {
		t.Errorf("sha256 = %v, want %s", r["sha256"], sha)
	}
	if r["returned_bytes"] != 2 {
		t.Errorf("returned_bytes = %v, want 2", r["returned_bytes"])
	}
	if r["truncated"] != false {
		t.Errorf("truncated = %v, want false", r["truncated"])
	}

	// Binary content is base64-encoded; returned_bytes counts raw bytes.
	bin := []byte{0xff, 0xfe, 0x00}
	rb := contentReply(bin, "application/octet-stream", nil, nil, true)
	bb := rb["content"].([]map[string]any)
	if bb[0]["text"] != base64.StdEncoding.EncodeToString(bin) {
		t.Errorf("binary not base64: %v", bb[0]["text"])
	}
	if rb["returned_bytes"] != 3 {
		t.Errorf("returned_bytes = %v, want 3 (raw bytes, not base64 len)", rb["returned_bytes"])
	}
	if _, ok := rb["size_bytes"]; ok {
		t.Errorf("size_bytes should be omitted when totalBytes is nil")
	}
	if rb["truncated"] != true {
		t.Errorf("truncated = %v, want true", rb["truncated"])
	}
}
