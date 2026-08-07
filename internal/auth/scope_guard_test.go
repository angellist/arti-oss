package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

func TestIsUploadAllowed(t *testing.T) {
	cases := []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/artifacts", true},
		{"POST", "/api/artifacts/by-slug/foo/append", true},
		{"GET", "/api/artifacts/abc", true},
		{"GET", "/api/me", true},
		{"DELETE", "/api/artifacts/abc", false},
		{"PATCH", "/api/artifacts/abc", false},
		{"POST", "/api/artifacts/abc/suggest-metadata", false},
		{"GET", "/api/admin/users", false},
		{"POST", "/mcp", false},
	}
	for _, c := range cases {
		if got := isUploadAllowed(c.method, c.path); got != c.want {
			t.Errorf("%s %s: want %v got %v", c.method, c.path, c.want, got)
		}
	}
}

// uploadCtx builds a request context with an upload-scoped Claims, optionally
// with a family ID set.
func uploadCtx(email, fam string) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, ctxClaims, Claims{
		Email:  email,
		Scopes: []string{UploadScope},
		Fam:    fam,
	})
}

// okHandler is a trivial next-handler that reads the body and writes 200.
func okHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			// Surface "request body too large" to the test via a header.
			w.Header().Set("X-Body-Err", err.Error())
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// TestEnforceUploadScope_NonUploadTokenPassesThrough verifies that a request
// carrying a regular user token is not touched by the middleware.
func TestEnforceUploadScope_NonUploadTokenPassesThrough(t *testing.T) {
	st := newFakeStore()
	mw := EnforceUploadScope(st, 1<<20)

	ctx := withIdent(context.Background(), "user@example.com", []string{"user"})
	req := httptest.NewRequest("DELETE", "/api/artifacts/x", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	mw(okHandler(t)).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("non-upload token: want 200 got %d", rr.Code)
	}
}

// TestEnforceUploadScope_DeleteForbidden verifies that an upload-scoped token
// cannot issue a DELETE request (allowlist rejects it).
func TestEnforceUploadScope_DeleteForbidden(t *testing.T) {
	st := newFakeStore()
	mw := EnforceUploadScope(st, 1<<20)

	req := httptest.NewRequest("DELETE", "/api/artifacts/x", nil).
		WithContext(uploadCtx("dev@example.com", ""))
	rr := httptest.NewRecorder()

	mw(okHandler(t)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("upload token DELETE: want 403 got %d", rr.Code)
	}
}

// TestEnforceUploadScope_AdminUploadTokenDeleteForbidden encodes the
// admin-downgrade intent: even a token whose email passes IsAdmin() must
// still be blocked on DELETE by the allowlist when it carries the upload scope.
func TestEnforceUploadScope_AdminUploadTokenDeleteForbidden(t *testing.T) {
	// There is no default admin: configure one explicitly and restore the
	// empty baseline afterwards so other tests keep seeing no admins.
	adminEmail := "admin@example.com"
	SetAdminEmails([]string{adminEmail})
	t.Cleanup(func() { SetAdminEmails(nil) })
	if !IsAdmin(adminEmail) {
		t.Fatal("SetAdminEmails did not take effect")
	}

	st := newFakeStore()
	mw := EnforceUploadScope(st, 1<<20)

	req := httptest.NewRequest("DELETE", "/api/artifacts/x", nil).
		WithContext(uploadCtx(adminEmail, ""))
	rr := httptest.NewRecorder()

	mw(okHandler(t)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("admin upload token DELETE: want 403 got %d (admin-downgrade broken)", rr.Code)
	}
}

// TestEnforceUploadScope_RevokedFamilyForbidden verifies that a token whose
// family is marked revoked in the store gets a 403 even on an otherwise-allowed
// POST /api/artifacts.
func TestEnforceUploadScope_RevokedFamilyForbidden(t *testing.T) {
	st := newFakeStore()
	famID := "fam-revoked-x"
	// Seed a revoked family into the store.
	st.families[famID] = sqlc.DeviceToken{
		FamilyID: famID,
		Email:    "dev@example.com",
		Revoked:  true,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	}

	mw := EnforceUploadScope(st, 1<<20)

	req := httptest.NewRequest("POST", "/api/artifacts", nil).
		WithContext(uploadCtx("dev@example.com", famID))
	rr := httptest.NewRecorder()

	mw(okHandler(t)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("revoked family: want 403 got %d", rr.Code)
	}
}

