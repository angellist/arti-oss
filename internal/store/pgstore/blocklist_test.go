//go:build integration

package pgstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// labelCounted reports whether `label` shows up in the label facet. It pages
// the whole facet rather than reading the first page: arti_test is shared, so
// a count-of-one label is not reliably in any top-N. Aggregates (the fixed
// top-N sidebar query) builds its filter separately and is called for the same
// reason a compile is useful — to prove the clause is valid SQL there too.
func labelCounted(t *testing.T, st *pgstore.Store, label, caller string) bool {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Aggregates(ctx, pgstore.AggregatesInput{Limit: 100, CallerEmail: caller}); err != nil {
		t.Fatalf("Aggregates(caller=%q): %v", caller, err)
	}
	for offset := int32(0); ; offset += 100 {
		br, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{
			Facet: "label", Sort: "name", Limit: 100, Offset: offset, CallerEmail: caller,
		})
		if err != nil {
			t.Fatalf("BrowseAggregates(caller=%q): %v", caller, err)
		}
		for _, v := range br.Values {
			if v.Value == label {
				return true
			}
		}
		if len(br.Values) == 0 || int64(offset)+int64(len(br.Values)) >= br.Total {
			return false
		}
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	u, err := uuid.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func putText(t *testing.T, st *pgstore.Store, slug string, labels ...string) (id string) {
	t.Helper()
	in := pgstore.PutInput{
		Labels:       labels,
		ArtifactType: pgstore.TypeText,
		Title:        "block test",
		ContentType:  "text/plain",
		Content:      []byte("hello"),
		Creator:      "blocker@example.com",
	}
	if slug != "" {
		in.NamedSlug = &slug
	}
	row, err := st.Put(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return pgstore.UUIDFromPG(row.ArtifactID).String()
}

// A block must make the document unreachable through every read the store
// offers, and must survive being lifted: nothing about the document changed,
// so the same reads answer again afterwards.
func TestBlock_HidesAndRestores(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("blocked")
	id := putText(t, st, slug)

	if _, err := st.GetBySlug(ctx, slug, nil); err != nil {
		t.Fatalf("precondition: slug should be readable: %v", err)
	}

	if err := st.AddBlock(ctx, slug, "test", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, slug) })

	if _, err := st.GetBySlug(ctx, slug, nil); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetBySlug after block = %v, want ErrNotFound", err)
	}
	if _, err := st.GetLatestBySlugForCaller(ctx, slug, "blocker@example.com"); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetLatestBySlugForCaller after block = %v, want ErrNotFound", err)
	}
	uid := mustUUID(t, id)
	if _, err := st.GetByID(ctx, uid); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetByID after block = %v, want ErrNotFound", err)
	}
	if rows, err := st.GetByIDs(ctx, []uuid.UUID{uid}); err != nil || len(rows) != 0 {
		t.Errorf("GetByIDs after block = %d rows, %v; want 0 rows", len(rows), err)
	}
	if rows, err := st.Versions(ctx, slug); err != nil || len(rows) != 0 {
		t.Errorf("Versions after block = %d rows, %v; want 0 rows", len(rows), err)
	}
	if res, err := st.List(ctx, pgstore.ListInput{Slug: &slug, Limit: 10}); err != nil || res.Total != 0 || len(res.Rows) != 0 {
		t.Errorf("List after block = %d rows total %d, %v; want none", len(res.Rows), res.Total, err)
	}
	// The share-link resolver reads a slug in any state, archived included,
	// and an external link is the reason a document gets blocked at all.
	if _, err := st.GetLatestBySlugAnyState(ctx, &slug); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetLatestBySlugAnyState after block = %v, want ErrNotFound", err)
	}
	if _, err := st.ArchiveBySlug(ctx, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetLatestBySlugAnyState(ctx, &slug); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetLatestBySlugAnyState on a blocked archived slug = %v, want ErrNotFound", err)
	}
	if _, err := st.UnarchiveByID(ctx, mustUUID(t, id)); err != nil {
		t.Fatal(err)
	}

	// A blocked slug takes no writes: a new version would come back with the
	// writer's ACL once the block is lifted.
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, NamedSlug: &slug, Title: slug,
		ContentType: "text/plain", Content: []byte("v2"), Creator: "someone@example.com",
	}); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("Put to blocked slug = %v, want ErrNotFound", err)
	}

	if n, err := st.RemoveBlock(ctx, slug); err != nil || n != 1 {
		t.Fatalf("RemoveBlock = %d, %v", n, err)
	}
	row, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatalf("GetBySlug after lift: %v", err)
	}
	if row.Version == nil || *row.Version != 1 {
		t.Errorf("after lift version = %v, want the original v1 (no write got through)", row.Version)
	}
}

