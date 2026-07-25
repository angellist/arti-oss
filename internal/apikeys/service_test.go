package apikeys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/apikeys"
	"github.com/angellist/arti-oss/internal/auth"
)

// ─── fake store ──────────────────────────────────────────────────────────────

type fakeServiceStore struct {
	inserted      []sqlc.InsertAPIKeyParams
	ownerKeys     []sqlc.ApiKey
	allKeys       []sqlc.ApiKey
	revokeN       int64 // rows affected for RevokeAPIKey (owner-scoped)
	revokeErr     error
	revokeByIDN   int64
	revokeByIDErr error
}

func (f *fakeServiceStore) InsertAPIKey(_ context.Context, arg sqlc.InsertAPIKeyParams) (sqlc.ApiKey, error) {
	f.inserted = append(f.inserted, arg)
	id := uuid.New()
	return sqlc.ApiKey{
		ID:         pgtype.UUID{Bytes: id, Valid: true},
		KeyHash:    arg.KeyHash,
		KeyPrefix:  arg.KeyPrefix,
		OwnerEmail: arg.OwnerEmail,
		Name:       arg.Name,
		Scopes:     arg.Scopes,
		CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
		ExpiresAt:  arg.ExpiresAt,
	}, nil
}

func (f *fakeServiceStore) ListAPIKeysByOwner(_ context.Context, _ string) ([]sqlc.ApiKey, error) {
	return f.ownerKeys, nil
}

func (f *fakeServiceStore) ListAllAPIKeys(_ context.Context) ([]sqlc.ApiKey, error) {
	return f.allKeys, nil
}

func (f *fakeServiceStore) RevokeAPIKey(_ context.Context, _ sqlc.RevokeAPIKeyParams) (int64, error) {
	return f.revokeN, f.revokeErr
}

func (f *fakeServiceStore) RevokeAPIKeyByID(_ context.Context, _ pgtype.UUID) (int64, error) {
	return f.revokeByIDN, f.revokeByIDErr
}

// ─── test helpers ────────────────────────────────────────────────────────────

// fakeChecker is a canManageAll implementation that returns true iff the email
// matches adminEmail. Tests that need a different checker build the router
// directly via newRouterWithChecker.
func fakeChecker(_ context.Context, email string) (bool, error) {
	return strings.EqualFold(email, adminEmail), nil
}

func newRouter(store *fakeServiceStore, maxTTL time.Duration) *chi.Mux {
	return newRouterWithChecker(store, maxTTL, fakeChecker)
}

func newRouterWithChecker(store *fakeServiceStore, maxTTL time.Duration, checker func(context.Context, string) (bool, error)) *chi.Mux {
	r := chi.NewRouter()
	apikeys.NewService(store, maxTTL, checker).Mount(r, func(h http.Handler) http.Handler { return h })
	return r
}

