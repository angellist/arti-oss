//go:build integration

package pgstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestArtifactViews_DedupeAndAnonymousCounts(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	artifact, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: unique("views"),
		ContentType: "text/plain", Content: []byte("body"), Creator: "owner@x.com",
		NamedSlug: func() *string { s := unique("view-slug"); return &s }(),
	})
	if err != nil {
		t.Fatal(err)
	}
	id := pgstore.UUIDFromPG(artifact.ArtifactID)
	key := *artifact.NamedSlug
	v := int32(1)
	if err := st.RecordArtifactView(ctx, id, key, &v, "reader@x.com", "viewer"); err != nil {
		t.Fatal(err)
	}
	// Viewer identity is normalized before the dedupe comparison.
	if err := st.RecordArtifactView(ctx, id, key, &v, "READER@X.COM", "viewer"); err != nil {
		t.Fatal(err)
	}
	// Anonymous requests are never deduplicated by viewer identity.
	for i := 0; i < 2; i++ {
		if err := st.RecordArtifactView(ctx, id, key, &v, "", "share"); err != nil {
			t.Fatal(err)
		}
	}
	// A named view older than the window is accepted again. Move the first
	// event outside the stats window too, so the aggregate assertions are
	// independent from the dedupe window.
	if _, err := pool.Exec(ctx,
		`UPDATE artifact_views SET at = now() - interval '31 days'
		 WHERE artifact_id = $1 AND viewer = 'reader@x.com'`, id); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordArtifactView(ctx, id, key, &v, "reader@x.com", "viewer"); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ArtifactViewStats(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 4 || stats.Last30d != 3 || stats.UniqueViewers != 1 {
		t.Fatalf("stats = %+v, want total=4 last30d=3 unique=1", stats)
	}
}

func TestArtifactViews_EmptySlugUsesUUIDKeys(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	label := unique("empty-view-sort")
	makeArtifact := func(title string) uuid.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: title, ContentType: "text/plain",
			Content: []byte(title), Creator: "owner@x.com", Labels: []string{label},
		})
		if err != nil {
			t.Fatal(err)
		}
		id := pgstore.UUIDFromPG(row.ArtifactID)
		if _, err := pool.Exec(ctx,
			`UPDATE artifacts SET named_slug = '' WHERE artifact_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	more := makeArtifact("empty slug more views")
	fewer := makeArtifact("empty slug fewer views")
	moreKey, fewerKey := more.String(), fewer.String()
	for i := 0; i < 2; i++ {
		if err := st.RecordArtifactView(ctx, more, moreKey, nil, "", "share"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordArtifactView(ctx, fewer, fewerKey, nil, "", "share"); err != nil {
		t.Fatal(err)
	}
	counts, err := st.ArtifactViewCounts(ctx, []string{moreKey, fewerKey})
	if err != nil {
		t.Fatal(err)
	}
	if counts[moreKey].Total != 2 || counts[fewerKey].Total != 1 {
		t.Fatalf("empty-slug counts = %+v, want separate UUID keys", counts)
	}
	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 10, Labels: []string{label}, OrderBy: "views", OrderDir: "desc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if pgstore.UUIDFromPG(res.Rows[0].ArtifactID) != more {
		t.Fatalf("empty-slug sorted rows = %v, want more-view artifact first", res.Rows)
	}
}

func TestArtifactViews_CountsAndRecent(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	slug := unique("view-counts")
	row, err := st.Put(ctx, pgstore.PutInput{
		ArtifactType: pgstore.TypeText, Title: "counts", ContentType: "text/plain",
		Content: []byte("body"), Creator: "owner@x.com", NamedSlug: &slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := pgstore.UUIDFromPG(row.ArtifactID)
	if err := st.RecordArtifactView(ctx, id, slug, row.Version, "a@x.com", "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordArtifactView(ctx, id, slug, row.Version, "b@x.com", "embed"); err != nil {
		t.Fatal(err)
	}
	counts, err := st.ArtifactViewCounts(ctx, []string{slug, "missing-" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	if counts[slug].Total != 2 || counts[slug].Last30d != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	recent, err := st.ListArtifactViews(ctx, slug, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 || recent[0].Viewer != "b@x.com" || recent[0].Surface != "embed" {
		t.Fatalf("recent = %+v", recent)
	}
	if recent[0].At.Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("recent timestamp is too old: %v", recent[0].At)
	}
}

func TestArtifactViews_SortOrdering(t *testing.T) {
	ctx := context.Background()
	pool := newPool(t)
	st := pgstore.New(pool, blob.NewInMemory(), pgstore.Config{})
	label := unique("view-sort")
	makeArtifact := func(title string) (uuid.UUID, string, error) {
		slug := unique("sort-slug")
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: title, ContentType: "text/plain",
			Content: []byte(title), Creator: "owner@x.com", Labels: []string{label},
			NamedSlug: &slug,
		})
		if err != nil {
			return uuid.Nil, "", err
		}
		return pgstore.UUIDFromPG(row.ArtifactID), slug, nil
	}
	more, moreKey, err := makeArtifact("more views")
	if err != nil {
		t.Fatal(err)
	}
	fewer, fewerKey, err := makeArtifact("fewer views")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := st.RecordArtifactView(ctx, more, moreKey, nil, "", "share"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordArtifactView(ctx, fewer, fewerKey, nil, "", "share"); err != nil {
		t.Fatal(err)
	}
	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 10, Labels: []string{label}, OrderBy: "views", OrderDir: "desc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(res.Rows))
	}
	if pgstore.UUIDFromPG(res.Rows[0].ArtifactID) != more {
		t.Fatalf("sorted rows = %v, want more-view artifact first", res.Rows)
	}
}
