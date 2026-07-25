//go:build integration

package pgstore_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func invalidationTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

func invalidationTestName(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func waitForInvalidationBuses(t *testing.T, stores ...*pgstore.Store) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ready := true
		for _, store := range stores {
			ready = ready && store.InvalidationBusHealthy()
		}
		if ready {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("authorization invalidation buses did not become healthy")
}

func assertPermissionRevoked(t *testing.T, store *pgstore.Store, caller string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := store.HasPermission(context.Background(), caller, rbac.ManageArtifacts)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("permission remained granted after invalidation")
}

// TestAuthzInvalidation_RoleRevocationAcrossStores is the regression smoke test
// for SEC-633. Store B warms its local authorization snapshot; revoking through
// Store A must invalidate B well before the old 15-second TTL expires.
func TestAuthzInvalidation_RoleRevocationAcrossStores(t *testing.T) {
	ctx := context.Background()
	storeA := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	storeB := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	stopA := storeA.StartInvalidationBus(ctx)
	stopB := storeB.StartInvalidationBus(ctx)
	t.Cleanup(stopA)
	t.Cleanup(stopB)
	waitForInvalidationBuses(t, storeA, storeB)

	role := invalidationTestName("replica-role")
	caller := invalidationTestName("caller") + "@example.com"
	if _, err := storeA.CreateRole(ctx, role, "", []string{string(rbac.ManageArtifacts)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.DeleteRole(ctx, role) })
	if err := storeA.AssignRole(ctx, rbac.PrincipalUser, caller, role, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.UnassignRole(ctx, rbac.PrincipalUser, caller, role) })

	if ok, err := storeB.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil || !ok {
		t.Fatalf("Store B should authorize before revocation: ok=%v err=%v", ok, err)
	}
	if err := storeA.UnassignRole(ctx, rbac.PrincipalUser, caller, role); err != nil {
		t.Fatal(err)
	}
	assertPermissionRevoked(t, storeB, caller)
}

// TestAuthzInvalidation_GroupRevocationAcrossStores covers the separate group
// snapshot: removing a member must revoke a role inherited through that group.
func TestAuthzInvalidation_GroupRevocationAcrossStores(t *testing.T) {
	ctx := context.Background()
	storeA := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	storeB := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	stopA := storeA.StartInvalidationBus(ctx)
	stopB := storeB.StartInvalidationBus(ctx)
	t.Cleanup(stopA)
	t.Cleanup(stopB)
	waitForInvalidationBuses(t, storeA, storeB)

	role := invalidationTestName("group-role")
	group := invalidationTestName("group")
	caller := invalidationTestName("member") + "@example.com"
	if _, err := storeA.CreateRole(ctx, role, "", []string{string(rbac.ManageArtifacts)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.DeleteRole(ctx, role) })
	if _, err := storeA.CreateGroup(ctx, group, "", []string{caller}, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.DeleteGroup(ctx, group) })
	if err := storeA.AssignRole(ctx, rbac.PrincipalGroup, group, role, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.UnassignRole(ctx, rbac.PrincipalGroup, group, role) })

	if ok, err := storeB.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil || !ok {
		t.Fatalf("Store B should authorize group member before revocation: ok=%v err=%v", ok, err)
	}
	if _, err := storeA.UpdateGroup(ctx, group, "", nil); err != nil {
		t.Fatal(err)
	}
	assertPermissionRevoked(t, storeB, caller)
}

// TestAuthzInvalidation_NoBusFailsClosed ensures an unwired Store never serves
// its local authorization snapshot. This is the safe default for tests,
// one-shot commands, and any future caller that forgets to start the listener.
func TestAuthzInvalidation_NoBusFailsClosed(t *testing.T) {
	ctx := context.Background()
	storeA := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	storeB := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})

	role := invalidationTestName("no-bus-role")
	caller := invalidationTestName("caller") + "@example.com"
	if _, err := storeA.CreateRole(ctx, role, "", []string{string(rbac.ManageArtifacts)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.DeleteRole(ctx, role) })
	if err := storeA.AssignRole(ctx, rbac.PrincipalUser, caller, role, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.UnassignRole(ctx, rbac.PrincipalUser, caller, role) })

	if ok, err := storeB.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil || !ok {
		t.Fatalf("Store B should authorize before revocation: ok=%v err=%v", ok, err)
	}
	if err := storeA.UnassignRole(ctx, rbac.PrincipalUser, caller, role); err != nil {
		t.Fatal(err)
	}
	if ok, err := storeB.HasPermission(ctx, caller, rbac.ManageArtifacts); err != nil || ok {
		t.Fatalf("unwired Store B must deny immediately after revocation: ok=%v err=%v", ok, err)
	}
}

func TestAuthzInvalidation_LegitimateAdminAccess(t *testing.T) {
	ctx := context.Background()
	store := pgstore.New(invalidationTestPool(t), blob.NewInMemory(), pgstore.Config{})
	stop := store.StartInvalidationBus(ctx)
	t.Cleanup(stop)
	waitForInvalidationBuses(t, store)

	caller := invalidationTestName("admin") + "@example.com"
	if err := store.EnsureAdminAssignments(ctx, []string{caller}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.UnassignRole(ctx, rbac.PrincipalUser, caller, rbac.RoleAdmin)
	})

	roles, permissions, err := store.CallerAccess(ctx, caller)
	if err != nil {
		t.Fatal(err)
	}
	foundAdmin := false
	for _, role := range roles {
		if role == rbac.RoleAdmin {
			foundAdmin = true
			break
		}
	}
	if !foundAdmin {
		t.Fatalf("admin bootstrap missing ADMIN role: %v", roles)
	}
	for _, permission := range rbac.AllPermissions {
		if _, ok := permissions[string(permission)]; !ok {
			t.Fatalf("admin access missing %s", permission)
		}
	}
}
