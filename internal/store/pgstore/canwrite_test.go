package pgstore

import (
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

const owner = "owner@example.com"

func TestCanWrite_NullMirrorsRead(t *testing.T) {
	// allowed_write NULL (nil) → write follows read: a reader can write.
	row := sqlc.Artifact{Creator: owner, AllowedAccess: []string{"alice@example.com"}, AllowedWrite: nil}
	if !CanWrite(row, "alice@example.com", nil, owner) {
		t.Fatal("nil allowed_write must mirror allowed_access (alice reads → alice writes)")
	}
	if CanWrite(row, "mallory@example.com", nil, owner) {
		t.Fatal("non-reader must not write when mirroring")
	}
	if !CanWrite(row, owner, nil, owner) {
		t.Fatal("owner always writes")
	}
}

func TestCanWrite_EmptyIsOwnerOnly(t *testing.T) {
	// Non-nil empty slice → owner-only writes even though alice can read.
	row := sqlc.Artifact{Creator: owner, AllowedAccess: []string{"alice@example.com"}, AllowedWrite: []string{}}
	if !CanWrite(row, owner, nil, owner) {
		t.Fatal("owner always writes")
	}
	if CanWrite(row, "alice@example.com", nil, owner) {
		t.Fatal("empty allowed_write means owner-only; alice (reader) must not write")
	}
}

// The write gate is anchored to the OWNER, never to row.Creator, which
// versioning reassigns to whoever pushed the latest version. Anchoring on
// Creator gave a delegated writer two escalations: keeping write after the
// owner revoked it, and locking the owner out of a document whose latest
// version someone else pushed.
func TestCanWrite_AnchoredToOwnerNotVersionCreator(t *testing.T) {
	// bob pushed the latest version, then was removed from allowed_write.
	row := sqlc.Artifact{Creator: "bob@example.com",
		AllowedAccess: []string{"bob@example.com", owner},
		AllowedWrite:  []string{}}
	if CanWrite(row, "bob@example.com", nil, owner) {
		t.Fatal("a revoked writer must not keep write access by having created the latest version")
	}
	if !CanWrite(row, owner, nil, owner) {
		t.Fatal("the owner must keep write access to a version someone else pushed")
	}
}

func TestCanWrite_ExplicitListAndGroup(t *testing.T) {
	row := sqlc.Artifact{Creator: owner,
		AllowedAccess: []string{"alice@example.com", "group:eng", "bob@example.com"},
		AllowedWrite:  []string{"group:eng"}}
	if !CanWrite(row, "someone@example.com", []string{"group:eng"}, owner) {
		t.Fatal("member of a write-granted group must write")
	}
	if CanWrite(row, "bob@example.com", nil, owner) {
		t.Fatal("bob can read but is not in allowed_write → no write")
	}
}

func TestCanWrite_EmptyCallerDenied(t *testing.T) {
	row := sqlc.Artifact{Creator: owner, AllowedAccess: []string{"*"}, AllowedWrite: nil}
	if CanWrite(row, "", nil, owner) {
		t.Fatal("empty caller must never write")
	}
}
