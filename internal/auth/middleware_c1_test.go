package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

// C1 regression: RequireAuth guards only the public-ingress routes (/api/*,
// /mcp), where X-Auth-Request-* is client-controlled — no oauth2-proxy sits in
// front to strip/overwrite it. A request bearing only a forged
// X-Auth-Request-Email must therefore NOT authenticate, even with an
// allowlisted domain and a matching groups header. Real identity must come from
// the service secret (rung 1), a Bearer token (rung 3), or the arti_session
// cookie (rung 4). Guards against reintroducing the unauthenticated
// identity-spoofing hole removed from RequireAuth.
func TestRequireAuth_RejectsForgedIngressHeader(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("test-secret"))
	mw := auth.RequireAuth(auth.NewConfig(nil, signer, "", []string{"engineers"}))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	// Forged oauth2-proxy headers only — allowlisted domain + matching group.
	req := httptest.NewRequest("GET", "/api/artifacts", nil)
	req.Header.Set(auth.IngressEmailHeader, "attacker@example.com")
	req.Header.Set(auth.IngressGroupsHeader, "engineers")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged %s must be rejected on a public-ingress route: want 401, got %d",
			auth.IngressEmailHeader, rec.Code)
	}

	// Sanity: a genuine HS256 session token still authenticates, so the rung
	// removal didn't break the real cookie/Bearer path.
	tok, err := signer.Sign(auth.Claims{Email: "alice@example.com", Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	req2 := httptest.NewRequest("GET", "/api/artifacts", nil)
	req2.Header.Set("Authorization", "Bearer "+tok)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("valid bearer token: want 200, got %d", rec2.Code)
	}
}
