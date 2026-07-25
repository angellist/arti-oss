//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func hasPerm(t *testing.T, st *pgstore.Store, caller string, p rbac.Permission) bool {
	t.Helper()
	ok, err := st.HasPermission(context.Background(), caller, p)
	if err != nil {
		t.Fatalf("HasPermission(%s,%s): %v", caller, p, err)
	}
	return ok
}

// TestRBAC_BaselineUser pins that every authenticated caller gets the USER
// baseline (USE_ARTIFACTS) with no assignment, and nothing more.
func TestRBAC_BaselineUser(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	const who = "nobody@a.com"
	if !hasPerm(t, st, who, rbac.UseArtifacts) {
		t.Error("every user must hold USE_ARTIFACTS via the USER baseline")
	}
	if hasPerm(t, st, who, rbac.ManageArtifacts) {
		t.Error("an unassigned user must NOT hold MANAGE_ARTIFACTS")
	}
	if hasPerm(t, st, "", rbac.UseArtifacts) {
		t.Error("an empty (unauthenticated) caller must hold nothing")
	}
}

// TestRBAC_UserAssignment + live propagation: assigning ADMIN grants its
// permissions immediately; unassigning revokes them on the next check.
func TestRBAC_UserAssignmentLive(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	const who = "alice@a.com"

	if hasPerm(t, st, who, rbac.ManageArtifacts) {
		t.Fatal("precondition: alice should not be admin yet")
	}
	if err := st.AssignRole(ctx, rbac.PrincipalUser, "Alice@a.com", rbac.RoleAdmin, "tester"); err != nil {
		t.Fatal(err)
	}
	if !hasPerm(t, st, who, rbac.ManageArtifacts) || !hasPerm(t, st, who, rbac.ManageRoles) {
		t.Error("ADMIN assignment must grant its permissions immediately (case-insensitive email)")
	}
	if err := st.UnassignRole(ctx, rbac.PrincipalUser, who, rbac.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if hasPerm(t, st, who, rbac.ManageArtifacts) {
		t.Error("unassigning must revoke the permission on the next check (live)")
	}
}

// TestRBAC_GroupDerivedRole is the key composition: a role assigned to a GROUP
// is held by its members — including members matched by a glob.
func TestRBAC_GroupDerivedRole(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	gname := unique("rbacg")
	if _, err := st.CreateGroup(ctx, gname, "", []string{"*@example.org"}, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, gname, rbac.RoleAdmin, "tester"); err != nil {
		t.Fatal(err)
	}
	// A belltower caller is a (glob) group member, so they inherit ADMIN.
	if !hasPerm(t, st, "gabe@example.org", rbac.ManageArtifacts) {
		t.Error("a member of a group assigned ADMIN must inherit MANAGE_ARTIFACTS")
	}
	if hasPerm(t, st, "outsider@a.com", rbac.ManageArtifacts) {
		t.Error("a non-member must not inherit the group's role")
	}

	// Assigning a role to a NON-existent group is rejected (dead assignment).
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, unique("nope"), rbac.RoleAdmin, "t"); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("assigning a role to a missing group must be rejected, got %v", err)
	}
	// A pasted "group:<slug>" token is accepted (normalized to the bare slug).
	if err := st.AssignRole(ctx, rbac.PrincipalGroup, pgstore.GroupToken(gname), rbac.RoleAdmin, "t"); err != nil {
		t.Errorf("assigning via a group:<slug> token must work, got %v", err)
	}
}

// TestRBAC_UserRoleImmutable pins that the USER baseline can't be edited or
// assigned: it's the fixed default applied to everyone, so a permission change
// would silently shift all users' access, and an explicit assignment is a no-op.
func TestRBAC_UserRoleImmutable(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	if _, err := st.UpdateRole(ctx, rbac.RoleUser, "", []string{string(rbac.UseArtifacts), string(rbac.ManageUserGroups)}); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("editing the USER role must be rejected, got %v", err)
	}
	if err := st.AssignRole(ctx, rbac.PrincipalUser, "x@a.com", rbac.RoleUser, "t"); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("assigning the USER role must be rejected, got %v", err)
	}
	// The baseline is intact and still grants USE_ARTIFACTS to everyone.
	if !hasPerm(t, st, "anyone@a.com", rbac.UseArtifacts) {
		t.Error("USER baseline must still grant USE_ARTIFACTS")
	}
}

// TestRBAC_RoleValidationAndGuards pins unknown-permission rejection and the
// built-in delete guard.
func TestRBAC_RoleValidationAndGuards(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	if _, err := st.CreateRole(ctx, unique("bad"), "", []string{"NOT_A_REAL_PERMISSION"}); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("unknown permission key must be rejected, got %v", err)
	}
	if err := st.DeleteRole(ctx, rbac.RoleAdmin); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("deleting a built-in role must be rejected, got %v", err)
	}
}

// TestRBAC_AdminRoleImmutableButAssignable pins that the ADMIN role can't be
// edited at all (permissions or description — so it can never be narrowed or
// the lockout reached), but unlike USER it CAN be assigned to principals.
func TestRBAC_AdminRoleImmutableButAssignable(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	all := []string{string(rbac.ManageRoles), string(rbac.ManageUserGroups), string(rbac.ManageArtifacts), string(rbac.UseArtifacts)}

	// Narrowing permissions is rejected...
	if _, err := st.UpdateRole(ctx, rbac.RoleAdmin, "", []string{string(rbac.UseArtifacts)}); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("narrowing ADMIN permissions must be rejected, got %v", err)
	}
	// ...and so is a description-only edit (full set unchanged) — ADMIN is fixed.
	if _, err := st.UpdateRole(ctx, rbac.RoleAdmin, "edited", all); !errors.Is(err, pgstore.ErrInvalidInput) {
		t.Errorf("editing ADMIN description must be rejected, got %v", err)
	}
	// But ADMIN is assignable (unlike USER).
	if err := st.AssignRole(ctx, rbac.PrincipalUser, "newadmin@a.com", rbac.RoleAdmin, "t"); err != nil {
		t.Errorf("ADMIN must be assignable, got %v", err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, "newadmin@a.com", rbac.RoleAdmin) })
}
