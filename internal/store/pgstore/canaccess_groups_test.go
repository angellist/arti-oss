package pgstore_test

import (
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// TestCanAccess_GroupGrant pins the core promise of group access: an artifact
// granted ONLY to a group is readable by a current member and by no one else.
// If group resolution regresses (e.g. callerGroups stops being consulted),
// either the allow or the deny assertion fails.
func TestCanAccess_GroupGrant(t *testing.T) {
	row := sqlc.Artifact{Creator: "owner@a.com", AllowedAccess: []string{"group:eng"}}

	if !pgstore.CanAccess(row, "dev@a.com", []string{"group:eng"}) {
		t.Error("an eng member must read a group:eng-granted artifact")
	}
	if pgstore.CanAccess(row, "outsider@a.com", nil) {
		t.Error("a caller in no groups must NOT read a group-only artifact")
	}
	if pgstore.CanAccess(row, "outsider@a.com", []string{"group:design"}) {
		t.Error("membership in an unrelated group must not grant access")
	}
}

// TestCanAccess_GroupTokenExactMatch pins that group tokens match EXACTLY, not
// case-insensitively — this is deliberate so the Go check agrees with the SQL
// paths (`allowed_access && $::text[]` overlap is case-sensitive). Tokens are
// canonical lowercase on both sides (names normalized on write; CallerGroups
// builds tokens from them), so a stray non-canonical `group:Eng` fails closed
// everywhere alike rather than matching in Go but not in SQL.
func TestCanAccess_GroupTokenExactMatch(t *testing.T) {
	if !pgstore.CanAccess(sqlc.Artifact{AllowedAccess: []string{"group:eng"}}, "dev@a.com", []string{"group:eng"}) {
		t.Error("canonical token must match exactly")
	}
	if pgstore.CanAccess(sqlc.Artifact{AllowedAccess: []string{"group:Eng"}}, "dev@a.com", []string{"group:eng"}) {
		t.Error("non-canonical (uppercase) token must NOT match — Go must agree with the case-sensitive SQL overlap")
	}
}

// TestCanAccess_NonGroupPathsUnchanged ensures adding groups didn't regress the
// pre-existing creator / exact-email / domain-glob paths.
func TestCanAccess_NonGroupPathsUnchanged(t *testing.T) {
	row := sqlc.Artifact{Creator: "owner@a.com", AllowedAccess: []string{"alice@a.com", "*@partner.io"}}

	if !pgstore.CanAccess(row, "owner@a.com", nil) {
		t.Error("creator must always pass")
	}
	if !pgstore.CanAccess(row, "alice@a.com", nil) {
		t.Error("exact-email grant must pass")
	}
	if !pgstore.CanAccess(row, "bob@partner.io", nil) {
		t.Error("domain glob must pass")
	}
	if pgstore.CanAccess(row, "eve@evil.com", []string{"group:eng"}) {
		t.Error("a group the row doesn't reference must not grant access")
	}
}

// TestCanAccess_EmptyCallerDenied keeps the auth-disabled/empty-caller case
// fail-closed even when the caller is handed group tokens.
func TestCanAccess_EmptyCallerDenied(t *testing.T) {
	row := sqlc.Artifact{AllowedAccess: []string{"*"}}
	if pgstore.CanAccess(row, "", []string{"group:eng"}) {
		t.Error("empty caller must be denied")
	}
}