// TestEnforceUploadScope_BodyCappedOnPost verifies that a POST body over
// maxUploadBytes causes a "request body too large" error when the next handler
// reads the body.
func TestEnforceUploadScope_BodyCappedOnPost(t *testing.T) {
	const maxBytes = 10
	st := newFakeStore()
	mw := EnforceUploadScope(st, maxBytes)

	// Body is 20 bytes, cap is 10.
	body := strings.NewReader("12345678901234567890")
	req := httptest.NewRequest("POST", "/api/artifacts", body).
		WithContext(uploadCtx("dev@example.com", ""))
	rr := httptest.NewRecorder()

	mw(okHandler(t)).ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body over cap: want 413 got %d (body err header: %s)",
			rr.Code, rr.Header().Get("X-Body-Err"))
	}
}

// TestFamClaimPreservedThroughRequireAuth is the regression test for the Fix 1
// security bug: RequireAuth must propagate the Fam claim from the verified HS256
// token into context so EnforceUploadScope can enforce family revocation on the
// access (upload) path.
//
// The test exercises the REAL middleware chain — RequireAuth → EnforceUploadScope
// — with a signed token (NOT a context-injected claim). Before Fix 1 this test
// fails because withIdent discards Fam, leaving EnforceUploadScope's revocation
// block dead; after Fix 1 withClaims preserves Fam and the revoked token is
// correctly rejected with 403.
func TestFamClaimPreservedThroughRequireAuth(t *testing.T) {
	signer := NewJWTSigner([]byte("k"))
	SetAllowedEmails([]string{"example.com"})
	t.Cleanup(func() { SetAllowedEmails([]string{"example.com", "example.org"}) })

	st := newFakeStore()

	// Seed a REVOKED family.
	const revokedFam = "fam-revoked"
	st.families[revokedFam] = sqlc.DeviceToken{
		FamilyID: revokedFam,
		Email:    "dev@example.com",
		Revoked:  true,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	}

	// Seed a live (non-revoked) family.
	const liveFam = "fam-live"
	st.families[liveFam] = sqlc.DeviceToken{
		FamilyID: liveFam,
		Email:    "dev@example.com",
		Revoked:  false,
		ExpiresAt: pgtype.Timestamptz{
			Time:  time.Now().Add(24 * time.Hour),
			Valid: true,
		},
	}

	cfg := NewConfig(nil, signer, "", nil)

	// finalHandler sets 200 when reached.
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	chain := RequireAuth(cfg)(EnforceUploadScope(st, 0)(final))

	signAndSend := func(fam string) int {
		tok, err := signer.Sign(Claims{
			Email:  "dev@example.com",
			Scopes: []string{UploadScope},
			Fam:    fam,
			TTL:    time.Hour,
		})
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		req := httptest.NewRequest("POST", "/api/artifacts", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		chain.ServeHTTP(rr, req)
		return rr.Code
	}

	// Revoked family must be blocked at 403.
	if got := signAndSend(revokedFam); got != http.StatusForbidden {
		t.Errorf("revoked family: want 403 got %d — Fam claim not reaching EnforceUploadScope", got)
	}

	// Live family must pass through to 200.
	if got := signAndSend(liveFam); got != http.StatusOK {
		t.Errorf("live family: want 200 got %d — valid upload token incorrectly rejected", got)
	}

	// Verify that a short-duration token (no Fam) still works on an allowed path.
	tok, _ := signer.Sign(Claims{
		Email:  "dev@example.com",
		Scopes: []string{UploadScope},
		TTL:    time.Hour,
	})
	req := httptest.NewRequest("POST", "/api/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("no-fam upload token: want 200 got %d", rr.Code)
	}

}
