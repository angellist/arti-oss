package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

func apiKeyStatus(t *testing.T, cfg auth.Config, email string) int {
	t.Helper()
	cfg.APIKeys = &stubAPIKeyAuthenticator{claims: auth.Claims{Email: email, Scopes: []string{auth.ServiceScope}, Typ: "api-key"}}
	h := auth.RequireAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer arti_service_sometoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A service principal's address is never on the allowlist, so its key passes
// the API-key rung only through the active-service-principal check; an
// .invalid address that is not one is refused.
func TestServicePrincipalKeyPassesWithoutTheAllowlist(t *testing.T) {
	prev := auth.AllowedEmails()
	t.Cleanup(func() { auth.SetAllowedEmails(prev) })
	auth.SetAllowedEmails([]string{"example.com"})

	cfg := auth.NewConfig(nil, nil, "", nil)
	cfg.ServicePrincipal = func(_ context.Context, email string) bool { return email == "couch@sa.arti.invalid" }

	if got := apiKeyStatus(t, cfg, "couch@sa.arti.invalid"); got != http.StatusOK {
		t.Fatalf("active service principal key: %d, want 200", got)
	}
	if got := apiKeyStatus(t, cfg, "other@sa.arti.invalid"); got != http.StatusUnauthorized {
		t.Fatalf("unregistered .invalid key: %d, want 401", got)
	}
	if got := apiKeyStatus(t, cfg, "alice@example.com"); got != http.StatusOK {
		t.Fatalf("allowlisted person's key: %d, want 200", got)
	}
}

// No allowlist entry admits a .invalid address, so no interactive login can be
// completed as a service principal.
func TestIsAllowedNeverAdmitsInvalidAddresses(t *testing.T) {
	prev := auth.AllowedEmails()
	t.Cleanup(func() { auth.SetAllowedEmails(prev) })
	auth.SetAllowedEmails([]string{"sa.arti.invalid", "couch@sa.arti.invalid"})
	if auth.IsAllowed("couch@sa.arti.invalid") {
		t.Fatal("IsAllowed admitted a .invalid address")
	}
}

// The acting-for claim is carried only on the two routes that produce a version;
// anywhere else it is refused outright rather than silently ignored.
func TestActingForHeaderOnlyOnVersionRoutes(t *testing.T) {
	prev := auth.AllowedEmails()
	t.Cleanup(func() { auth.SetAllowedEmails(prev) })
	auth.SetAllowedEmails([]string{"example.com"})
	cfg := auth.NewConfig(nil, nil, "", nil)
	cfg.APIKeys = &stubAPIKeyAuthenticator{claims: auth.Claims{Email: "alice@example.com", Scopes: []string{"upload"}, Typ: "api-key"}}
	var seen string
	h := auth.RequireAuth(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = auth.ActingForFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/artifacts", http.StatusOK},
		{"POST", "/api/artifacts/by-slug/notes/append", http.StatusOK},
		{"PATCH", "/api/artifacts/00000000-0000-0000-0000-000000000000", http.StatusForbidden},
		{"POST", "/mcp", http.StatusForbidden},
	} {
		seen = ""
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer arti_upload_sometoken")
		req.Header.Set(auth.ActingForHeader, "Bob@Example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s %s: %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
		if tc.want == http.StatusOK && seen != "bob@example.com" {
			t.Fatalf("%s %s: acting-for in context = %q", tc.method, tc.path, seen)
		}
	}
}
