package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

// An embed-scoped token (minted for the comments overlay and exposed in
// served-page source) must NOT authenticate the regular API/MCP routes — only
// the embed middleware, which pins it to a single artifact. A plain session
// token still works.
func TestRequireAuth_RejectsEmbedScopedToken(t *testing.T) {
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
	if code := call(mint([]string{auth.EmbedScopePrefix + "some-artifact-id"})); code != http.StatusUnauthorized {
		t.Fatalf("embed-scoped token on a session route: want 401, got %d", code)
	}
}
