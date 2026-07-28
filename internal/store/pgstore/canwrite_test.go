package pgstore

import (
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

func TestCanWrite_NullMirrorsRead(t *testing.T) {
	// allowed_write NULL (nil) → write follows read: a reader can write.
	row := sqlc.Artifact{Creator: "owner@example.com", AllowedAccess: []string{"alice@example.com"}, AllowedWrite: nil}
	if !CanWrite(row, "alice@example.com", nil) {
		t.Fatal("nil allowed_write must mirror allowed_access (alice reads → alice writes)")
	}
	if CanWrite(row, "mallory@example.com", nil) {
		t.Fatal("non-reader must not write when mirroring")
	}
	if !CanWrite(row, "owner@example.com", nil) {
		t.Fatal("creator always writes")
	}
}

func TestCanWrite_EmptyIsCreatorOnly(t *testing.T) {
	// Non-nil empty slice → creator-only writes even though alice can read.
	row := sqlc.Artifact{Creator: "owner@example.com", AllowedAccess: []string{"alice@example.com"}, AllowedWrite: []string{}}
	if !CanWrite(row, "owner@example.com", nil) {
		t.Fatal("creator always writes")
	}
	if CanWrite(row, "alice@example.com", nil) {
		t.Fatal("empty allowed_write means creator-only; alice (reader) must not write")
	}
}

func TestCanWrite_ExplicitListAndGroup(t *testing.T) {
	row := sqlc.Artifact{Creator: "owner@example.com",
		AllowedAccess: []string{"alice@example.com", "group:eng", "bob@example.com"},
		AllowedWrite:  []string{"group:eng"}}
	if !CanWrite(row, "someone@example.com", []string{"group:eng"}) {
		t.Fatal("member of a write-granted group must write")
	}
	if CanWrite(row, "bob@example.com", nil) {
		t.Fatal("bob can read but is not in allowed_write → no write")
	}
}

func TestCanWrite_EmptyCallerDenied(t *testing.T) {
	row := sqlc.Artifact{Creator: "owner@example.com", AllowedAccess: []string{"*"}, AllowedWrite: nil}
	if CanWrite(row, "", nil) {
		t.Fatal("empty caller must never write")
	}
}
