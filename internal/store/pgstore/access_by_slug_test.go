//go:build integration

package pgstore_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// seedSlugVersion inserts one version under slug with an explicit ACL pair and
// returns the row. Fails the test on error.
func seedSlugVersion(t *testing.T, st *pgstore.Store, slug string, access, write []string) sqlc.Artifact {
	t.Helper()
	row, err := st.Put(context.Background(), pgstore.PutInput{
		ArtifactType:  pgstore.TypeText,
		NamedSlug:     &slug,
		Title:         "acl-fixture",
		ContentType:   "text/plain",
		Content:       []byte("body"),
		Creator:       "alice@example.com",
		AllowedAccess: access,
		AllowedWrite:  write,
	})
	if err != nil {
		t.Fatalf("seed %s: %v", slug, err)
	}
	return row
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]struct{}{}
	for _, x := range a {
		m[x] = struct{}{}
	}
	for _, x := range b {
		if _, ok := m[x]; !ok {
			return false
		}
	}
	return true
}

func mustGet(t *testing.T, st *pgstore.Store, id uuid.UUID) sqlc.Artifact {
	t.Helper()
	row, err := st.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return row
}

func rowUUID(t *testing.T, row sqlc.Artifact) uuid.UUID {
	t.Helper()
	id, err := uuid.FromBytes(row.ArtifactID.Bytes[:])
	if err != nil {
		t.Fatalf("row uuid: %v", err)
	}
	return id
}

// The slug is the unit of access: one UpdateAccessBySlug call must rewrite the
// ACL pair on EVERY version of the slug, archived rows included — otherwise an
// unarchive resurrects a stale ACL and a revocation doesn't stick.
func TestUpdateAccessBySlug_WritesEveryVersionIncludingArchived(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("acl-slug")

	v1 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	v2 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	v1id, v2id := rowUUID(t, v1), rowUUID(t, v2)
	if _, err := st.ArchiveByID(ctx, v1id); err != nil {
		t.Fatalf("archive v1: %v", err)
	}

	n, err := st.UpdateAccessBySlug(ctx, slug, []string{"kept@example.com"}, nil)
	if err != nil {
		t.Fatalf("UpdateAccessBySlug: %v", err)
	}
	if n != 2 {
		t.Fatalf("changed rows = %d, want 2 (archived row included)", n)
	}
	for _, id := range []uuid.UUID{v1id, v2id} {
		row := mustGet(t, st, id)
		if !sameSet(row.AllowedAccess, []string{"kept@example.com"}) {
			t.Errorf("version %s allowed_access = %v, want [kept@example.com]", id, row.AllowedAccess)
		}
		if row.AllowedWrite != nil {
			t.Errorf("version %s allowed_write = %v, want nil (mirror mode preserved)", id, row.AllowedWrite)
		}
	}
	// The archived row stays archived — ACL convergence must not resurrect it.
	if v1After := mustGet(t, st, v1id); !v1After.DeletedAt.Valid {
		t.Error("archived version lost its deleted_at on ACL update")
	}
}

// nil write = mirror mode (write follows read) and [] = creator-only are
// different states; the slug-wide write must preserve whichever it is given,
// and must union an explicit write list into access (the ⊆ invariant).
func TestUpdateAccessBySlug_MirrorVsExplicitWrite(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("acl-mirror")

	v1 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	v2 := seedSlugVersion(t, st, slug, []string{"*"}, nil)

	// Explicit write list: unioned into access on every row.
	if _, err := st.UpdateAccessBySlug(ctx, slug, []string{"r@example.com"}, []string{"w@example.com"}); err != nil {
		t.Fatalf("explicit write: %v", err)
	}
	for _, row := range []sqlc.Artifact{v1, v2} {
		got := mustGet(t, st, rowUUID(t, row))
		if !sameSet(got.AllowedAccess, []string{"r@example.com", "w@example.com"}) {
			t.Errorf("allowed_access = %v, want write unioned into read", got.AllowedAccess)
		}
		if !sameSet(got.AllowedWrite, []string{"w@example.com"}) || got.AllowedWrite == nil {
			t.Errorf("allowed_write = %v, want [w@example.com]", got.AllowedWrite)
		}
	}

	// Back to mirror mode: write must be NULL again on every row.
	if _, err := st.UpdateAccessBySlug(ctx, slug, []string{"r@example.com"}, nil); err != nil {
		t.Fatalf("mirror write: %v", err)
	}
	for _, row := range []sqlc.Artifact{v1, v2} {
		if got := mustGet(t, st, rowUUID(t, row)); got.AllowedWrite != nil {
			t.Errorf("allowed_write = %v, want nil (mirror)", got.AllowedWrite)
		}
	}
}

