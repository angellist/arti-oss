//go:build integration

package roles_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/roles"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func unique(p string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return p + "-" + hex.EncodeToString(b)
}

func setup(t *testing.T) (*chi.Mux, *pgstore.Store) {
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
	roles.NewService(st).Mount(r)
	return r, st
}

func do(t *testing.T, r *chi.Mux, caller, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(auth.WithIdentity(req.Context(), caller))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestRoles_ManageRolesGating pins that the role API is invisible (404) to a
// caller without MANAGE_ROLES, and usable by one who holds ADMIN.
func TestRoles_ManageRolesGating(t *testing.T) {
	r, st := setup(t)
	const admin, plain = "radmin@a.com", "plain@a.com"
	if err := st.AssignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	if w := do(t, r, plain, "GET", "/api/roles", ""); w.Code != http.StatusNotFound {
		t.Errorf("non-MANAGE_ROLES caller: want 404, got %d", w.Code)
	}
	if w := do(t, r, admin, "GET", "/api/roles", ""); w.Code != http.StatusOK {
		t.Errorf("admin GET /api/roles: want 200, got %d", w.Code)
	}
}

// TestRoles_CRUDAndGuards exercises create/update/delete + the built-in guard
// and unknown-permission rejection through the HTTP layer.
func TestRoles_CRUDAndGuards(t *testing.T) {
	r, st := setup(t)
	const admin = "radmin2@a.com"
	if err := st.AssignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(context.Background(), rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	name := unique("editor")
	body := `{"name":"` + name + `","description":"x","permissions":["USE_ARTIFACTS"]}`
	if w := do(t, r, admin, "POST", "/api/roles", body); w.Code != http.StatusCreated {
		t.Fatalf("create role: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = st.DeleteRole(context.Background(), name) })

	// Unknown permission key rejected.
	if w := do(t, r, admin, "PATCH", "/api/roles/"+name, `{"permissions":["BOGUS"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown permission: want 400, got %d", w.Code)
	}
	// Built-in role can't be deleted.
	if w := do(t, r, admin, "DELETE", "/api/roles/"+rbac.RoleAdmin, ""); w.Code != http.StatusBadRequest {
		t.Errorf("delete ADMIN: want 400, got %d", w.Code)
	}
}

// TestRoles_AssignAndLookup pins assignment + the per-user lookup, including a
// group-derived role labeled with its source.
func TestRoles_AssignAndLookup(t *testing.T) {
	ctx := context.Background()
	r, st := setup(t)
	const admin, alice = "radmin3@a.com", "rlookup@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	// Assign ADMIN to alice directly via the API.
	assign := `{"principal_type":"user","principal_id":"` + alice + `","role_name":"ADMIN"}`
	if w := do(t, r, admin, "POST", "/api/role-assignments", assign); w.Code != http.StatusNoContent {
		t.Fatalf("assign: want 204, got %d (%s)", w.Code, w.Body.String())
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, alice, rbac.RoleAdmin) })

	// Lookup alice: she should hold USER (baseline) + ADMIN (direct).
	w := do(t, r, admin, "GET", "/api/role-lookup?email="+alice, "")
	if w.Code != http.StatusOK {
		t.Fatalf("lookup: want 200, got %d", w.Code)
	}
	var resp struct {
		Roles []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"roles"`
		Effective []string `json:"effective_permissions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	srcOf := map[string]string{}
	for _, r := range resp.Roles {
		srcOf[r.Name] = r.Source
	}
	if srcOf[rbac.RoleUser] != "baseline" {
		t.Errorf("USER role source: want baseline, got %q", srcOf[rbac.RoleUser])
	}
	if srcOf[rbac.RoleAdmin] != "direct" {
		t.Errorf("ADMIN role source: want direct, got %q", srcOf[rbac.RoleAdmin])
	}
	if len(resp.Effective) != len(rbac.AllPermissions) {
		t.Errorf("ADMIN holder should have all %d permissions, got %d", len(rbac.AllPermissions), len(resp.Effective))
	}
}

// TestRoles_LookupShowsGroupDerivedRole pins that the per-user lookup surfaces
// a role a person holds VIA a group (source group:<name>), separately from any
// direct assignment of the same role.
func TestRoles_LookupShowsGroupDerivedRole(t *testing.T) {
	ctx := context.Background()
	r, st := setup(t)
	const admin, member = "ladmin@a.com", "groupmember@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	g := unique("lkg")
	if _, err := st.CreateGroup(ctx, g, "", []string{member}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, g, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}

	w := do(t, r, admin, "GET", "/api/role-lookup?email="+member, "")
	if w.Code != http.StatusOK {
		t.Fatalf("lookup: want 200, got %d", w.Code)
	}
	var resp struct {
		Roles []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var foundGroupAdmin bool
	for _, role := range resp.Roles {
		if role.Name == rbac.RoleAdmin && role.Source == "group:"+g {
			foundGroupAdmin = true
		}
	}
	if !foundGroupAdmin {
		t.Errorf("lookup must show ADMIN sourced from group:%s; got %+v", g, resp.Roles)
	}
}

// TestRoles_NoSelfDemotion pins that an admin can't unassign ADMIN from
// themselves (lockout foot-gun), but can unassign it from someone else.
func TestRoles_NoSelfDemotion(t *testing.T) {
	ctx := context.Background()
	r, st := setup(t)
	const admin, other = "selfdemote@a.com", "other-admin@a.com"
	for _, e := range []string{admin, other} {
		if err := st.AssignRole(ctx, rbac.PrincipalUser, e, rbac.RoleAdmin, "test"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, e, rbac.RoleAdmin) })
	}

	unassignPath := func(email string) string {
		return "/api/role-assignments?principal_type=user&role_name=ADMIN&principal_id=" + url.QueryEscape(email)
	}
	if w := do(t, r, admin, "DELETE", unassignPath(admin), ""); w.Code != http.StatusBadRequest {
		t.Errorf("self-unassign of ADMIN: want 400, got %d", w.Code)
	}
	if w := do(t, r, admin, "DELETE", unassignPath(other), ""); w.Code != http.StatusNoContent {
		t.Errorf("unassigning ADMIN from another user: want 204, got %d", w.Code)
	}
}
