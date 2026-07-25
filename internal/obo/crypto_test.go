package obo

import (
	"bytes"
	"testing"
	"time"
)

func TestState_SealOpenRoundTrip(t *testing.T) {
	c, err := NewCipher([]byte("master-key-for-state-roundtrip!!"))
	if err != nil {
		t.Fatal(err)
	}
	in := authState{Email: "alice@x.com", Verifier: "verif-123", Resource: "https://h/mcp", Scope: "mcp:proxy:1", IAT: time.Now().Unix()}
	tok, err := c.SealState(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.OpenState(tok)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", out, in)
	}

	// The PKCE verifier must NOT be readable from the opaque token.
	if bytes.Contains([]byte(tok), []byte(in.Verifier)) {
		t.Fatal("state token leaks the PKCE verifier in cleartext")
	}
	// Tampering breaks the GCM auth tag.
	if _, err := c.OpenState(tok[:len(tok)-2] + "AA"); err == nil {
		t.Fatal("expected tampered state to fail")
	}
	// A different master key can't open it.
	c2, _ := NewCipher([]byte("a-totally-different-master-key!!"))
	if _, err := c2.OpenState(tok); err == nil {
		t.Fatal("expected wrong-key open to fail")
	}
}

func TestColumn_SealOpenRoundTrip(t *testing.T) {
	c, _ := NewCipher([]byte("master-key-for-column-roundtrip!"))
	enc := c.SealCol("super-secret-token")
	if bytes.Contains(enc, []byte("super-secret-token")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := c.OpenCol(enc)
	if err != nil {
		t.Fatal(err)
	}
	if got != "super-secret-token" {
		t.Fatalf("got %q", got)
	}
	// Empty seals to nil (stored as NULL) and opens back to "".
	if c.SealCol("") != nil {
		t.Fatal("empty plaintext should seal to nil")
	}
	if v, err := c.OpenCol(nil); err != nil || v != "" {
		t.Fatalf("nil open: %q %v", v, err)
	}
}

func TestNewCipher_RejectsShortKey(t *testing.T) {
	if _, err := NewCipher([]byte("tooshort")); err == nil {
		t.Fatal("expected short-key rejection")
	}
}