// do fires a request with the given caller email injected as identity.
func do(t *testing.T, r *chi.Mux, email, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), email))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// doWithClaims fires a request with full Claims injected (for Typ tests).
func doWithClaims(t *testing.T, r *chi.Mux, claims auth.Claims, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(auth.WithTestClaims(req.Context(), claims))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const (
	adminEmail   = "admin@example.com"
	regularEmail = "alice@example.com"
)

// ─── httpCreate tests ────────────────────────────────────────────────────────

func TestCreate_UploadScope_TTL3650_Clamped(t *testing.T) {
	maxTTL := 365 * 24 * time.Hour
	store := &fakeServiceStore{}
	r := newRouter(store, maxTTL)

	body := `{"name":"my-agent","scopes":["upload"],"ttl_days":3650}`
	w := do(t, r, regularEmail, "POST", "/api/keys", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Key       string `json:"key"`
		KeyPrefix string `json:"key_prefix"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Key must start with the upload prefix.
	if len(resp.Key) < len("arti_upload_") || resp.Key[:len("arti_upload_")] != "arti_upload_" {
		t.Errorf("key %q doesn't start with arti_upload_", resp.Key)
	}

	// Stored expiry must be ≤ now + maxTTL (clamped from 3650d to 365d).
	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(store.inserted))
	}
	storedExpiry := store.inserted[0].ExpiresAt.Time
	upperBound := time.Now().Add(maxTTL).Add(5 * time.Second) // small tolerance
	if storedExpiry.After(upperBound) {
		t.Errorf("expiry %v exceeds maxTTL ceiling %v", storedExpiry, upperBound)
	}
}

func TestCreate_UnsupportedScope_400(t *testing.T) {
	store := &fakeServiceStore{}
	r := newRouter(store, 365*24*time.Hour)

	body := `{"name":"bad","scopes":["full"]}`
	w := do(t, r, regularEmail, "POST", "/api/keys", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_EmptyName_400(t *testing.T) {
	store := &fakeServiceStore{}
	r := newRouter(store, 365*24*time.Hour)

	body := `{"name":"","scopes":["upload"]}`
	w := do(t, r, regularEmail, "POST", "/api/keys", body)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreate_APIKeyCaller_403(t *testing.T) {
	store := &fakeServiceStore{}
	r := newRouter(store, 365*24*time.Hour)

	body := `{"name":"second-key","scopes":["upload"]}`
	w := doWithClaims(t, r, auth.Claims{
		Email:  regularEmail,
		Scopes: []string{"upload"},
		Typ:    "api-key",
	}, "POST", "/api/keys", body)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── httpList tests ──────────────────────────────────────────────────────────

func TestList_OwnKeys(t *testing.T) {
	id := uuid.New()
	store := &fakeServiceStore{
		ownerKeys: []sqlc.ApiKey{
			{
				ID:         pgtype.UUID{Bytes: id, Valid: true},
				Name:       "k1",
				KeyPrefix:  "arti_upload_xxxxx",
				OwnerEmail: regularEmail,
				Scopes:     []string{"upload"},
				CreatedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
			},
		},
	}
	r := newRouter(store, 365*24*time.Hour)

	w := do(t, r, regularEmail, "GET", "/api/keys", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("want 1 key, got %d", len(resp))
	}
}

func TestList_AllTrue_AdminSeesAll(t *testing.T) {
	id1, id2 := uuid.New(), uuid.New()
	store := &fakeServiceStore{
		ownerKeys: []sqlc.ApiKey{
			{ID: pgtype.UUID{Bytes: id1, Valid: true}, Name: "k1", KeyPrefix: "p1",
				OwnerEmail: adminEmail, Scopes: []string{"upload"},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
		},
		allKeys: []sqlc.ApiKey{
			{ID: pgtype.UUID{Bytes: id1, Valid: true}, Name: "k1", KeyPrefix: "p1",
				OwnerEmail: adminEmail, Scopes: []string{"upload"},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
			{ID: pgtype.UUID{Bytes: id2, Valid: true}, Name: "k2", KeyPrefix: "p2",
				OwnerEmail: regularEmail, Scopes: []string{"upload"},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
		},
	}
	r := newRouter(store, 365*24*time.Hour)

	// Admin with ?all=true should see all keys (2).
	w := do(t, r, adminEmail, "GET", "/api/keys?all=true", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 2 {
		t.Errorf("admin ?all=true: want 2 keys, got %d", len(resp))
	}
}

func TestList_AllTrue_NonAdminSeesOwn(t *testing.T) {
	id := uuid.New()
	store := &fakeServiceStore{
		ownerKeys: []sqlc.ApiKey{
			{ID: pgtype.UUID{Bytes: id, Valid: true}, Name: "mine", KeyPrefix: "p",
				OwnerEmail: regularEmail, Scopes: []string{"upload"},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
		},
	}
	r := newRouter(store, 365*24*time.Hour)

	// Non-admin with ?all=true still only sees own keys.
	w := do(t, r, regularEmail, "GET", "/api/keys?all=true", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("non-admin ?all=true: want 1 (own) key, got %d", len(resp))
	}
}

// ─── httpRevoke tests ────────────────────────────────────────────────────────

func TestRevoke_Own_204(t *testing.T) {
	store := &fakeServiceStore{revokeN: 1}
	r := newRouter(store, 365*24*time.Hour)

	id := uuid.New()
	w := do(t, r, regularEmail, "DELETE", "/api/keys/"+id.String(), "")
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevoke_Other_NonAdmin_404(t *testing.T) {
	// Owner-scoped revoke returns 0 rows (other user's key).
	store := &fakeServiceStore{revokeN: 0}
	r := newRouter(store, 365*24*time.Hour)

	id := uuid.New()
	w := do(t, r, regularEmail, "DELETE", "/api/keys/"+id.String(), "")
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevoke_AdminAny_204(t *testing.T) {
	store := &fakeServiceStore{revokeByIDN: 1}
	r := newRouter(store, 365*24*time.Hour)

	id := uuid.New()
	w := do(t, r, adminEmail, "DELETE", "/api/keys/"+id.String(), "")
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevoke_InvalidID_400(t *testing.T) {
	store := &fakeServiceStore{}
	r := newRouter(store, 365*24*time.Hour)

	w := do(t, r, regularEmail, "DELETE", "/api/keys/not-a-uuid", "")
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestList_AllTrue_CheckerError_DegradesToOwn(t *testing.T) {
	// A permission-check error on ?all=true must NOT 500 — it degrades to the
	// caller's own keys (consistent with how a non-admin's ?all=true behaves),
	// so a blip never blocks a user from listing their own keys.
	id := uuid.New()
	store := &fakeServiceStore{
		ownerKeys: []sqlc.ApiKey{
			{ID: pgtype.UUID{Bytes: id, Valid: true}, Name: "mine", KeyPrefix: "p",
				OwnerEmail: regularEmail, Scopes: []string{"upload"},
				CreatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true}},
		},
	}
	errChecker := func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("db down")
	}
	r := newRouterWithChecker(store, 365*24*time.Hour, errChecker)

	w := do(t, r, regularEmail, "GET", "/api/keys?all=true", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (degrade to own), got %d: %s", w.Code, w.Body.String())
	}
	var resp []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp) != 1 {
		t.Errorf("degrade-to-own: want 1 key, got %d", len(resp))
	}
}

func TestRevoke_NonOwner_CheckerError_500(t *testing.T) {
	// Owner-scoped revoke matches nothing (revokeN=0), so we escalate to the
	// admin check — which errors → 500. Self-service own-revoke never reaches
	// this path (see TestRevoke_Own_CheckerError_StillRevokes).
	store := &fakeServiceStore{revokeN: 0}
	errChecker := func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("db down")
	}
	r := newRouterWithChecker(store, 365*24*time.Hour, errChecker)

	id := uuid.New()
	w := do(t, r, regularEmail, "DELETE", "/api/keys/"+id.String(), "")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRevoke_Own_CheckerError_StillRevokes(t *testing.T) {
	// Regression (Bugbot): revoking your OWN key (owner-scoped revoke matches,
	// revokeN=1) must succeed even if the admin permission check would error —
	// self-service must not depend on the fallible RBAC check.
	store := &fakeServiceStore{revokeN: 1}
	errChecker := func(_ context.Context, _ string) (bool, error) {
		return false, errors.New("db down")
	}
	r := newRouterWithChecker(store, 365*24*time.Hour, errChecker)

	id := uuid.New()
	w := do(t, r, regularEmail, "DELETE", "/api/keys/"+id.String(), "")
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204 (own revoke independent of checker), got %d: %s", w.Code, w.Body.String())
	}
}