// Re-sending the pair the slug already carries must report zero changed rows,
// so the service can skip the N-version reindex in the steady state.
func TestUpdateAccessBySlug_NoChangeReturnsZero(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("acl-noop")

	seedSlugVersion(t, st, slug, []string{"*"}, nil)
	if n, err := st.UpdateAccessBySlug(ctx, slug, []string{"a@example.com"}, nil); err != nil || n != 1 {
		t.Fatalf("first update: n=%d err=%v, want 1,nil", n, err)
	}
	if n, err := st.UpdateAccessBySlug(ctx, slug, []string{"a@example.com"}, nil); err != nil || n != 0 {
		t.Fatalf("identical resend: n=%d err=%v, want 0,nil", n, err)
	}
}

// Publishing a new version with an ACL that differs from the slug's current
// pair changes the SLUG's ACL: the siblings must converge to the new pair in
// the same operation. (Authority is the caller's job — the service's
// CheckAccess hook — exactly like UpdateAccess today.)
func TestPut_ExplicitACLChangeFansOutToSiblings(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("acl-fanout")

	v1 := seedSlugVersion(t, st, slug, []string{"*"}, nil)
	seedSlugVersion(t, st, slug, []string{"only@example.com"}, nil)

	got := mustGet(t, st, rowUUID(t, v1))
	if !sameSet(got.AllowedAccess, []string{"only@example.com"}) {
		t.Fatalf("v1 allowed_access = %v, want converged to [only@example.com]", got.AllowedAccess)
	}
	_ = ctx
}

// When the caller marks the ACL as inherited (it did not explicitly set one),
// the pair persisted — and NOT fanned out — must come from the FRESH prior
// version read inside Put's critical section, not from the possibly-stale
// values the caller resolved earlier. Otherwise a publish racing an ACL change
// silently reverts the slug to the stale pair.
func TestPut_InheritedACLUsesFreshPrev(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("acl-inherit")

	v1 := seedSlugVersion(t, st, slug, []string{"fresh@example.com"}, nil)

	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		NamedSlug:    &slug,
		Title:        "v2",
		ContentType:  "text/plain",
		Content:      []byte("body2"),
		Creator:      "alice@example.com",
		// Stale fallback a racing caller would have resolved from an earlier
		// read; the inherit flags mean "prefer the fresh prev's pair".
		AllowedAccess: []string{"stale@example.com"},
		AllowedWrite:  nil,
		InheritAccess: true,
		InheritWrite:  true,
	})
	if err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if !sameSet(row.AllowedAccess, []string{"fresh@example.com"}) {
		t.Fatalf("v2 allowed_access = %v, want inherited fresh pair [fresh@example.com]", row.AllowedAccess)
	}
	if got := mustGet(t, st, rowUUID(t, v1)); !sameSet(got.AllowedAccess, []string{"fresh@example.com"}) {
		t.Fatalf("v1 allowed_access = %v, stale pair must not fan out", got.AllowedAccess)
	}
}

// The invariant under concurrency: a slug-wide ACL write racing a version
// insert must never leave versions of the slug with different pairs, whichever
// order the two land in.
func TestSlugACL_RevokeRacingCreateStaysUniform(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	for i := 0; i < 10; i++ {
		slug := unique("acl-race")
		seedSlugVersion(t, st, slug, []string{"*"}, nil)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = st.UpdateAccessBySlug(ctx, slug, []string{"kept@example.com"}, nil)
		}()
		go func() {
			defer wg.Done()
			_, _ = st.Put(ctx, pgstore.PutInput{
				ArtifactType:  pgstore.TypeText,
				NamedSlug:     &slug,
				Title:         "v2",
				ContentType:   "text/plain",
				Content:       []byte("body2"),
				Creator:       "alice@example.com",
				AllowedAccess: []string{"*"},
			})
		}()
		wg.Wait()

		rows, err := st.Versions(ctx, slug)
		if err != nil || len(rows) < 2 {
			t.Fatalf("versions: n=%d err=%v", len(rows), err)
		}
		first := rows[0].AllowedAccess
		for _, r := range rows[1:] {
			if !sameSet(r.AllowedAccess, first) {
				t.Fatalf("iteration %d: divergent ACLs after race: %v vs %v", i, first, r.AllowedAccess)
			}
		}
	}
}
