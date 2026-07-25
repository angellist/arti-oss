package auth

import (
	"testing"
	"time"
)

func TestIsUploadScoped(t *testing.T) {
	if !(Claims{Scopes: []string{"upload"}}).IsUploadScoped() {
		t.Fatal("upload scope not detected")
	}
	if (Claims{Scopes: []string{"user"}}).IsUploadScoped() {
		t.Fatal("user scope wrongly reported upload")
	}
}

func TestFamClaimRoundTrips(t *testing.T) {
	s := NewJWTSigner([]byte("k"))
	tok, _ := s.Sign(Claims{Email: "a@example.com", Scopes: []string{"upload"}, Fam: "fam-123", TTL: time.Minute})
	c, err := s.Verify(tok)
	if err != nil || c.Fam != "fam-123" {
		t.Fatalf("fam round-trip failed: %v fam=%q", err, c.Fam)
	}
}
