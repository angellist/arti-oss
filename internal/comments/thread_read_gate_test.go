//go:build integration

package comments

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/auth"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Turning commenting off means the document has no threads, which is what every
// list path reports. Reading one thread by id must say the same thing, or a
// caller holding the id reads around the switch.
func TestGetThreadIsGoneWhenCommentingIsOff(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	art := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	svc := NewService(pool, art, auth.NewJWTSigner([]byte("test-secret")))

	const owner = "owner-threadgate@example.com"
	row, err := art.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "doc", ContentType: "text/markdown",
		Content: []byte("a line of text"), Creator: owner, AllowedAccess: []string{"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := uuidFrom(row.ArtifactID)
	out, err := svc.AddForCaller(ctx, id, owner, AddInput{Body: "a thread"})
	if err != nil {
		t.Fatal(err)
	}
	tid, err := uuid.Parse(out.Thread.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.GetThreadForCaller(ctx, tid, owner); err != nil {
		t.Fatalf("thread must be readable while commenting is on: %v", err)
	}
	if _, err := art.SetCommentsEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}

	list, err := svc.ListForCaller(ctx, id, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Threads) != 0 {
		t.Fatalf("list returned %d threads with commenting off", len(list.Threads))
	}
	if _, err := svc.GetThreadForCaller(ctx, tid, owner); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("get one thread: err = %v, want ErrNotFound to match the list paths", err)
	}
}
