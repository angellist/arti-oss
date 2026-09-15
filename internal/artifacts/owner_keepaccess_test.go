//go:build integration

package artifacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func hasTok(list []string, tok string) bool {
	for _, t := range list {
		if strings.EqualFold(t, tok) {
			return true
		}
	}
	return false
}

// Write access answers to the owner and has no creator fallback (CanWrite),
// so handing a document over silently costs the outgoing owner the ability to
// edit a document they may still be working in. keepAccess is the opt-out for
// that, and it must ADD only: read on versions they created is theirs anyway
// via CanAccess's creator short-circuit, so a control that claimed to revoke
// would be lying.
func TestTransferOwner_KeepAccessGrantsTheOutgoingOwnerWrite(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const alice = "alice-keep@example.com"
	const bob = "bob-keep@example.com"
	const carol = "carol-keep@example.com"

	for _, keep := range []bool{true, false} {
		slug := uniqueSlug("keep-access")
		// An explicit write list, so the grant has somewhere to land, and carol
		// present so we can prove a transfer removes nobody.
		readers := []string{carol}
		writers := []string{carol}
		v1, err := svc.Create(ctx, artifacts.CreateRequest{
			NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
			AllowedAccess: &readers, AllowedWrite: &writers,
		}, alice)
		if err != nil {
			t.Fatalf("seed (keep=%v): %v", keep, err)
		}
		if _, err := svc.TransferOwner(ctx, slug, bob, alice, keep); err != nil {
			t.Fatalf("transfer (keep=%v): %v", keep, err)
		}
		after, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), bob)
		if err != nil {
			t.Fatalf("new owner reading the document (keep=%v): %v", keep, err)
		}
		if !hasTok(after.AllowedAccess, bob) {
			t.Fatalf("keep=%v: recipient missing from access %v", keep, after.AllowedAccess)
		}
		if got := hasTok(after.AllowedWrite, alice); got != keep {
			t.Fatalf("keep=%v: outgoing owner in allowed_write = %v (%v)", keep, got, after.AllowedWrite)
		}
		if got := hasTok(after.AllowedAccess, alice); got != keep {
			t.Fatalf("keep=%v: outgoing owner in allowed_access = %v (%v)", keep, got, after.AllowedAccess)
		}
		if !hasTok(after.AllowedAccess, carol) || !hasTok(after.AllowedWrite, carol) {
			t.Fatalf("keep=%v: a transfer dropped carol (%v / %v); it must only ever add",
				keep, after.AllowedAccess, after.AllowedWrite)
		}
		// The behaviour the flag exists for: can the outgoing owner still write?
		aliceView, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), alice)
		if err != nil {
			t.Fatalf("outgoing owner reading (keep=%v): %v", keep, err)
		}
		if aliceView.CanWrite == nil || *aliceView.CanWrite != keep {
			t.Fatalf("keep=%v: outgoing owner can_write = %v, want %v", keep, aliceView.CanWrite, keep)
		}
	}
}

// A mirror-mode document has no explicit write list: read and write are the
// same set, so the read grant already carries write. Materialising the list
// would make it explicit and sticky for every later version, and because write
// is unioned into read the next narrowing of read would widen it straight back.
func TestTransferOwner_KeepAccessLeavesMirrorModeAlone(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const alice = "alice-mirror@example.com"
	const bob = "bob-mirror@example.com"

	slug := uniqueSlug("keep-mirror")
	readers := []string{"carol-mirror@example.com"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &readers,
	}, alice)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if v1.AllowedWrite != nil {
		t.Fatalf("fixture is not mirror mode: %v", v1.AllowedWrite)
	}
	if _, err := svc.TransferOwner(ctx, slug, bob, alice, true); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	after, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), bob)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.AllowedWrite != nil {
		t.Fatalf("allowed_write became %v; mirror mode must survive a transfer", after.AllowedWrite)
	}
	if !hasTok(after.AllowedAccess, alice) {
		t.Fatalf("outgoing owner missing from %v; in mirror mode the read grant IS the write grant", after.AllowedAccess)
	}
}

