//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func putVersion(t *testing.T, st *pgstore.Store, slug, creator string) {
	t.Helper()
	if _, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &slug, Title: "doc",
		ContentType: "text/plain", Content: []byte("body"), Creator: creator,
		AllowedAccess: []string{"*"},
	}); err != nil {
		t.Fatalf("put %s as %s: %v", slug, creator, err)
	}
}

// Ownership is claimed by the first version and survives everything a
// lower-privileged action can do: publishing a version, and archiving the very
// version that established it.
func TestSlugOwner_ClaimedByFirstVersionAndImmutable(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("owner-claim")

	putVersion(t, st, slug, "alice@example.com")
	putVersion(t, st, slug, "bob@example.com")

	owner, err := st.SlugOwner(ctx, slug)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	if owner != "alice@example.com" {
		t.Fatalf("owner after bob's version = %q, want alice@example.com", owner)
	}

	if _, err := st.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if owner, err = st.SlugOwner(ctx, slug); err != nil {
		t.Fatalf("owner after archive: %v", err)
	}
	if owner != "alice@example.com" {
		t.Fatalf("owner after archiving v1 = %q, want alice@example.com", owner)
	}
}

// A slug written by a binary that predates migration 0029 has no owner row.
// It must still resolve to its earliest creator — never to whoever happens to
// publish next — and the next write must heal the row with that same answer.
func TestSlugOwner_HealsSlugWrittenBeforeMigration(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	slug := unique("owner-legacy")

	putVersion(t, st, slug, "alice@example.com")
	if _, err := pool.Exec(ctx, `DELETE FROM artifact_owners WHERE named_slug = $1`, slug); err != nil {
		t.Fatalf("simulate pre-migration slug: %v", err)
	}

	owner, err := st.SlugOwner(ctx, slug)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	if owner != "alice@example.com" {
		t.Fatalf("derived owner = %q, want alice@example.com", owner)
	}

	putVersion(t, st, slug, "bob@example.com")
	if got := ownerRow(t, pool, slug); got != "alice@example.com" {
		t.Fatalf("healed owner row = %q, want alice@example.com — bob's publish must not claim alice's document", got)
	}
}

// Transfer moves ownership and leaves the new owner able to read what they now
// own, even when the document's ACL never named them.
func TestTransferSlugOwner_MovesOwnerAndGrantsRead(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	slug := unique("owner-transfer")

	seedSlugVersion(t, st, slug, []string{"alice@example.com"}, nil)
	seedSlugVersion(t, st, slug, []string{"alice@example.com"}, nil)

	prev, err := st.TransferSlugOwner(ctx, slug, "carol@example.com", "alice@example.com", true, nil)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if prev != "alice@example.com" {
		t.Fatalf("previous owner = %q, want alice@example.com", prev)
	}
	if got := ownerRow(t, pool, slug); got != "carol@example.com" {
		t.Fatalf("owner after transfer = %q, want carol@example.com", got)
	}

	rows, err := pool.Query(ctx, `SELECT allowed_access FROM artifacts WHERE named_slug = $1`, slug)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var access []string
		if err := rows.Scan(&access); err != nil {
			t.Fatal(err)
		}
		if !contains(access, "carol@example.com") {
			t.Fatalf("version %d access = %v, want the new owner granted read", n, access)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("checked %d versions, want 2", n)
	}
}

// A `*` document must not accumulate a redundant owner token: the patterns
// already grant them.
func TestTransferSlugOwner_NoRedundantTokenOnWorldReadable(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	slug := unique("owner-world")

	seedSlugVersion(t, st, slug, []string{"*"}, nil)
	if _, err := st.TransferSlugOwner(ctx, slug, "carol@example.com", "alice@example.com", true, nil); err != nil {
		t.Fatalf("transfer: %v", err)
	}

	var access []string
	if err := pool.QueryRow(ctx,
		`SELECT allowed_access FROM artifacts WHERE named_slug = $1 ORDER BY version DESC LIMIT 1`,
		slug).Scan(&access); err != nil {
		t.Fatal(err)
	}
	if len(access) != 1 || access[0] != "*" {
		t.Fatalf("access = %v, want unchanged ['*']", access)
	}
}

// The authorize hook sees the owner as it stands inside the transaction, and
// refusing it must leave the document untouched — including the read grant.
func TestTransferSlugOwner_AuthorizeRefusalIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	slug := unique("owner-authorize")

	seedSlugVersion(t, st, slug, []string{"alice@example.com"}, nil)

	var saw string
	_, err := st.TransferSlugOwner(ctx, slug, "mallory@example.com", "mallory@example.com", true,
		func(prev string) error {
			saw = prev
			return errRefused
		})
	if !errors.Is(err, errRefused) {
		t.Fatalf("transfer err = %v, want the authorize refusal", err)
	}
	if saw != "alice@example.com" {
		t.Fatalf("authorize saw prev = %q, want alice@example.com", saw)
	}
	if got := ownerRow(t, pool, slug); got != "alice@example.com" {
		t.Fatalf("owner after refused transfer = %q, want alice@example.com", got)
	}

	var access []string
	if err := pool.QueryRow(ctx,
		`SELECT allowed_access FROM artifacts WHERE named_slug = $1`, slug).Scan(&access); err != nil {
		t.Fatal(err)
	}
	for _, tok := range access {
		if tok == "mallory@example.com" {
			t.Fatalf("refused transfer still granted read: %v", access)
		}
	}
}

var errRefused = errors.New("refused")

func TestTransferSlugOwner_UnknownSlug(t *testing.T) {
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	if _, err := st.TransferSlugOwner(context.Background(), unique("nope"), "carol@example.com", "alice@example.com", true, nil); err == nil {
		t.Fatal("transferring a slug with no versions must fail")
	}
}

func ownerRow(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var owner string
	if err := pool.QueryRow(context.Background(),
		`SELECT owner_email FROM artifact_owners WHERE named_slug = $1`, slug).Scan(&owner); err != nil {
		t.Fatalf("read owner row for %s: %v", slug, err)
	}
	return owner
}
