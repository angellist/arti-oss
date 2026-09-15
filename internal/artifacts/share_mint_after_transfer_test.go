//go:build integration

package artifacts_test

import (
	"context"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/artifacts"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// Ownership became transferable in #290, but shareMintAuthority still decides
// "this slug's lineage is disputed" by comparing the earliest LIVE version's
// creator against the CALLER. That comparison is only meaningful while caller,
// owner and earliest creator are necessarily the same person — which every
// never-transferred document satisfies and no transferred one does.
//
// So after a transfer the new owner passes shareAuthority (they are the stored
// owner) and then fails here, because the live lineage names their predecessor;
// the predecessor fails shareAuthority. Nobody but an admin can mint, and
// can_share goes false so the viewer hides the control without an error.
func TestShareMint_NewOwnerCanMintAfterATransfer(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "https://arti.example.com", nil, nil)
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 720 * time.Hour})

	const alice = "alice-mint@example.com"
	const bob = "bob-mint@example.com"

	slug := uniqueSlug("share-mint-transfer")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "handover", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, alice)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Alice can mint while she still owns it — the baseline the transfer must
	// not break.
	if _, err := svc.MintShare(ctx, v1.ArtifactID, alice, artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); err != nil {
		t.Fatalf("owner minting before the transfer: %v", err)
	}

	if _, err := svc.TransferOwner(ctx, slug, bob, alice, false); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	// Alice created v1 and it is still live, so the live lineage still names
	// her. That must not stop Bob, who now owns the document.
	if _, err := svc.MintShare(ctx, v1.ArtifactID, bob, artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); err != nil {
		t.Fatalf("the new owner must be able to mint a share link: %v", err)
	}
	info, err := svc.Get(ctx, mustUUID(t, v1.ArtifactID), bob)
	if err != nil {
		t.Fatalf("get as the new owner: %v", err)
	}
	if info.CanShare == nil || !*info.CanShare {
		t.Fatal("can_share must be true for the new owner, or the viewer hides the control with no error")
	}
	// The previous owner keeps no share authority.
	if _, err := svc.MintShare(ctx, v1.ArtifactID, alice, artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); !artifacts.IsForbidden(err) {
		t.Fatalf("previous owner minting: err = %v, want forbidden", err)
	}
}

// A reclaim must stay refused. Ownership does not move when a slug is archived,
// so after A hands the document to B, archives the whole lineage and republishes
// under the same name, B still owns the slug while A wrote everything now in it.
// Letting B mint there would publish A's new document to the internet on the
// strength of a handover that predated it.
func TestShareMint_ReclaimAfterTransferStaysRefused(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "https://arti.example.com", nil, nil)
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 720 * time.Hour})

	const alice = "alice-reclaim@example.com"
	const bob = "bob-reclaim@example.com"

	slug := uniqueSlug("share-reclaim")
	world := []string{"*"}
	if _, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "first life", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, alice); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.TransferOwner(ctx, slug, bob, alice, false); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if _, err := svc.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	// Alice reclaims the freed name with new content. Both creator-derived
	// identities are Alice again, so a creator-vs-creator comparison would call
	// this undisputed.
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "second life", ContentType: "text/markdown", Content: "v2",
		AllowedAccess: &world,
	}, alice)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if _, err := svc.MintShare(ctx, v2.ArtifactID, bob, artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); !artifacts.IsForbidden(err) {
		t.Fatalf("stale owner minting on reclaimed content: err = %v, want forbidden", err)
	}
	info, err := svc.Get(ctx, mustUUID(t, v2.ArtifactID), bob)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if info.CanShare != nil && *info.CanShare {
		t.Fatal("can_share must be false for a stale owner on reclaimed content")
	}
}

// A slug whose ownership was never moved keeps the stricter rule: any lineage
// written by someone other than the owner is a dispute. This is the shifted
// lineage case, and it must not have been relaxed by the transfer carve-out.
func TestShareMint_ShiftedLineageWithoutATransferStaysRefused(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	svc := artifacts.NewService(st, "https://arti.example.com", nil, nil)
	svc.SetShareConfig(artifacts.ShareConfig{Enabled: true, MaxTTL: 720 * time.Hour})

	const alice = "alice-shift@example.com"
	const writer = "writer-shift@example.com"

	slug := uniqueSlug("share-shift")
	world := []string{"*"}
	v1, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v1", ContentType: "text/markdown", Content: "v1",
		AllowedAccess: &world,
	}, alice)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	v2, err := svc.Create(ctx, artifacts.CreateRequest{
		NamedSlug: &slug, Title: "v2", ContentType: "text/markdown", Content: "v2",
	}, writer)
	if err != nil {
		t.Fatalf("delegated version: %v", err)
	}
	// Alice archives her own version, leaving the writer's as the earliest live
	// one. Ownership never moved, so she is still the owner.
	if _, err := svc.ArchiveByID(ctx, mustUUID(t, v1.ArtifactID)); err != nil {
		t.Fatalf("archive v1: %v", err)
	}
	if _, err := svc.MintShare(ctx, v2.ArtifactID, alice, artifacts.MintShareRequest{Scope: "version", TTL: "7d"}); !artifacts.IsForbidden(err) {
		t.Fatalf("owner minting over a shifted lineage: err = %v, want forbidden", err)
	}
}
