//go:build integration

package groups_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/groups"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func unique(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func testRouter(t *testing.T) (*chi.Mux, *pgstore.Store) {
	t.Helper()
	url := os.Getenv("ARTI_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable"
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	r := chi.NewRouter()
	groups.NewService(st).Mount(r)
	return r, st
}

// do issues a request attributed to `caller` (mirrors the auth-disabled
// middleware, which injects the identity into the request context).
func do(t *testing.T, r *chi.Mux, caller, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), caller))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func listHasGroup(t *testing.T, r *chi.Mux, caller, name string) bool {
	t.Helper()
	w := do(t, r, caller, "GET", "/api/groups", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list as %s: status %d", caller, w.Code)
	}
	var resp struct {
		Groups []struct {
			Name string `json:"name"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, g := range resp.Groups {
		if g.Name == name {
			return true
		}
	}
	return false
}

// TestGroups_OwnershipAuthz pins the ownership rules: anyone can create a group
// (becoming its owner), an owner and admins can see + edit it, and a different
// non-admin user can neither see it in their list nor edit/delete it (404, so
// it isn't even discoverable).
func TestGroups_OwnershipAuthz(t *testing.T) {
	r, st := testRouter(t)

	const alice, bob, admin = "alice@a.com", "bob@a.com", "admin@a.com"
	// Admin-ness for groups now comes from holding MANAGE_USER_GROUPS — assign
	// the ADMIN role to the admin user (and revoke after) instead of the old
	// auth.SetAdminEmails allowlist.
	if err := st.AssignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.UnassignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin)
	})
	name := unique("own")

	// Any authenticated caller can create — alice owns it.
	if w := do(t, r, alice, "POST", "/api/groups", `{"name":"`+name+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("alice create: status %d body %s", w.Code, w.Body.String())
	}

	// Visibility: owner + admin see it; an unrelated user does not.
	if !listHasGroup(t, r, alice, name) {
		t.Error("owner must see their own group")
	}
	if !listHasGroup(t, r, admin, name) {
		t.Error("admin must see every group")
	}
	if listHasGroup(t, r, bob, name) {
		t.Error("a non-owner non-admin must NOT see someone else's group")
	}

	// Management by a non-owner is rejected as 404 (not discoverable).
	if w := do(t, r, bob, "PATCH", "/api/groups/"+name, `{"display_name":"x"}`); w.Code != http.StatusNotFound {
		t.Errorf("bob PATCH alice's group: want 404, got %d", w.Code)
	}
	if w := do(t, r, bob, "DELETE", "/api/groups/"+name, ""); w.Code != http.StatusNotFound {
		t.Errorf("bob DELETE alice's group: want 404, got %d", w.Code)
	}

	// Owner and admin can manage it.
	if w := do(t, r, alice, "PATCH", "/api/groups/"+name, `{"display_name":"By Alice"}`); w.Code != http.StatusOK {
		t.Errorf("alice PATCH her group: want 200, got %d", w.Code)
	}
	if w := do(t, r, admin, "PATCH", "/api/groups/"+name, `{"display_name":"By Admin"}`); w.Code != http.StatusOK {
		t.Errorf("admin PATCH any group: want 200, got %d", w.Code)
	}
	if w := do(t, r, alice, "DELETE", "/api/groups/"+name, ""); w.Code != http.StatusNoContent {
		t.Errorf("alice DELETE her group: want 204, got %d", w.Code)
	}
}
