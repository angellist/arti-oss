//go:build integration

package pgstore_test

import (
	"context"
	"testing"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// UpdateAccess must union the write list into access (the ⊆ invariant) so
// read paths — which only ever consult allowed_access — stay a superset.
func TestUpdateAccess_UnionsWriteIntoAccess(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "t", ContentType: "text/plain",
		Content: []byte("hi"), Creator: "owner@example.com",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	id := pgstore.UUIDFromPG(row.ArtifactID)

	// Grant write to a token NOT present in access; expect it unioned in.
	if _, err := st.UpdateAccess(ctx, id, []string{"alice@example.com"}, []string{"group:eng"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := st.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !contains(got.AllowedAccess, "group:eng") {
		t.Fatalf("⊆ invariant: allowed_access must include the write grant; got %v", got.AllowedAccess)
	}
	if !contains(got.AllowedWrite, "group:eng") {
		t.Fatalf("allowed_write must hold group:eng; got %v", got.AllowedWrite)
	}
}

// The nil (NULL) vs empty ([]) distinction on allowed_write is load-bearing:
// NULL means "write follows read", empty means "creator-only". It must
// round-trip through Postgres intact.
func TestAllowedWrite_NullVsEmptyRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// A: no write list → NULL → read-back nil.
	rowA, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "a", ContentType: "text/plain",
		Content: []byte("a"), Creator: "owner@example.com",
	})
	if err != nil {
		t.Fatalf("put A: %v", err)
	}
	gotA, _ := st.GetByID(ctx, pgstore.UUIDFromPG(rowA.ArtifactID))
	if gotA.AllowedWrite != nil {
		t.Fatalf("NULL allowed_write must read back nil (mirror), got %v", gotA.AllowedWrite)
	}

	// B: explicit empty write list → creator-only → read-back non-nil empty.
	rowB, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "b", ContentType: "text/plain",
		Content: []byte("b"), Creator: "owner@example.com",
		AllowedAccess: []string{"*"}, AllowedWrite: []string{},
	})
	if err != nil {
		t.Fatalf("put B: %v", err)
	}
	gotB, _ := st.GetByID(ctx, pgstore.UUIDFromPG(rowB.ArtifactID))
	if gotB.AllowedWrite == nil || len(gotB.AllowedWrite) != 0 {
		t.Fatalf("empty allowed_write must read back non-nil empty (creator-only), got %#v", gotB.AllowedWrite)
	}
}
