//go:build integration

package pgstore_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func hashKey(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// TestAPIKeyLifecycle exercises insert → get-by-hash → list-by-owner
// (case-insensitive) → revoke → get-by-hash returns no rows → list
// still returns the (revoked) row.
func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("ak")
	token := "arti_upload_" + suffix
	hash := hashKey(token)
	ownerEmail := fmt.Sprintf("owner-%s@example.com", suffix)

	// 1. Insert a new API key.
	row, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: ownerEmail,
		Name:       "test key " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}
	if !row.ID.Valid {
		t.Fatal("InsertAPIKey: expected a valid UUID, got zero")
	}
	if row.OwnerEmail != ownerEmail {
		t.Fatalf("InsertAPIKey: expected owner_email=%q, got %q", ownerEmail, row.OwnerEmail)
	}
	if row.RevokedAt.Valid {
		t.Fatal("InsertAPIKey: new key should not have revoked_at set")
	}

	// 2. GetAPIKeyByHash must return the active row.
	got, err := st.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash: %v", err)
	}
	if got.ID != row.ID {
		t.Fatalf("GetAPIKeyByHash: expected id=%v, got %v", row.ID, got.ID)
	}

	// 3. ListAPIKeysByOwner must find it — test case-insensitive match.
	keys, err := st.ListAPIKeysByOwner(ctx, fmt.Sprintf("OWNER-%s@example.com", suffix))
	if err != nil {
		t.Fatalf("ListAPIKeysByOwner: %v", err)
	}
	found := false
	for _, k := range keys {
		if k.ID == row.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListAPIKeysByOwner: key not found (case-insensitive check failed)")
	}

	// 4. ListAllAPIKeys must also include the row.
	all, err := st.ListAllAPIKeys(ctx)
	if err != nil {
		t.Fatalf("ListAllAPIKeys: %v", err)
	}
	foundAll := false
	for _, k := range all {
		if k.ID == row.ID {
			foundAll = true
		}
	}
	if !foundAll {
		t.Fatalf("ListAllAPIKeys: key not found")
	}

	// 5. RevokeAPIKey (owner-scoped) with correct owner revokes the key.
	n, err := st.RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{
		ID:         row.ID,
		OwnerEmail: ownerEmail,
	})
	if err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	if n != 1 {
		t.Fatalf("RevokeAPIKey: expected 1 row affected, got %d", n)
	}

	// 6. GetAPIKeyByHash must now return no rows (revoked_at IS NULL filter).
	_, err = st.GetAPIKeyByHash(ctx, hash)
	if err != pgx.ErrNoRows {
		t.Fatalf("GetAPIKeyByHash after revoke: expected pgx.ErrNoRows, got %v", err)
	}

	// 7. ListAPIKeysByOwner still returns the revoked row (UI shows it marked).
	keysAfter, err := st.ListAPIKeysByOwner(ctx, ownerEmail)
	if err != nil {
		t.Fatalf("ListAPIKeysByOwner after revoke: %v", err)
	}
	foundRevoked := false
	for _, k := range keysAfter {
		if k.ID == row.ID {
			foundRevoked = true
			if !k.RevokedAt.Valid {
				t.Fatal("ListAPIKeysByOwner after revoke: expected revoked_at to be set")
			}
		}
	}
	if !foundRevoked {
		t.Fatal("ListAPIKeysByOwner after revoke: key missing from list")
	}
}

// TestAPIKeyRevokeByID exercises the admin RevokeAPIKeyByID path.
func TestAPIKeyRevokeByID(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("akadm")
	token := "arti_upload_" + suffix
	hash := hashKey(token)
	ownerEmail := fmt.Sprintf("admin-owner-%s@example.com", suffix)

	row, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: ownerEmail,
		Name:       "admin test key " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	// RevokeAPIKeyByID (admin path) must revoke regardless of owner.
	n, err := st.RevokeAPIKeyByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("RevokeAPIKeyByID: %v", err)
	}
	if n != 1 {
		t.Fatalf("RevokeAPIKeyByID: expected 1 row affected, got %d", n)
	}

	// Confirm the key is no longer active.
	_, err = st.GetAPIKeyByHash(ctx, hash)
	if err != pgx.ErrNoRows {
		t.Fatalf("GetAPIKeyByHash after RevokeAPIKeyByID: expected pgx.ErrNoRows, got %v", err)
	}

	// Re-revoking must return 0 rows (idempotent, already revoked).
	n, err = st.RevokeAPIKeyByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("RevokeAPIKeyByID (second): %v", err)
	}
	if n != 0 {
		t.Fatalf("RevokeAPIKeyByID (already revoked): expected 0 rows, got %d", n)
	}
}

