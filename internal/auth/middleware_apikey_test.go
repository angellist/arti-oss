package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/angellist/arti-oss/internal/auth"
)

// stubAPIKeyAuthenticator is a test double for auth.APIKeyAuthenticator.
type stubAPIKeyAuthenticator struct {
	claims auth.Claims
	err    error
}

func (s *stubAPIKeyAuthenticator) Authenticate(_ context.Context, _ string) (auth.Claims, error) {
	return s.claims, s.err
}

func TestRequireAuth_APIKeyRung_Valid(t *testing.T) {
	stub := &stubAPIKeyAuthenticator{
		claims: auth.Claims{
			Email:  "alice@example.com",
			Scopes: []string{"upload"},
			Typ:    "api-key",
		},
	}
	cfg := auth.NewConfig(nil, nil, "", nil)
	cfg.APIKeys = stub
	mw := auth.RequireAuth(cfg)

	var gotClaims auth.Claims
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := auth.ClaimsFromContext(r.Context())
		if !ok {
			t.Error("ClaimsFromContext: expected claims in context, got none")
		}
		gotClaims = c
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer arti_upload_sometoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	if gotClaims.Email != "alice@example.com" {
		t.Errorf("Email = %q, want alice@example.com", gotClaims.Email)
	}
	if len(gotClaims.Scopes) != 1 || gotClaims.Scopes[0] != "upload" {
		t.Errorf("Scopes = %v, want [upload]", gotClaims.Scopes)
	}
	if gotClaims.Typ != "api-key" {
		t.Errorf("Typ = %q, want api-key", gotClaims.Typ)
	}
}

func TestRequireAuth_APIKeyRung_AuthenticateError(t *testing.T) {
	stub := &stubAPIKeyAuthenticator{err: errors.New("invalid key")}
	cfg := auth.NewConfig(nil, nil, "", nil)
	cfg.APIKeys = stub
	mw := auth.RequireAuth(cfg)

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer arti_upload_badtoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestRequireAuth_APIKeyRung_NilMeansSkipped(t *testing.T) {
	// With cfg.APIKeys == nil, an arti_ token must NOT be accepted via the API-key
	// rung. It will fall through to OIDC/HS256, both of which are also nil here,
	// so the result is 401 "invalid token" (not 401 "invalid api key").
	cfg := auth.NewConfig(nil, nil, "", nil)
	// cfg.APIKeys left nil
	mw := auth.RequireAuth(cfg)

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer arti_upload_sometoken")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}
