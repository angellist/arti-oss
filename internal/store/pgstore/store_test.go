//go:build integration

package pgstore_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func mustEnv(t *testing.T, k, def string) string {
	v := os.Getenv(k)
	if v == "" {
		v = def
	}
	if v == "" {
		t.Fatalf("env %s not set", k)
	}
	return v
}

func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := mustEnv(t, "ARTI_TEST_DATABASE_URL",
		"postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func unique(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

func TestPut_InlineText(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		Title:        unique("t"),
		ContentType:  "text/plain",
		Content:      []byte("hello"),
		Creator:      "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.InlineContent == nil || string(row.InlineContent) != "hello" {
		t.Fatalf("expected inline content, got blob_ref=%v inline=%q", row.BlobRef, row.InlineContent)
	}
	if row.BlobRef != nil {
		t.Fatalf("blob_ref should be nil for inline; got %s", *row.BlobRef)
	}
}

func TestPut_LargeText_GoesToBlob(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	big := []byte(strings.Repeat("x", 65*1024)) // 65 KiB > 64 KiB
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		Title:        unique("big"),
		ContentType:  "text/plain",
		Content:      big,
		Creator:      "alice@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if row.InlineContent != nil {
		t.Fatalf("expected blob, got inline")
	}
	if row.BlobRef == nil {
		t.Fatalf("blob_ref nil")
	}
}

func TestVersioning_Slug(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	slug := unique("ver")
	v1, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "v1", ContentType: "text/plain",
		Content: []byte("a"), Creator: "alice@example.com",
		NamedSlug: &slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	v2, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "v2", ContentType: "text/plain",
		Content: []byte("b"), Creator: "alice@example.com",
		NamedSlug: &slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.Version == nil || *v1.Version != 1 {
		t.Fatalf("v1 version got %v", v1.Version)
	}
	if v2.Version == nil || *v2.Version != 2 {
		t.Fatalf("v2 version got %v", v2.Version)
	}
	got, err := st.GetBySlug(ctx, slug, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "v2" {
		t.Fatalf("latest should be v2; got %q", got.Title)
	}
}

func TestList_FilterScopeLabel(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	scope := unique("scope")
	_, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "mem", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Scopes: []string{scope}, Labels: []string{"memory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 10, Scope: &scope, Labels: []string{"memory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 {
		t.Fatalf("expected 1 row, got total=%d rows=%d", res.Total, len(res.Rows))
	}
}

// Attachments are visible in the catalog by default; only rows scoped
// app:couch (the couch app's private state) are hidden, and only when
// HideAppCouch is set (the non-admin catalog path). Admins never set the
// flag, so they still find everything. Access is held constant here (the
// owner can see both rows) so the test isolates the scope filter, not the
// allowed_access gate.
func TestList_HideAppCouch(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	owner := "alice@example.com"
	label := unique("couch-vis")

	// A normal attachment (no app:couch scope) — must stay visible.
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeAttachment, Title: "normal.pdf", ContentType: "application/pdf",
		Content: []byte("%PDF"), Creator: owner, Labels: []string{label},
	}); err != nil {
		t.Fatal(err)
	}
	// A couch-private attachment (app:couch scope) — hidden from non-admins.
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeAttachment, Title: "couch-state.bin", ContentType: "application/octet-stream",
		Content: []byte("state"), Creator: owner, Labels: []string{label},
		Scopes: []string{pgstore.ScopeAppCouch},
	}); err != nil {
		t.Fatal(err)
	}

	// Non-admin catalog (HideAppCouch, scoped to the owner so access allows
	// both): sees the normal attachment, not the app:couch one.
	nonAdmin, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{label}, CallerEmail: owner, HideAppCouch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nonAdmin.Total != 1 || len(nonAdmin.Rows) != 1 {
		t.Fatalf("non-admin: want 1 row (normal attachment), got total=%d rows=%d", nonAdmin.Total, len(nonAdmin.Rows))
	}
	if nonAdmin.Rows[0].Title != "normal.pdf" {
		t.Errorf("non-admin saw %q, want normal.pdf", nonAdmin.Rows[0].Title)
	}

	// Admin (no access filter, HideAppCouch unset): sees both.
	admin, err := st.List(ctx, pgstore.ListInput{Limit: 50, Labels: []string{label}})
	if err != nil {
		t.Fatal(err)
	}
	if admin.Total != 2 || len(admin.Rows) != 2 {
		t.Fatalf("admin: want 2 rows, got total=%d rows=%d", admin.Total, len(admin.Rows))
	}
}