// One pattern covers a family of slugs, and leaves everything else alone.
func TestBlock_GlobAndSluglessByID(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	fam := unique("fam")
	label := unique("lbl")
	hit := putText(t, st, fam+"-one", label)
	miss := unique("other")
	putText(t, st, miss)
	slugless := putText(t, st, "")

	// `?` is one character in the single-row matcher, so the catalog SQL has
	// to honour it too or List surfaces what GetByID has made absent.
	q1 := unique("q") + "-1"
	putText(t, st, q1)
	qpat := strings.TrimSuffix(q1, "1") + "?"
	if err := st.AddBlock(ctx, qpat, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, qpat) })
	if res, err := st.List(ctx, pgstore.ListInput{Slug: &q1, Limit: 10}); err != nil || res.Total != 0 || len(res.Rows) != 0 {
		t.Errorf("List under a `?` block = %d rows total %d, %v; want none", len(res.Rows), res.Total, err)
	}

	if !labelCounted(t, st, label, "") {
		t.Fatalf("precondition: label %q should be counted before the block", label)
	}

	if err := st.AddBlock(ctx, fam+"-*", "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, fam+"-*") })
	if err := st.AddBlock(ctx, slugless, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, slugless) })

	if _, err := st.GetByID(ctx, mustUUID(t, hit)); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("glob-matched slug = %v, want ErrNotFound", err)
	}
	if _, err := st.GetByID(ctx, mustUUID(t, slugless)); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("slugless artifact blocked by id = %v, want ErrNotFound", err)
	}
	if _, err := st.GetBySlug(ctx, miss, nil); err != nil {
		t.Errorf("unrelated slug should still read: %v", err)
	}

	// A blocked document is gone from the facet counts too, on both the
	// caller-filtered and the admin (empty caller) path.
	for _, caller := range []string{"", "blocker@example.com"} {
		if labelCounted(t, st, label, caller) {
			t.Errorf("caller=%q: blocked document still counted under label %q", caller, label)
		}
	}

	// The pattern is the primary key and matching folds case, so one name in
	// two spellings has to be one row. Otherwise a lift that reports success
	// leaves the document hidden by the other spelling.
	mixed := unique("MiXeD-Case")
	if err := st.AddBlock(ctx, mixed, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddBlock(ctx, strings.ToLower(mixed), "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RemoveBlock(ctx, mixed); err != nil || n != 1 {
		t.Errorf("RemoveBlock of a mixed-case pattern = %d rows, %v; want exactly 1", n, err)
	}
	if blocked, err := st.BlockedSlug(ctx, strings.ToLower(mixed)); err != nil || blocked {
		t.Errorf("after lifting the mixed-case pattern, still blocked = %v, %v", blocked, err)
	}

	blocks, err := st.ListBlocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, b := range blocks {
		if b.Pattern == fam+"-*" || b.Pattern == slugless {
			seen++
			if b.CreatedBy != "admin@example.com" {
				t.Errorf("block %q created_by = %q", b.Pattern, b.CreatedBy)
			}
		}
	}
	if seen != 2 {
		t.Errorf("ListBlocks returned %d of the 2 patterns just added", seen)
	}
}

