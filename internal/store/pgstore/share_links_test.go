//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// hashFor builds a token hash that is unique to this test run. arti_test is
// shared across runs and rows persist, so a fixed literal collides with the
// unique index on the second run.
func hashFor(label string) []byte {
	return []byte(unique(label))
}

func newShareStore(t *testing.T) *pgstore.Store {
	t.Helper()
	return pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
}

// The plaintext token is shown once and never stored. What the row keeps must
// be the digest plus a display prefix — nothing from which a URL is derivable.
func TestShareLinkLifecycle(t *testing.T) {
	st, ctx := newShareStore(t), context.Background()
	art := seedSlugVersion(t, st, unique("share-lifecycle"), []string{"*"}, nil)
	aid := pgstore.UUIDFromPG(art.ArtifactID)
	hash := hashFor("hash-a")

	row, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash:   hash,
		TokenPrefix: "abcd1234",
		ArtifactID:  &aid,
		CreatedBy:   "owner@example.com",
		ExpiresAt:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if row.TokenPrefix != "abcd1234" || len(row.TokenHash) == 0 {
		t.Fatalf("row = %+v", row)
	}
	if row.Slug != nil {
		t.Fatalf("pinned link carries a slug: %v", *row.Slug)
	}

	got, err := st.GetShareLinkByHash(ctx, hash)
	if err != nil || got.ID != row.ID {
		t.Fatalf("by hash: (%+v, %v)", got, err)
	}
	if _, err := st.GetShareLinkByHash(ctx, []byte("no-such-hash")); !errors.Is(err, pgstore.ErrNotFound) {
		t.Fatalf("miss: want ErrNotFound, got %v", err)
	}

	if err := st.RevokeShareLink(ctx, row.ID, "owner@example.com"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, _ = st.GetShareLinkByHash(ctx, hash)
	if !got.RevokedAt.Valid {
		t.Fatal("revoked_at still null after revoke")
	}
	if got.RevokedBy == nil || *got.RevokedBy != "owner@example.com" {
		t.Fatalf("revoked_by = %v", got.RevokedBy)
	}
}

// The CHECK is the invariant the serve path relies on: a row targets one
// version or one slug, never both and never neither. Seed a REAL artifact —
// with a fabricated id the foreign key fires first and the test passes
// without ever exercising the constraint it is named after.
func TestShareLinkTargetExactlyOne(t *testing.T) {
	st, ctx := newShareStore(t), context.Background()
	slug := unique("share-check")
	art := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	aid := pgstore.UUIDFromPG(art.ArtifactID)

	if _, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("h-both"), ArtifactID: &aid, Slug: &slug,
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}); err == nil {
		t.Fatal("both artifact_id and slug set: want a constraint violation")
	}
	if _, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("h-neither"),
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}); err == nil {
		t.Fatal("neither set: want a constraint violation")
	}
}

func TestRecordShareLinkOpen(t *testing.T) {
	st, ctx := newShareStore(t), context.Background()
	art := seedSlugVersion(t, st, unique("share-opens"), []string{"*"}, nil)
	aid := pgstore.UUIDFromPG(art.ArtifactID)
	row, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("hash-opens"), ArtifactID: &aid,
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	const socket, forwarded = "198.51.100.7", "203.0.113.9"
	for i := 0; i < 3; i++ {
		if err := st.RecordShareLinkOpen(ctx, row.ID, forwarded, socket, "curl/8"); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	opens, err := st.ListShareLinkOpens(ctx, row.ID, 10)
	if err != nil || len(opens) != 3 {
		t.Fatalf("opens = %d, %v; want 3", len(opens), err)
	}
	// The two address columns must stay distinct. That difference is the only
	// signal that the forwarded header was set by the client rather than by
	// the ingress, and the leak story in DD-0069 leans on it.
	if opens[0].Ip == opens[0].PeerAddr {
		t.Fatalf("ip and peer_addr collapsed to %q", opens[0].Ip)
	}
	if opens[0].Ip != forwarded || opens[0].PeerAddr != socket {
		t.Fatalf("ip=%q peer_addr=%q; want %q / %q", opens[0].Ip, opens[0].PeerAddr, forwarded, socket)
	}

	after, err := st.GetShareLink(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.OpenCount != 3 {
		t.Fatalf("open_count = %d, want 3", after.OpenCount)
	}
	if !after.LastOpenedAt.Valid {
		t.Fatal("last_opened_at not set")
	}
}

// A pinned link stores the artifact_id of whichever version was current when
// it was minted, so a list keyed only on the latest version's id would hide
// links minted against earlier ones. The Share dialog promises every link for
// the DOCUMENT, so the query has to walk the slug's lineage.
func TestListShareLinksForDoc_SpansTheSlugLineage(t *testing.T) {
	st, ctx := newShareStore(t), context.Background()
	slug := unique("share-lineage")

	v1 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	v1id := pgstore.UUIDFromPG(v1.ArtifactID)
	if _, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("h-v1"), ArtifactID: &v1id,
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	v2 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	v2id := pgstore.UUIDFromPG(v2.ArtifactID)
	if _, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("h-v2"), ArtifactID: &v2id,
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateShareLink(ctx, pgstore.ShareLinkInput{
		TokenHash: hashFor("h-track"), Slug: &slug,
		CreatedBy: "o@example.com", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	// Listing from the LATEST version must still find the v1-pinned link.
	rows, err := st.ListShareLinksForDoc(ctx, v2id, &slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("listed %d links from the latest version, want all 3 (v1-pinned, v2-pinned, tracking)", len(rows))
	}
}
