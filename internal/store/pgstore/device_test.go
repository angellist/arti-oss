//go:build integration

package pgstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// TestDeviceFlow_CodeLifecycle exercises the device code insert→approve→take
// path against a live Postgres instance, proving the sqlc queries match the
// schema.
func TestDeviceFlow_CodeLifecycle(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// unique() is defined in store_test.go (same pgstore_test package).
	suffix := unique("dv")
	dc := "dc-" + suffix
	uc := "uc-" + suffix

	// 1. Insert a pending grant (duration=long, 10m window).
	err := st.InsertDeviceCode(ctx, sqlc.InsertDeviceCodeParams{
		DeviceCode: dc,
		UserCode:   uc,
		Duration:   "long",
		ExpiresAt:  pgTimestamptz(time.Now().Add(10 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("InsertDeviceCode: %v", err)
	}

	// GetDeviceByUserCode must return the pending row with no email yet.
	got, err := st.GetDeviceByUserCode(ctx, uc)
	if err != nil {
		t.Fatalf("GetDeviceByUserCode: %v", err)
	}
	if got.Status != "pending" {
		t.Fatalf("expected status=pending, got %q", got.Status)
	}
	if got.Email != nil {
		t.Fatalf("email should be nil before approval, got %v", got.Email)
	}
	if got.Duration != "long" {
		t.Fatalf("expected duration=long, got %q", got.Duration)
	}

	// 2. Approve the code (simulates the SSO confirm page clicking Approve).
	email := fmt.Sprintf("approver-%s@example.com", suffix)
	err = st.ApproveDeviceCode(ctx, sqlc.ApproveDeviceCodeParams{
		UserCode: uc,
		Email:    &email,
	})
	if err != nil {
		t.Fatalf("ApproveDeviceCode: %v", err)
	}

	// GetDeviceCode must now show status=approved with the email set.
	approved, err := st.GetDeviceCode(ctx, dc)
	if err != nil {
		t.Fatalf("GetDeviceCode after approve: %v", err)
	}
	if approved.Status != "approved" {
		t.Fatalf("expected status=approved, got %q", approved.Status)
	}
	if approved.Email == nil || *approved.Email != email {
		t.Fatalf("expected email=%q, got %v", email, approved.Email)
	}

	// 3. TakeApprovedDeviceCode returns the row once and atomically flips to
	// consumed — the single-use pickup guarantee.
	taken, err := st.TakeApprovedDeviceCode(ctx, dc)
	if err != nil {
		t.Fatalf("TakeApprovedDeviceCode (first): %v", err)
	}
	if taken.Status != "consumed" {
		t.Fatalf("expected status=consumed after take, got %q", taken.Status)
	}
	if !taken.Consumed {
		t.Fatalf("expected consumed=true after take")
	}

	// 3b. A second Take must return pgx.ErrNoRows — single-use, not double-use.
	_, err = st.TakeApprovedDeviceCode(ctx, dc)
	if err != pgx.ErrNoRows {
		t.Fatalf("second TakeApprovedDeviceCode: expected pgx.ErrNoRows, got %v", err)
	}
}

// TestDeviceFlow_TokenFamilyLifecycle exercises the token family insert,
// refresh rotation, and family-level revocation.
func TestDeviceFlow_TokenFamilyLifecycle(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("dtok")
	familyID := "fam-" + suffix
	email := fmt.Sprintf("user-%s@example.com", suffix)
	jti1 := "jti1-" + suffix
	jti2 := "jti2-" + suffix

	// 1. Insert a new token family (revoked=false by default).
	err := st.InsertDeviceTokenFamily(ctx, sqlc.InsertDeviceTokenFamilyParams{
		FamilyID:          familyID,
		Email:             email,
		CurrentRefreshJti: jti1,
		ExpiresAt:         pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	})
	if err != nil {
		t.Fatalf("InsertDeviceTokenFamily: %v", err)
	}

	// GetDeviceTokenFamily must return revoked=false and the initial jti.
	fam, err := st.GetDeviceTokenFamily(ctx, familyID)
	if err != nil {
		t.Fatalf("GetDeviceTokenFamily: %v", err)
	}
	if fam.Revoked {
		t.Fatalf("family should not be revoked on insert")
	}
	if fam.CurrentRefreshJti != jti1 {
		t.Fatalf("expected jti=%q, got %q", jti1, fam.CurrentRefreshJti)
	}
	if fam.Email != email {
		t.Fatalf("expected email=%q, got %q", email, fam.Email)
	}

	// 2. RotateDeviceTokenRefresh with the correct prev jti updates the jti and
	// returns rowsAffected==1 (compare-and-swap on current_refresh_jti).
	n, err := st.RotateDeviceTokenRefresh(ctx, sqlc.RotateDeviceTokenRefreshParams{
		FamilyID: familyID,
		NewJti:   jti2,
		PrevJti:  jti1,
	})
	if err != nil {
		t.Fatalf("RotateDeviceTokenRefresh: %v", err)
	}
	if n != 1 {
		t.Fatalf("RotateDeviceTokenRefresh: expected 1 row affected, got %d", n)
	}

	// 2b. Replaying the OLD jti (jti1) must now match 0 rows — the CAS rejects a
	// superseded/concurrent refresh token, the defense against rotation replay.
	n, err = st.RotateDeviceTokenRefresh(ctx, sqlc.RotateDeviceTokenRefreshParams{
		FamilyID: familyID,
		NewJti:   "jti-stale-" + suffix,
		PrevJti:  jti1, // stale
	})
	if err != nil {
		t.Fatalf("RotateDeviceTokenRefresh (stale prev): %v", err)
	}
	if n != 0 {
		t.Fatalf("RotateDeviceTokenRefresh with stale prev jti: expected 0 rows, got %d", n)
	}

	// 3. RevokeDeviceTokenFamily marks the family revoked.
	if err = st.RevokeDeviceTokenFamily(ctx, familyID); err != nil {
		t.Fatalf("RevokeDeviceTokenFamily: %v", err)
	}

	// GetDeviceTokenFamily must now show revoked=true.
	famAfterRevoke, err := st.GetDeviceTokenFamily(ctx, familyID)
	if err != nil {
		t.Fatalf("GetDeviceTokenFamily after revoke: %v", err)
	}
	if !famAfterRevoke.Revoked {
		t.Fatalf("family must be revoked after RevokeDeviceTokenFamily")
	}

	// RotateDeviceTokenRefresh on a revoked family must return 0 rows — the
	// revocation kill-switch blocks all further refresh rotation.
	n, err = st.RotateDeviceTokenRefresh(ctx, sqlc.RotateDeviceTokenRefreshParams{
		FamilyID: familyID,
		NewJti:   "jti3-" + suffix,
		PrevJti:  jti2,
	})
	if err != nil {
		t.Fatalf("RotateDeviceTokenRefresh on revoked family: %v", err)
	}
	if n != 0 {
		t.Fatalf("RotateDeviceTokenRefresh on revoked family: expected 0 rows, got %d", n)
	}
}

// TestDeviceFlow_OneActiveFamilyPerEmail proves the partial unique index
// (migration 0013) rejects a second active family for the same email — the
// DB-level guarantee behind "one active token per user" under concurrency.
func TestDeviceFlow_OneActiveFamilyPerEmail(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("duniq")
	email := fmt.Sprintf("uniq-%s@example.com", suffix)
	mk := func(fam string) sqlc.InsertDeviceTokenFamilyParams {
		return sqlc.InsertDeviceTokenFamilyParams{
			FamilyID: fam, Email: email, CurrentRefreshJti: "jti-" + fam,
			ExpiresAt: pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
		}
	}
	if err := st.InsertDeviceTokenFamily(ctx, mk("uniq-a-"+suffix)); err != nil {
		t.Fatalf("first active family should insert: %v", err)
	}
	// Second active family for the same email must violate the unique index.
	if err := st.InsertDeviceTokenFamily(ctx, mk("uniq-b-"+suffix)); err == nil {
		t.Fatal("expected unique-index violation inserting a second active family for the same email")
	}
	// After revoking the first, a new active family inserts fine (case-insensitive).
	if err := st.RevokeDeviceTokensForEmail(ctx, strings.ToUpper(email)); err != nil {
		t.Fatalf("RevokeDeviceTokensForEmail (upper-case): %v", err)
	}
	if err := st.InsertDeviceTokenFamily(ctx, mk("uniq-c-"+suffix)); err != nil {
		t.Fatalf("new active family after revoke should insert: %v", err)
	}
}

// TestDeviceFlow_RevokeByEmail proves that RevokeDeviceTokensForEmail revokes
// the user's active family (matched case-insensitively) and that a subsequent
// rotation returns 0. (At most one active family per email is allowed by the
// 0013 unique index — see TestDeviceFlow_OneActiveFamilyPerEmail.)
func TestDeviceFlow_RevokeByEmail(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	suffix := unique("drev")
	email := fmt.Sprintf("RevokeUser-%s@Example.com", suffix) // mixed case on purpose
	famID := "fam-rev-" + suffix
	jti := "jti-" + suffix

	if err := st.InsertDeviceTokenFamily(ctx, sqlc.InsertDeviceTokenFamilyParams{
		FamilyID:          famID,
		Email:             email,
		CurrentRefreshJti: jti,
		ExpiresAt:         pgTimestamptz(time.Now().Add(30 * 24 * time.Hour)),
	}); err != nil {
		t.Fatalf("InsertDeviceTokenFamily: %v", err)
	}

	// Revoke by a differently-cased spelling of the same address — must still
	// match (LOWER(email) = LOWER($1)).
	if err := st.RevokeDeviceTokensForEmail(ctx, strings.ToLower(email)); err != nil {
		t.Fatalf("RevokeDeviceTokensForEmail: %v", err)
	}

	fam, err := st.GetDeviceTokenFamily(ctx, famID)
	if err != nil {
		t.Fatalf("GetDeviceTokenFamily: %v", err)
	}
	if !fam.Revoked {
		t.Fatalf("family must be revoked after case-insensitive RevokeDeviceTokensForEmail")
	}
	n, err := st.RotateDeviceTokenRefresh(ctx, sqlc.RotateDeviceTokenRefreshParams{
		FamilyID: famID,
		NewJti:   "newjti-" + suffix,
		PrevJti:  jti,
	})
	if err != nil {
		t.Fatalf("RotateDeviceTokenRefresh on revoked: %v", err)
	}
	if n != 0 {
		t.Fatalf("rotate on revoked family: expected 0 rows, got %d", n)
	}
}