// A document already open to everyone needs no extra token for either party.
// grantReadBySlugTx skips a token the existing patterns already match, so a
// chain of transfers cannot accumulate redundant entries.
func TestTransferOwner_AddsNoRedundantTokenOnAWorldReadableDocument(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	slug := uniqueSlug("keep-world")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, "alice-world@example.com")
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.TransferOwner(ctx, slug, "bob-world@example.com", "alice-world@example.com", true); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	after, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), "bob-world@example.com")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(after.AllowedAccess) != 1 || after.AllowedAccess[0] != "*" {
		t.Fatalf("allowed_access = %v, want just [*]", after.AllowedAccess)
	}
}

// The new owner controls access, comments and sharing, so they must be able to
// retitle the document too. Without this an "owner" cannot name the thing they
// own, which reads as a bug. The widening adds the owner and nobody else.
func TestTransferOwner_NewOwnerCanEditMetadata(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const alice = "alice-meta@example.com"
	const bob = "bob-meta@example.com"

	slug := uniqueSlug("meta-owner")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "before", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, alice)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	id := mustUUID(t, v1.ArtifactID)
	if _, err := svc.TransferOwner(ctx, slug, bob, alice, true); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	title := "renamed by the new owner"
	if _, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &title}, bob); err != nil {
		t.Fatalf("new owner renaming: %v", err)
	}
	info, err := svc.Get(ctx, id, bob)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if info.CanEditMetadata == nil || !*info.CanEditMetadata {
		t.Fatal("can_edit_metadata must be true for the new owner, or the viewer hides rename")
	}
	// The widening added the owner, not everyone: a reader with no stake is
	// still refused, and still told so.
	stranger := "stranger-meta@example.com"
	if _, err := svc.UpdateMetadata(ctx, id, artifacts.UpdateMetadataRequest{Title: &title}, stranger); !artifacts.IsForbidden(err) {
		t.Fatalf("stranger renaming: err = %v, want forbidden", err)
	}
	sInfo, err := svc.Get(ctx, id, stranger)
	if err != nil {
		t.Fatalf("stranger get: %v", err)
	}
	if sInfo.CanEditMetadata != nil && *sInfo.CanEditMetadata {
		t.Fatal("can_edit_metadata must be false for a plain reader")
	}
}

// write ⊆ read is the invariant finalAccess enforces on every other ACL write
// path, and keep_access must not be the exception. On a world-readable document
// the read grant is correctly skipped (`*` already covers everyone), so a write
// grant that touched only allowed_write would leave a token allowed_access does
// not carry — invisible to the access editor, which builds its rows from
// allowed_access, and dropped by the next Confirm.
func TestTransferOwner_KeepAccessKeepsWriteWithinRead(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "http://localhost", nil, nil)

	const alice = "alice-subset@example.com"
	const bob = "bob-subset@example.com"
	const carol = "carol-subset@example.com"

	slug := uniqueSlug("keep-subset")
	// World-readable, but writes restricted — so the read grant is skipped
	// while the write grant is exactly what alice stands to lose.
	world := []string{"*"}
	writers := []string{carol}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "doc", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world, AllowedWrite: &writers,
	}, alice)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.TransferOwner(ctx, slug, bob, alice, true); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	after, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), bob)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !hasTok(after.AllowedWrite, alice) {
		t.Fatalf("outgoing owner missing from allowed_write %v", after.AllowedWrite)
	}
	// The point of the test: every write token is also a read token, so the
	// access editor renders the grantee and cannot drop them.
	for _, w := range after.AllowedWrite {
		if !hasTok(after.AllowedAccess, w) {
			t.Fatalf("write token %q is not in allowed_access %v; write must stay a subset of read",
				w, after.AllowedAccess)
		}
	}
}
