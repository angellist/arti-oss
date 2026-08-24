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

// TestUsers_RosterGatedAndEnumerates pins criterion 1 plus the disclosure
// boundary that matters: /api/users is the ONE surface allowed to enumerate
// principals, and only behind MANAGE_ROLES. A non-holder must get 404, not 403,
// so the surface is not discoverable — the same choice the rest of this package
// makes.
func TestUsers_RosterGatedAndEnumerates(t *testing.T) {
	r, st := setup(t)
	ctx := context.Background()
	const admin, plain = "rosteradmin@a.com", "rosterplain@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	if w := do(t, r, plain, "GET", "/api/users", ""); w.Code != http.StatusNotFound {
		t.Errorf("roster without MANAGE_ROLES: want 404, got %d", w.Code)
	}
	if w := do(t, r, plain, "POST", "/api/users", `{"email":"x@y.com"}`); w.Code != http.StatusNotFound {
		t.Errorf("add-user without MANAGE_ROLES: want 404, got %d", w.Code)
	}

	w := do(t, r, admin, "GET", "/api/users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin roster: want 200, got %d (%s)", w.Code, w.Body.String())
	}
	var got struct {
		Users []struct {
			Email string `json:"email"`
			Roles []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"roles"`
			Groups    []string `json:"groups"`
			IdPGroups []string `json:"idp_groups"`
		} `json:"users"`
		Sources []string `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	// The admin was just assigned a role, so the roster must contain them —
	// role_assignments is one of the arms.
	var found bool
	for _, u := range got.Users {
		if u.Email == admin {
			found = true
			if u.Groups == nil || u.IdPGroups == nil {
				t.Error("group slices must marshal as [] not null, so clients need not handle absent")
			}
		}
	}
	if !found {
		t.Errorf("roster is missing %q, who holds a direct role", admin)
	}
	if len(got.Sources) == 0 {
		t.Error("the response must name the sources it read, so a missing principal class is diagnosable")
	}
}

// TestUsers_AddUserRecordsWithoutGranting pins that the add-user path records a
// principal and grants nothing. This is the whole reason the users table exists:
// AssignRole REJECTS the USER role, so a role assignment cannot express "this
// principal exists at baseline privilege".
func TestUsers_AddUserRecordsWithoutGranting(t *testing.T) {
	r, st := setup(t)
	ctx := context.Background()
	const admin = "rosteradmin2@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	email := unique("sa") + "@example.com"
	w := do(t, r, admin, "POST", "/api/users",
		`{"email":"`+email+`","kind":"service","note":"platform team"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("add user: want 201, got %d (%s)", w.Code, w.Body.String())
	}
	var u struct {
		Email      string `json:"email"`
		Kind       string `json:"kind"`
		Note       string `json:"note"`
		AddedBy    string `json:"added_by"`
		Registered bool   `json:"registered"`
		Roles      []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
		} `json:"roles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if u.Email != email || u.Kind != "service" || u.Note != "platform team" || !u.Registered {
		t.Errorf("registration not reflected: %+v", u)
	}
	if u.AddedBy != admin {
		t.Errorf("added_by = %q, want the calling admin", u.AddedBy)
	}
	for _, role := range u.Roles {
		if role.Source != "baseline" {
			t.Errorf("adding a user must grant nothing beyond the baseline; got %+v", u.Roles)
		}
	}

	// A pattern is not a principal and must be refused.
	if w := do(t, r, admin, "POST", "/api/users", `{"email":"*@example.com"}`); w.Code != http.StatusBadRequest {
		t.Errorf("glob email: want 400, got %d", w.Code)
	}
}

// TestUsers_RosterAgreesWithRoleLookup pins criterion 3 across the HTTP
// boundary: the roster and /api/role-lookup must report the same role sources
// for the same person, because they now share one computation.
func TestUsers_RosterAgreesWithRoleLookup(t *testing.T) {
	r, st := setup(t)
	ctx := context.Background()
	const admin = "rosteradmin3@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	subject := unique("both") + "@example.com"
	group := unique("rgrp")
	if _, err := st.CreateGroup(ctx, group, "roster fixture", []string{subject}, admin); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeleteGroup(ctx, group) })
	if err := st.AssignRole(ctx, rbac.PrincipalUser, subject, rbac.RoleAdmin, admin); err != nil {
		t.Fatal(err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, group, rbac.RoleAdmin, admin); err != nil {
		t.Fatal(err)
	}

	sources := func(body []byte, path string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		var rl struct {
			Roles []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"roles"`
		}
		if err := json.Unmarshal(body, &rl); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		for _, x := range rl.Roles {
			out[x.Name+"/"+x.Source] = true
		}
		return out
	}

	lw := do(t, r, admin, "GET", "/api/role-lookup?email="+url.QueryEscape(subject), "")
	if lw.Code != http.StatusOK {
		t.Fatalf("role-lookup: %d (%s)", lw.Code, lw.Body.String())
	}
	fromLookup := sources(lw.Body.Bytes(), "role-lookup")

	rw := do(t, r, admin, "GET", "/api/users", "")
	var roster struct {
		Users []json.RawMessage `json:"users"`
	}
	if err := json.Unmarshal(rw.Body.Bytes(), &roster); err != nil {
		t.Fatal(err)
	}
	var fromRoster map[string]bool
	for _, raw := range roster.Users {
		var probe struct {
			Email string `json:"email"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Email == subject {
			fromRoster = sources(raw, "roster")
		}
	}
	if fromRoster == nil {
		t.Fatalf("roster has no row for %q", subject)
	}
	if len(fromRoster) != len(fromLookup) {
		t.Errorf("roster and role-lookup disagree:\n roster=%v\n lookup=%v", fromRoster, fromLookup)
	}
	for k := range fromLookup {
		if !fromRoster[k] {
			t.Errorf("roster is missing %q, which role-lookup reports", k)
		}
	}
}