// A map's entries are its live head, and they are written without going
// through Put. A block has to stop them too, or lifting restores a document
// whose content changed while it was supposed to be gone.
func TestBlock_RefusesMapWrites(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("blocked-map")
	mapID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeMap, NamedSlug: &slug, Title: "block test",
		ContentType: pgstore.MapContentType, Content: []byte(""),
		Creator: "map-owner@example.com", MapID: mapID,
	}); err != nil {
		t.Fatal(err)
	}
	write := []pgstore.MapEntryWrite{{Key: "k", Value: []byte(`"v"`)}}
	if _, err := st.MapPut(ctx, slug, mapID, write, "map-owner@example.com", nil); err != nil {
		t.Fatalf("precondition: map write should succeed before the block: %v", err)
	}

	if err := st.AddBlock(ctx, slug, "test", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, slug) })

	if _, err := st.MapPut(ctx, slug, mapID, write, "map-owner@example.com", nil); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("MapPut on a blocked slug = %v, want ErrNotFound", err)
	}
	if _, err := st.MapDelete(ctx, slug, mapID, []string{"k"}, nil); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("MapDelete on a blocked slug = %v, want ErrNotFound", err)
	}

	// The entry the block was supposed to freeze is still there afterwards.
	if _, err := st.RemoveBlock(ctx, slug); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MapGet(ctx, mapID, "k"); err != nil {
		t.Errorf("after lift the entry should be intact: %v", err)
	}
}

// The review path is the one read that ignores a block, so what it refuses
// matters more than what it returns: it must be useless for reading anything
// that is not blocked, or it is a way around the ACL for every admin.
func TestBlockedReview_OnlyEverReturnsBlockedDocuments(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	fam := unique("review")
	hidden, other := fam+"-hidden", fam+"-visible"
	putText(t, st, hidden)
	putText(t, st, other)

	// Not blocked yet: the review read refuses it, even though it exists and
	// the caller would be allowed to read it the ordinary way.
	if _, err := st.GetBlockedBySlug(ctx, hidden); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetBlockedBySlug on an unblocked document = %v, want ErrNotFound", err)
	}

	if err := st.AddBlock(ctx, hidden, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, hidden) })

	row, err := st.GetBlockedBySlug(ctx, hidden)
	if err != nil {
		t.Fatalf("GetBlockedBySlug on a blocked document: %v", err)
	}
	if row.NamedSlug == nil || *row.NamedSlug != hidden {
		t.Errorf("resolved to %v, want %q", row.NamedSlug, hidden)
	}
	// The neighbour is not blocked, so the review read still refuses it.
	if _, err := st.GetBlockedBySlug(ctx, other); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetBlockedBySlug on a sibling document = %v, want ErrNotFound", err)
	}

	matches, err := st.ListBlockedMatches(ctx, hidden)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].NamedSlug == nil || *matches[0].NamedSlug != hidden {
		t.Fatalf("ListBlockedMatches = %d rows, want exactly the blocked one", len(matches))
	}
	if matches[0].Title == "" || matches[0].Creator == "" {
		t.Errorf("match carries no identity: %+v", matches[0])
	}

	// A glob reports every document it covers, so an admin can see the cost of
	// lifting one entry rather than guessing at the family.
	globbed, err := st.ListBlockedMatches(ctx, fam+"-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(globbed) != 2 {
		t.Errorf("ListBlockedMatches for %q = %d rows, want both documents", fam+"-*", len(globbed))
	}
}

// A slugless artifact can only be blocked by its id, so resolving the review
// read by slug alone would leave exactly those documents unreadable to the
// admin who blocked them. Every ATTACHMENT is one.
func TestBlockedReview_ResolvesASluglessDocumentByID(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	id := putText(t, st, "")
	uid := mustUUID(t, id)

	if _, err := st.GetBlockedByID(ctx, uid); !errors.Is(err, pgstore.ErrNotFound) {
		t.Errorf("GetBlockedByID on an unblocked document = %v, want ErrNotFound", err)
	}

	if err := st.AddBlock(ctx, id, "", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.RemoveBlock(ctx, id) })

	row, err := st.GetBlockedByID(ctx, uid)
	if err != nil {
		t.Fatalf("GetBlockedByID on a blocked slugless document: %v", err)
	}
	if pgstore.UUIDFromPG(row.ArtifactID).String() != id {
		t.Errorf("resolved to %s, want %s", pgstore.UUIDFromPG(row.ArtifactID), id)
	}
	if row.NamedSlug != nil {
		t.Errorf("fixture is not slugless: %v", *row.NamedSlug)
	}

	matches, err := st.ListBlockedMatches(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ArtifactID != id {
		t.Fatalf("ListBlockedMatches by id = %d rows, want the slugless document", len(matches))
	}
}
