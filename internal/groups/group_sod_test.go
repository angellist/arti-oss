//go:build integration

package groups_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/angellist/arti-oss/internal/rbac"
)

// H8: membership + lifecycle of a PRIVILEGED group (one carrying an RBAC role)
// require MANAGE_USER_GROUPS, not mere ownership — otherwise a non-admin owner
// could add members (or a wildcard) and silently hand them the role, or delete
// and recreate the role-bearing name with attacker-chosen members.
func TestGroups_PrivilegedMembershipSoD(t *testing.T) {
	r, st := testRouter(t)
	ctx := context.Background()
	const alice, admin = "alice@a.com", "admin@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	// alice owns the group but is NOT a member, so a role on the group doesn't
	// grant her the permission and confound the gate.
	name := unique("priv")
	if w := do(t, r, alice, "POST", "/api/groups", `{"name":"`+name+`","members":["m@a.com"]}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	// Non-privileged group: the owner may edit membership (unchanged behavior).
	if w := do(t, r, alice, "PATCH", "/api/groups/"+name, `{"members":["m@a.com","x@a.com"]}`); w.Code != http.StatusOK {
		t.Fatalf("owner edit of non-privileged group: want 200, got %d %s", w.Code, w.Body.String())
	}

	// Admin grants a role to the group -> it becomes privileged.
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, name, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalGroup, name, rbac.RoleAdmin) })

	// Owner (non-admin) can no longer change membership...
	if w := do(t, r, alice, "PATCH", "/api/groups/"+name, `{"members":["m@a.com","evil@a.com"]}`); w.Code != http.StatusForbidden {
		t.Errorf("owner membership edit of privileged group: want 403, got %d %s", w.Code, w.Body.String())
	}
	// ...but display-name-only edits stay owner-allowed.
	if w := do(t, r, alice, "PATCH", "/api/groups/"+name, `{"display_name":"renamed"}`); w.Code != http.StatusOK {
		t.Errorf("owner display-name edit of privileged group: want 200, got %d %s", w.Code, w.Body.String())
	}
	// ...and the owner can't delete it (delete+recreate vector).
	if w := do(t, r, alice, "DELETE", "/api/groups/"+name, ""); w.Code != http.StatusForbidden {
		t.Errorf("owner delete of privileged group: want 403, got %d", w.Code)
	}

	// Admin (MANAGE_USER_GROUPS) can change membership and delete.
	if w := do(t, r, admin, "PATCH", "/api/groups/"+name, `{"members":["m@a.com","trusted@a.com"]}`); w.Code != http.StatusOK {
		t.Errorf("admin membership edit of privileged group: want 200, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, r, admin, "DELETE", "/api/groups/"+name, ""); w.Code != http.StatusNoContent {
		t.Errorf("admin delete of privileged group: want 204, got %d", w.Code)
	}

	// The role assignment persists past the delete, so a non-admin can't recreate
	// the privileged name with their own members.
	if w := do(t, r, alice, "POST", "/api/groups", `{"name":"`+name+`","members":["evil@a.com"]}`); w.Code != http.StatusForbidden {
		t.Errorf("non-admin recreate of role-bearing name: want 403, got %d %s", w.Code, w.Body.String())
	}
}