// TestAPIKeyRevokeWrongOwner proves owner-scoped revoke returns 0 rows
// (not an error) when the caller is not the key owner.
func TestAPIKeyRevokeWrongOwner(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("akown")
	token := "arti_upload_" + suffix
	hash := hashKey(token)
	ownerEmail := fmt.Sprintf("real-owner-%s@example.com", suffix)
	wrongEmail := fmt.Sprintf("attacker-%s@example.com", suffix)

	row, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: ownerEmail,
		Name:       "ownership test " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	// RevokeAPIKey with wrong owner must return 0 rows.
	n, err := st.RevokeAPIKey(ctx, sqlc.RevokeAPIKeyParams{
		ID:         row.ID,
		OwnerEmail: wrongEmail,
	})
	if err != nil {
		t.Fatalf("RevokeAPIKey (wrong owner): %v", err)
	}
	if n != 0 {
		t.Fatalf("RevokeAPIKey (wrong owner): expected 0 rows, got %d", n)
	}

	// Key must still be active.
	_, err = st.GetAPIKeyByHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetAPIKeyByHash (should still be active): %v", err)
	}
}

// TestAPIKeyTouchUpdatesLastUsed proves TouchAPIKey sets last_used_at.
func TestAPIKeyTouchUpdatesLastUsed(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("aktouch")
	token := "arti_upload_" + suffix
	hash := hashKey(token)
	ownerEmail := fmt.Sprintf("touch-owner-%s@example.com", suffix)

	row, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: ownerEmail,
		Name:       "touch test " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("InsertAPIKey: %v", err)
	}

	// Initially last_used_at should not be set.
	if row.LastUsedAt.Valid {
		t.Fatal("InsertAPIKey: last_used_at should be NULL on insert")
	}

	// Touch the key.
	if err = st.TouchAPIKey(ctx, row.ID); err != nil {
		t.Fatalf("TouchAPIKey: %v", err)
	}

	// Fetch via list to verify last_used_at is now set.
	keys, err := st.ListAPIKeysByOwner(ctx, ownerEmail)
	if err != nil {
		t.Fatalf("ListAPIKeysByOwner after touch: %v", err)
	}
	var touched *sqlc.ApiKey
	for i := range keys {
		if keys[i].ID == row.ID {
			touched = &keys[i]
		}
	}
	if touched == nil {
		t.Fatal("key not found in ListAPIKeysByOwner after touch")
	}
	if !touched.LastUsedAt.Valid {
		t.Fatal("TouchAPIKey: last_used_at should be set after touch")
	}
}

// TestAPIKeyExpiredNotReturned proves GetAPIKeyByHash excludes expired keys.
func TestAPIKeyExpiredNotReturned(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("akexp")
	token := "arti_upload_" + suffix
	hash := hashKey(token)
	ownerEmail := fmt.Sprintf("exp-owner-%s@example.com", suffix)

	// Insert a key already expired (expires_at in the past).
	_, err := st.InsertAPIKey(ctx, sqlc.InsertAPIKeyParams{
		KeyHash:    hash,
		KeyPrefix:  "arti_upload_" + suffix[:5],
		OwnerEmail: ownerEmail,
		Name:       "expired test " + suffix,
		Scopes:     []string{"upload"},
		ExpiresAt:  pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour), Valid: true},
	})
	if err != nil {
		t.Fatalf("InsertAPIKey (expired): %v", err)
	}

	// GetAPIKeyByHash must return no rows for an expired key.
	_, err = st.GetAPIKeyByHash(ctx, hash)
	if err != pgx.ErrNoRows {
		t.Fatalf("GetAPIKeyByHash (expired): expected pgx.ErrNoRows, got %v", err)
	}
}
