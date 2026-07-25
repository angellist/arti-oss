package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

// An app-scoped token (minted for an APP artifact's in-page bridge and exposed
// in served-page source) must NOT authenticate the regular API/MCP routes —
// only the apps proxy, which pins it to a single app + its tool allowlist.
// Mirrors TestRequireAuth_RejectsEmbedScopedToken; guards against the
// privilege-escalation where a scraped app token drives the full API as the
// viewer.
func TestRequireAuth_RejectsAppScopedToken(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("test-secret"))
	mw := auth.RequireAuth(auth.NewConfig(nil, signer, "", nil))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	mint := func(scopes []string) string {
		tok, err := signer.Sign(auth.Claims{Email: "alice@example.com", Scopes: scopes, TTL: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	call := func(tok string) int {
		req := httptest.NewRequest("GET", "/api/artifacts", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := call(mint([]string{"user"})); code != http.StatusOK {
		t.Fatalf("session token: want 200, got %d", code)
	}
	if code := call(mint([]string{auth.AppScopePrefix + "some-app-id"})); code != http.StatusUnauthorized {
		t.Fatalf("app-scoped token on a session route: want 401, got %d", code)
	}
}

func TestIsAppScoped(t *testing.T) {
	app := auth.Claims{Scopes: []string{auth.AppScopePrefix + "abc"}}
	if !app.IsAppScoped() {
		t.Fatal("app-scoped claims should report IsAppScoped()=true")
	}
	if app.IsEmbedScoped() {
		t.Fatal("app-scoped claims must not be mistaken for embed-scoped")
	}
	session := auth.Claims{Scopes: []string{"user"}}
	if session.IsAppScoped() {
		t.Fatal("session claims must not report IsAppScoped()=true")
	}
}
