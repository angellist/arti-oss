package obo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A spec-compliant AS (RFC 9728 protected-resource → RFC 8414 AS metadata) is
// discovered and keyed by its reported issuer.
func TestDiscover_ResolvesViaProtectedResource(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"authorization_servers": []string{base}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 base,
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
			"registration_endpoint":  base + "/register",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	b := New("https://arti.example.com", nil, newMemStore(), testCipher(t))
	m, err := b.discover(context.Background(), base+"/api/v1/proxy/X/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if m.Issuer != base || m.Token != base+"/token" {
		t.Fatalf("unexpected metadata: %+v", m)
	}
}

// A non-compliant AS that omits the required issuer must error rather than fall
// back to a path-dependent key (which would split DCR rows across pods).
func TestDiscover_RequiresIssuer(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	// No protected-resource doc → origin fallback. AS metadata omits issuer.
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_endpoint": base + "/authorize",
			"token_endpoint":         base + "/token",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	b := New("https://arti.example.com", nil, newMemStore(), testCipher(t))
	if _, err := b.discover(context.Background(), base+"/mcp"); err == nil {
		t.Fatal("expected discovery to fail when the AS omits the required issuer")
	}
}
