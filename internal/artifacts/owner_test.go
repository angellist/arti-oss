//go:build integration

package artifacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Transfer is owner-or-admin, resolved through the immutable owner — so a
// delegated writer who pushed the latest version, and therefore is that
// version's creator, still cannot hand the document to themselves.
func TestTransferOwner_OwnerOnly(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("transfer-authority")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com"); err != nil {
		t.Fatalf("bob content version: %v", err)
	}

	if _, err := svc.TransferOwner(ctx, slug, "bob@example.com", "bob@example.com", true); err == nil {
		t.Fatal("a delegated writer must not transfer ownership to themselves")
	}

	resp, err := svc.TransferOwner(ctx, slug, "carol@example.com", "alice@example.com", true)
	if err != nil {
		t.Fatalf("owner transfer: %v", err)
	}
	if resp.Owner != "carol@example.com" || resp.PreviousOwner != "alice@example.com" {
		t.Fatalf("transfer = %+v, want carol@example.com from alice@example.com", resp)
	}

	// Authority moved with it: alice is now an ordinary writer, carol decides.
	if _, err := svc.TransferOwner(ctx, slug, "alice@example.com", "alice@example.com", true); err == nil {
		t.Fatal("the former owner must not be able to take the document back")
	}
	if _, err := svc.TransferOwner(ctx, slug, "alice@example.com", "carol@example.com", true); err != nil {
		t.Fatalf("new owner transferring back: %v", err)
	}
}

// A stranger must not learn that a private slug exists by poking the transfer
// endpoint: no-access answers not-found, while a reader who simply lacks
// authority gets a forbidden.
func TestTransferOwner_NoExistenceOracle(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	private := uniqueSlug("transfer-private")
	restricted := []string{"alice@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &private, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &restricted,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed private: %v", err)
	}
	if _, err := svc.TransferOwner(ctx, private, "mallory@example.com", "mallory@example.com", true); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("stranger on a private slug: err = %v, want ErrNotFound", err)
	}

	open := uniqueSlug("transfer-open")
	world := []string{"*"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &open, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed open: %v", err)
	}
	_, err := svc.TransferOwner(ctx, open, "bob@example.com", "bob@example.com", true)
	if err == nil {
		t.Fatal("a reader must not transfer a document they do not own")
	}
	if errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("reader on a readable slug: err = %v, want a forbidden rather than not-found", err)
	}
}

func TestTransferOwner_RejectsNonEmail(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("transfer-bad-target")
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, bad := range []string{"", "group:eng", "alice", "*@example.com"} {
		if _, err := svc.TransferOwner(ctx, slug, bad, "alice@example.com", true); err == nil {
			t.Fatalf("transfer to %q must be rejected", bad)
		}
	}
}

// The owner cannot be locked out of their own document by version churn. With
// owner-only writes and a latest version pushed by someone else, the owner
// still writes and the revoked writer does not — the gate reads the owner, not
// the latest version's creator.
func TestWriteGate_OwnerKeepsWriteAfterAnotherVersion(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("owner-write")
	world := []string{"*"}
	writers := []string{"bob@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v2",
	}, "bob@example.com"); err != nil {
		t.Fatalf("bob content version: %v", err)
	}

	// Alice revokes bob and closes writes to herself.
	ownerOnly := []string{}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v3",
		AllowedAccess: &world, AllowedWrite: &ownerOnly,
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner closing writes: %v", err)
	}

	_, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v4",
	}, "bob@example.com")
	if !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("revoked writer versioning: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v4",
	}, "alice@example.com"); err != nil {
		t.Fatalf("owner must keep write access: %v", err)
	}

	info, err := svc.GetBySlug(ctx, slug, nil, "alice@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if info.Owner == nil || *info.Owner != "alice@example.com" {
		t.Fatalf("reported owner = %v, want alice@example.com", info.Owner)
	}
}

// Transferring must not strand the new owner outside a restricted document.
func TestTransferOwner_NewOwnerCanReadRestrictedDoc(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("transfer-restricted")
	private := []string{"alice@example.com"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &private,
	}, "alice@example.com"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.GetBySlug(ctx, slug, nil, "carol@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("carol reading before transfer: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.TransferOwner(ctx, slug, "carol@example.com", "alice@example.com", true); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	info, err := svc.GetBySlug(ctx, slug, nil, "carol@example.com")
	if err != nil {
		t.Fatalf("new owner reading their own document: %v", err)
	}
	if info.CanWrite == nil || !*info.CanWrite {
		t.Fatalf("new owner can_write = %v, want true", info.CanWrite)
	}
}
