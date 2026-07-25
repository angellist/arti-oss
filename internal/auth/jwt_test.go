package auth_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

func TestJWT_RoundTrip(t *testing.T) {
	s := auth.NewJWTSigner([]byte("k"))
	tok, err := s.Sign(auth.Claims{Email: "alice@example.com", Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	c, err := s.Verify(tok)
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "alice@example.com" {
		t.Fatalf("email=%q", c.Email)
	}
}

func TestJWT_Expired(t *testing.T) {
	s := auth.NewJWTSigner([]byte("k"))
	tok, _ := s.Sign(auth.Claims{Email: "x@a.com", TTL: -time.Second})
	if _, err := s.Verify(tok); !errors.Is(err, auth.ErrExpired) {
		t.Fatalf("expected ErrExpired; got %v", err)
	}
}

func TestJWT_BadSig(t *testing.T) {
	a := auth.NewJWTSigner([]byte("k1"))
	b := auth.NewJWTSigner([]byte("k2"))
	tok, _ := a.Sign(auth.Claims{Email: "x@a.com", TTL: time.Hour})
	if _, err := b.Verify(tok); !errors.Is(err, auth.ErrBadSignature) {
		t.Fatalf("expected ErrBadSignature; got %v", err)
	}
}

func TestValidateSigningKey(t *testing.T) {
	// A 32-byte (256-bit) key is the production floor; anything weaker,
	// empty, or the in-repo dev default must refuse to boot — UNLESS auth
	// is disabled (local dev runs ARTI_AUTH_DISABLED=true with the dev key).
	good := strings.Repeat("a", 32)
	cases := []struct {
		name         string
		key          string
		authDisabled bool
		wantErr      bool
	}{
		{"empty rejected", "", false, true},
		{"dev-default rejected", auth.DevDefaultSigningKey, false, true},
		{"too short rejected", strings.Repeat("a", 31), false, true},
		{"32 bytes ok", good, false, false},
		{"long random ok", strings.Repeat("z", 64), false, false},
		// AuthDisabled (local dev) exempts every weak/missing key.
		{"empty ok when auth disabled", "", true, false},
		{"dev-default ok when auth disabled", auth.DevDefaultSigningKey, true, false},
		{"too short ok when auth disabled", "x", true, false},
	}
	for _, c := range cases {
		err := auth.ValidateSigningKey(c.key, c.authDisabled)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: ValidateSigningKey(len=%d, authDisabled=%v) err=%v, wantErr=%v",
				c.name, len(c.key), c.authDisabled, err, c.wantErr)
		}
	}
}

func TestAllowlist(t *testing.T) {
	// Suite allowlist (TestMain): the reserved example domains.
	cases := []struct {
		email string
		want  bool
	}{
		{"alice@example.com", true},
		{"BOB@example.com", true},
		{"carol@example.org", true},
		{"DAVE@example.org", true},
		{"eve@gmail.com", false},
		{"bad@example.com.evil.com", false},
		{"noatsign", false},
	}
	for _, c := range cases {
		if got := auth.IsAllowed(c.email); got != c.want {
			t.Errorf("IsAllowed(%q)=%v want %v", c.email, got, c.want)
		}
	}
}

func TestAllowlist_Configurable(t *testing.T) {
	// Restore the suite baseline (TestMain) at the end of the test.
	t.Cleanup(func() { auth.SetAllowedDomains([]string{"example.com", "example.org"}) })

	auth.SetAllowedDomains([]string{"example.com", "Partner.com"})
	if !auth.IsAllowed("alice@partner.com") {
		t.Error("partner.com should be allowed after SetAllowedDomains")
	}
	if !auth.IsAllowed("alice@example.com") {
		t.Error("example.com should still be allowed")
	}
	if auth.IsAllowed("eve@gmail.com") {
		t.Error("gmail.com should still be denied")
	}

	// There is no baked-in default: an empty allowlist denies everyone
	// (fail closed), it does not fall back to any organization's domains.
	auth.SetAllowedDomains(nil)
	if auth.IsAllowed("alice@example.com") {
		t.Error("empty allowlist must deny example.com (fail closed)")
	}
	if auth.IsAllowed("alice@partner.com") {
		t.Error("empty allowlist must deny partner.com (fail closed)")
	}
}