// Free-text search must surface artifacts by their owner, not just by
// title/description/slug — a user searching a coworker's name ("derek")
// expects to find what that coworker created (creator derek.xx@abc.com),
// even when the name appears nowhere in the artifact's text fields.
func TestSearch_MatchesCreator(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	label := unique("creator-search")
	mine, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "untitled", ContentType: "text/plain",
		Content: []byte("x"), Creator: "derek.xx@abc.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second artifact by a different owner, sharing the label, to prove
	// the query filters on creator rather than returning everything.
	_, err = st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "untitled", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.Search(ctx, "derek", pgstore.ListInput{
		Limit: 10, Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 {
		t.Fatalf("expected 1 row matching creator, got total=%d rows=%d", res.Total, len(res.Rows))
	}
	if res.Rows[0].ArtifactID != mine.ArtifactID {
		t.Fatalf("expected derek's artifact %s, got %s", mine.ArtifactID, res.Rows[0].ArtifactID)
	}
}

// Access control set at creation time must actually gate reads. The
// "private" feature (CLI --private, REST/MCP allowed_access:[]) relies on
// the contract that an empty AllowedAccess slice means creator-only,
// distinct from nil which means everyone-authenticated. This locks both
// sides of that distinction from the requester's point of view.
func TestPut_AllowedAccess_PrivateVsPublic(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	const owner = "owner@example.com"
	const other = "stranger@example.com"

	privLabel := unique("private")
	priv, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "secret", ContentType: "text/plain",
		Content: []byte("x"), Creator: owner,
		Labels:        []string{privLabel},
		AllowedAccess: []string{}, // explicit empty → creator-only
	})
	if err != nil {
		t.Fatal(err)
	}

	pubLabel := unique("public")
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "open", ContentType: "text/plain",
		Content: []byte("x"), Creator: owner,
		Labels:        []string{pubLabel},
		AllowedAccess: nil, // nil → everyone-authenticated
	}); err != nil {
		t.Fatal(err)
	}

	// A stranger sees the public one but NOT the private one.
	strangerPriv, err := st.List(ctx, pgstore.ListInput{Limit: 10, Labels: []string{privLabel}, CallerEmail: other})
	if err != nil {
		t.Fatal(err)
	}
	if strangerPriv.Total != 0 {
		t.Fatalf("private artifact must be hidden from non-creator; stranger saw %d", strangerPriv.Total)
	}
	strangerPub, err := st.List(ctx, pgstore.ListInput{Limit: 10, Labels: []string{pubLabel}, CallerEmail: other})
	if err != nil {
		t.Fatal(err)
	}
	if strangerPub.Total != 1 {
		t.Fatalf("public artifact must be visible to any authenticated caller; stranger saw %d", strangerPub.Total)
	}

	// The creator always sees their own private artifact.
	ownerPriv, err := st.List(ctx, pgstore.ListInput{Limit: 10, Labels: []string{privLabel}, CallerEmail: owner})
	if err != nil {
		t.Fatal(err)
	}
	if ownerPriv.Total != 1 || len(ownerPriv.Rows) != 1 || ownerPriv.Rows[0].ArtifactID != priv.ArtifactID {
		t.Fatalf("creator must see their own private artifact; got total=%d", ownerPriv.Total)
	}
}

