package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

func TestRequireAuthOrRedirect_BrowserNavigation(t *testing.T) {
	mw := auth.RequireAuthOrRedirect(auth.NewConfig(nil, nil, "", nil), "/auth/login")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/app/demo?view=full", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", rec.Code)
	}
	want := "/auth/login?return_to=" + url.QueryEscape("/app/demo?view=full")
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestRequireAuthOrRedirect_APIMissingCredentials(t *testing.T) {
	mw := auth.RequireAuthOrRedirect(auth.NewConfig(nil, nil, "", nil), "/auth/login")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("body = %q, want unauthorized JSON", rec.Body.String())
	}
}

func TestRequireAuthOrRedirect_AuthorizationHeaderStaysJSON(t *testing.T) {
	mw := auth.RequireAuthOrRedirect(auth.NewConfig(nil, nil, "", nil), "/auth/login")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts", nil)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Authorization", "Bearer invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "" {
		t.Fatalf("unexpected redirect Location %q", rec.Header().Get("Location"))
	}
}

func TestRequireAuthOrRedirect_ValidCookiePassesThrough(t *testing.T) {
	signer := auth.NewJWTSigner([]byte("test-secret"))
	tok, err := signer.Sign(auth.Claims{Email: "alice@example.com", Scopes: []string{"user"}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	mw := auth.RequireAuthOrRedirect(auth.NewConfig(nil, signer, "", nil), "/auth/login")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/app/demo", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: tok})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}