func TestList_LatestPerSlug(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// A slug with three versions, plus a slug-less artifact. Use a shared
	// label so the list query scopes to just this test's rows.
	label := unique("lps")
	slug := unique("lps-slug")
	for i, body := range []string{"one", "two", "three"} {
		if _, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: "ver" + body, ContentType: "text/plain",
			Content: []byte(body), Creator: "alice@example.com",
			NamedSlug: &slug, Labels: []string{label},
		}); err != nil {
			t.Fatalf("put v%d: %v", i+1, err)
		}
	}
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "loner", ContentType: "text/plain",
		Content: []byte("z"), Creator: "alice@example.com",
		Labels: []string{label},
	}); err != nil {
		t.Fatalf("put loner: %v", err)
	}

	// Without LatestPerSlug: 3 versions + 1 loner = 4 rows.
	all, err := st.List(ctx, pgstore.ListInput{Limit: 50, Labels: []string{label}})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total != 4 {
		t.Fatalf("expected 4 rows without LatestPerSlug, got %d", all.Total)
	}

	// With LatestPerSlug: 1 latest-of-slug + 1 loner = 2 rows; the slug
	// row must be v3 ("verthree").
	latest, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{label}, LatestPerSlug: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if latest.Total != 2 {
		t.Fatalf("expected 2 rows with LatestPerSlug, got %d", latest.Total)
	}
	var sawSlugV3, sawLoner bool
	for _, r := range latest.Rows {
		if r.NamedSlug != nil && *r.NamedSlug == slug {
			if r.Version == nil || *r.Version != 3 {
				t.Fatalf("slug row should be v3, got %v", r.Version)
			}
			sawSlugV3 = true
		}
		if r.NamedSlug == nil && r.Title == "loner" {
			sawLoner = true
		}
	}
	if !sawSlugV3 || !sawLoner {
		t.Fatalf("expected latest slug v3 + loner; sawSlugV3=%v sawLoner=%v", sawSlugV3, sawLoner)
	}
}

func TestList_LatestPerSlug_IncludeArchived(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// One slug, three versions; archive the newest (v3). A shared label scopes
	// the query to just this test's rows.
	label := unique("lpsa")
	slug := unique("lpsa-slug")
	var v3ID uuid.UUID
	for i, body := range []string{"one", "two", "three"} {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: "ver" + body, ContentType: "text/plain",
			Content: []byte(body), Creator: "alice@example.com",
			NamedSlug: &slug, Labels: []string{label},
		})
		if err != nil {
			t.Fatalf("put v%d: %v", i+1, err)
		}
		v3ID = pgstore.UUIDFromPG(row.ArtifactID)
	}
	if n, err := st.ArchiveByID(ctx, v3ID); err != nil || n != 1 {
		t.Fatalf("archive v3: n=%d err=%v", n, err)
	}

	oneRow := func(t *testing.T, in pgstore.ListInput) sqlc.Artifact {
		t.Helper()
		in.Limit, in.Labels = 50, []string{label}
		res, err := st.List(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if res.Total != 1 {
			t.Fatalf("expected 1 row, got %d", res.Total)
		}
		return res.Rows[0]
	}

	// Default collapse excludes archived from the candidate set: latest is the
	// live v2 (guards that this change didn't alter the default behavior).
	if got := oneRow(t, pgstore.ListInput{LatestPerSlug: true}); got.Version == nil || *got.Version != 2 || got.DeletedAt.Valid {
		t.Fatalf("latest-only should be live v2; version=%v archived=%v", got.Version, got.DeletedAt.Valid)
	}

	// With archived included, the archived v3 rejoins the candidate set and is
	// the latest — the composition this change enables.
	if got := oneRow(t, pgstore.ListInput{LatestPerSlug: true, IncludeArchived: true}); got.Version == nil || *got.Version != 3 || !got.DeletedAt.Valid {
		t.Fatalf("latest-only+archived should be archived v3; version=%v archived=%v", got.Version, got.DeletedAt.Valid)
	}

	// Without collapse, the archived toggle just widens the row set.
	count := func(in pgstore.ListInput) int64 {
		in.Limit, in.Labels = 50, []string{label}
		res, err := st.List(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return res.Total
	}
	if n := count(pgstore.ListInput{}); n != 2 { // v1, v2 (archived v3 hidden)
		t.Fatalf("all-versions live should be 2, got %d", n)
	}
	if n := count(pgstore.ListInput{IncludeArchived: true}); n != 3 { // v1, v2, v3
		t.Fatalf("all-versions incl archived should be 3, got %d", n)
	}
}

func TestArchive(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	row, _ := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "a", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
	})
	id := pgstore.UUIDFromPG(row.ArtifactID)
	n, err := st.ArchiveByID(ctx, id)
	if err != nil || n != 1 {
		t.Fatalf("archive: n=%d err=%v", n, err)
	}
	// Second archive is a no-op (already deleted).
	n, _ = st.ArchiveByID(ctx, id)
	if n != 0 {
		t.Fatalf("expected 0 from repeat archive, got %d", n)
	}
}

func TestContentRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	row, _ := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "rt", ContentType: "text/plain",
		Content: []byte("payload"), Creator: "alice@example.com",
	})
	rc, err := st.Content(ctx, row)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "payload" {
		t.Fatalf("got %q", string(b))
	}
	_ = uuid.UUID{}
}

func TestScopes_PutFilterDualWrite(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	scopes := []string{"a:bt-auto-route", "u:lavina.kalwani"}
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText,
		Title:        unique("scoped"),
		ContentType:  "text/plain",
		Content:      []byte("x"),
		Creator:      "alice@example.com",
		Scopes:       scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stored as an array...
	if len(row.Scopes) != 2 || row.Scopes[0] != "a:bt-auto-route" {
		t.Fatalf("scopes = %v", row.Scopes)
	}
	// ...and dual-written into the legacy scalar column as scopes[0].
	if row.Scope == nil || *row.Scope != "a:bt-auto-route" {
		t.Fatalf("legacy scope = %v, want a:bt-auto-route", row.Scope)
	}

	// Exact-membership filter finds it by the SECOND scope.
	exact := "u:lavina.kalwani"
	res, err := st.List(ctx, pgstore.ListInput{Scope: &exact, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(res.Rows, row.ArtifactID) {
		t.Fatalf("exact scope filter did not return the artifact")
	}

	// Glob filter matches the a:* family.
	glob := "a:*"
	res, err = st.List(ctx, pgstore.ListInput{Scope: &glob, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !containsID(res.Rows, row.ArtifactID) {
		t.Fatalf("glob scope filter did not return the artifact")
	}
}

// containsID reports whether any row has the given artifact_id.
func containsID(rows []sqlc.Artifact, id pgtype.UUID) bool {
	for _, r := range rows {
		if r.ArtifactID == id {
			return true
		}
	}
	return false
}

func TestAggregates(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	mk := func(scope string, labels []string) {
		_, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText,
			Title:        unique("agg"),
			ContentType:  "text/plain",
			Content:      []byte("x"),
			Creator:      "alice@example.com",
			Scopes:       []string{scope},
			Labels:       labels,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// The integration DB is shared across the whole suite, and Aggregates
	// returns only the top-N labels by count (Store clamps the limit to 100 —
	// anything larger silently falls back to 20, so a bigger number does NOT
	// widen the window). A count-1 label can therefore be crowded out of the
	// result by the many one-off labels other tests seed. To stay robust we
	// (a) use unique label names so nothing else contributes to these counts,
	// and (b) seed each on enough rows that it clearly outranks the count-1
	// field and lands in the top-N regardless of how much else is in the DB.
	// (PR #104.)
	multi := unique("agg-multi")   // seeded on 3 rows → count 3
	single := unique("agg-single") // seeded on 2 rows → count 2
	mk("user:alice@example.com", []string{multi, single})
	mk("user:bob@example.com", []string{multi})
	mk("topic:platform:auth", []string{multi, single})

	agg, err := st.Aggregates(ctx, pgstore.AggregatesInput{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	// Scope-type "user" should be at least 2; "topic" at least 1. (Scope types
	// are a tiny set, never at risk of the top-N truncation that bites labels.)
	scopeCounts := map[string]int64{}
	for _, r := range agg.ScopeTypes {
		scopeCounts[r.Type] = r.Count
	}
	if scopeCounts["user"] < 2 {
		t.Fatalf("expected user scope count >= 2, got %d", scopeCounts["user"])
	}
	if scopeCounts["topic"] < 1 {
		t.Fatalf("expected topic scope count >= 1, got %d", scopeCounts["topic"])
	}
	// "topic:platform:auth" must parse to type "topic", not "topic:platform".
	if _, leaked := scopeCounts["topic:platform"]; leaked {
		t.Fatalf("scope prefix should split on first ':' only")
	}

	// Exact per-slug counts: the unique labels are seeded on a known number of
	// distinct (slug-less) rows, so no other test perturbs them — this also
	// covers "count once per slug" since each mk() is its own slug-less row.
	labelCounts := map[string]int64{}
	for _, r := range agg.Labels {
		labelCounts[r.Label] = r.Count
	}
	if labelCounts[multi] != 3 {
		t.Fatalf("expected %s label count = 3, got %d", multi, labelCounts[multi])
	}
	if labelCounts[single] != 2 {
		t.Fatalf("expected %s label count = 2, got %d", single, labelCounts[single])
	}
}

// Aggregates must also histogram content_type (a scalar column, no unnest)
// alongside scopes/labels, so the sidebar can offer a "Content Types" filter
// section the same way it does for labels.
func TestAggregates_ContentTypes(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	mk := func(contentType string, n int) {
		for i := 0; i < n; i++ {
			_, err := st.Put(ctx, pgstore.PutInput{
				ArtifactType: pgstore.TypeText,
				Title:        unique("aggct"),
				ContentType:  contentType,
				Content:      []byte("x"),
				Creator:      "alice@example.com",
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Unique content types isolate these counts from the shared test DB, same
	// approach TestAggregates uses for labels.
	multi := "application/x-" + unique("aggct-multi")   // 3 rows
	single := "application/x-" + unique("aggct-single") // 1 row
	mk(multi, 3)
	mk(single, 1)

	agg, err := st.Aggregates(ctx, pgstore.AggregatesInput{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	counts := map[string]int64{}
	for _, r := range agg.ContentTypes {
		counts[r.ContentType] = r.Count
	}
	if counts[multi] != 3 {
		t.Fatalf("expected %s content_type count = 3, got %d", multi, counts[multi])
	}
	if counts[single] != 1 {
		t.Fatalf("expected %s content_type count = 1, got %d", single, counts[single])
	}
}

// Aggregate counts are per-slug, not per-version: the sidebar should show how
// many distinct artifacts carry a label, not how many saved versions do. A
// slug with N versions all tagged L must add 1 to L's count, and a slug whose
// every version is archived must drop out entirely (count 0).
func TestAggregates_PerSlugNotPerVersion(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	// Unique label isolates this test's counts from the shared DB.
	label := unique("aggslug")
	mkVersion := func(slug string) {
		_, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText,
			Title:        unique("aggslug"),
			ContentType:  "text/plain",
			Content:      []byte("x"),
			Creator:      "alice@example.com",
			NamedSlug:    &slug,
			Labels:       []string{label},
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	slugA, slugB := unique("slug-a"), unique("slug-b")
	mkVersion(slugA) // v1
	mkVersion(slugA) // v2 — same slug, must not double-count
	mkVersion(slugB) // v1

	count := func() int64 {
		agg, err := st.Aggregates(ctx, pgstore.AggregatesInput{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range agg.Labels {
			if r.Label == label {
				return r.Count
			}
		}
		return 0
	}

	// Two slugs (three versions) → count 2, proving per-slug dedup.
	if got := count(); got != 2 {
		t.Fatalf("expected per-slug count 2 (2 slugs, 3 versions), got %d", got)
	}

	// Archiving every version of slugA drops it from the histogram.
	if _, err := st.ArchiveBySlug(ctx, slugA); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("expected count 1 after archiving slugA, got %d", got)
	}

	// All slugs carrying the label archived → count 0.
	if _, err := st.ArchiveBySlug(ctx, slugB); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Fatalf("expected count 0 after archiving all slugs, got %d", got)
	}
}

// Free-text search must tolerate the difference between the separators a
// user types and the ones in the stored title: typing "vc agent" (space)
// is expected to find a title written "vc-agent" (hyphen). We can't rely
// on punctuation matching, so the box splits the query into words and
// matches each as a substring.
func TestSearch_SeparatorTolerantTitle(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	label := unique("septitle")
	want, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "vc-agent", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Scope to the unique label so the count is isolated from other rows in
	// the shared test DB; total==1 then proves the free text matched too.
	res, err := st.Search(ctx, "vc agent", pgstore.ListInput{
		Limit: 10, Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 {
		t.Fatalf(`"vc agent" should match title "vc-agent": got total=%d rows=%d`, res.Total, len(res.Rows))
	}
	if res.Rows[0].ArtifactID != want.ArtifactID {
		t.Fatalf("matched wrong artifact %s", res.Rows[0].ArtifactID)
	}
}

// Word order must not matter: each word is matched independently, so
// "agent vc" finds the same title "vc-agent" as "vc agent" does.
func TestSearch_WordOrderIndependent(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	label := unique("wordorder")
	want, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "vc-agent", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.Search(ctx, "agent vc", pgstore.ListInput{
		Limit: 10, Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].ArtifactID != want.ArtifactID {
		t.Fatalf(`"agent vc" should match title "vc-agent": got total=%d rows=%d`, res.Total, len(res.Rows))
	}
}

// Free text (no label: operator) must also search inside labels, so a user
// can surface artifacts by a label's name from the same box. The label's
// random suffix appears in no other field of any artifact, so a hit can
// only have come from matching the labels column.
func TestSearch_FreeTextMatchesLabel(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	label := unique("freelabel")
	want, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "untitled", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := st.Search(ctx, label, pgstore.ListInput{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].ArtifactID != want.ArtifactID {
		t.Fatalf("free-text %q should match the artifact carrying it as a label: got total=%d rows=%d", label, res.Total, len(res.Rows))
	}
}

// Every word must match (logical AND across words), not just one. With two
// artifacts sharing a label — "vc-agent" and "agent-smith" — searching
// "vc agent" must return only "vc-agent": "agent-smith" matches "agent"
// but lacks "vc". An OR-of-words bug would wrongly return both.
func TestSearch_AllTokensRequired(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	label := unique("andtest")
	want, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "vc-agent", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "agent-smith", ContentType: "text/plain",
		Content: []byte("x"), Creator: "alice@example.com",
		Labels: []string{label},
	}); err != nil {
		t.Fatal(err)
	}

	res, err := st.Search(ctx, "vc agent", pgstore.ListInput{
		Limit: 10, Labels: []string{label},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Rows) != 1 || res.Rows[0].ArtifactID != want.ArtifactID {
		t.Fatalf(`"vc agent" must match only "vc-agent", not "agent-smith": got total=%d rows=%d`, res.Total, len(res.Rows))
	}
}
