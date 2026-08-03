//go:build integration

package pgstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// idSet collects the artifact ids returned by a list/search so tests can
// assert membership without depending on ordering.
func idSet(res pgstore.ListResult) map[pgtype.UUID]bool {
	m := make(map[pgtype.UUID]bool, len(res.Rows))
	for _, r := range res.Rows {
		m[r.ArtifactID] = true
	}
	return m
}

// A `-label:X` token must drop rows carrying label X while keeping every
// other row in scope — crucially including rows with NO labels at all. The
// empty-array row is the regression guard: a naive `NOT (labels @> …)` over a
// nullable column would silently exclude it, but labels is NOT NULL DEFAULT
// '{}' so the exclusion must still let it through.
func TestList_NegatedLabel_KeepsEmptyAndOthers(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	scope := unique("neglabel") // anchors the cohort to just these rows

	put := func(labels []string) pgtype.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: unique("t"), ContentType: "text/plain",
			Content: []byte("x"), Creator: "alice@example.com",
			Scopes: []string{scope}, Labels: labels,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hard-delete on test exit so these rows don't pollute the shared
		// test DB — TestAggregates asserts on the global top-N labels and
		// breaks if leftover cohort labels crowd out its expected ones.
		t.Cleanup(func() { _, _ = st.HardDeleteByID(context.Background(), uuid.UUID(row.ArtifactID.Bytes)) })
		return row.ArtifactID
	}
	keepFoo := put([]string{"foo"})
	dropBar := put([]string{"bar"})
	keepNone := put(nil)

	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Scope: &scope, NotLabels: []string{"bar"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idSet(res)
	if res.Total != 2 || len(got) != 2 {
		t.Fatalf("expected 2 rows, got total=%d rows=%d", res.Total, len(res.Rows))
	}
	if got[dropBar] {
		t.Errorf("bar-labelled row should be excluded by -label:bar")
	}
	if !got[keepFoo] || !got[keepNone] {
		t.Errorf("foo row and label-less row must survive -label:bar")
	}
}

// `-scope:X` (exact) drops rows whose scopes contain X; `-scope:topic:*`
// (glob) drops rows with ANY scope under topic:. Scope-less rows survive both.
func TestList_NegatedScope_ExactAndGlob(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	cohort := unique("negscope") // anchor by label

	put := func(scopes []string) pgtype.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: unique("t"), ContentType: "text/plain",
			Content: []byte("x"), Creator: "alice@example.com",
			Scopes: scopes, Labels: []string{cohort},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hard-delete on test exit so these rows don't pollute the shared
		// test DB — TestAggregates asserts on the global top-N labels and
		// breaks if leftover cohort labels crowd out its expected ones.
		t.Cleanup(func() { _, _ = st.HardDeleteByID(context.Background(), uuid.UUID(row.ArtifactID.Bytes)) })
		return row.ArtifactID
	}
	alpha := put([]string{"topic:alpha"})
	beta := put([]string{"topic:beta"})
	none := put(nil)

	exact, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{cohort}, NotScope: []string{"topic:beta"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idSet(exact)
	if got[beta] || !got[alpha] || !got[none] {
		t.Errorf("-scope:topic:beta should drop only beta; got alpha=%v beta=%v none=%v",
			got[alpha], got[beta], got[none])
	}

	glob, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{cohort}, NotScope: []string{"topic:*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got = idSet(glob)
	if got[alpha] || got[beta] || !got[none] {
		t.Errorf("-scope:topic:* should drop alpha+beta, keep scope-less; got alpha=%v beta=%v none=%v",
			got[alpha], got[beta], got[none])
	}
}

// `-creator:X` and `-type:X` each exclude matching rows independently.
func TestList_NegatedCreatorAndType(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	cohort := unique("negct")
	alice := unique("alice") + "@example.com"
	bob := unique("bob") + "@example.com"

	put := func(creator, typ string) pgtype.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: typ, Title: unique("t"), ContentType: "text/plain",
			Content: []byte("x"), Creator: creator, Labels: []string{cohort},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hard-delete on test exit so these rows don't pollute the shared
		// test DB — TestAggregates asserts on the global top-N labels and
		// breaks if leftover cohort labels crowd out its expected ones.
		t.Cleanup(func() { _, _ = st.HardDeleteByID(context.Background(), uuid.UUID(row.ArtifactID.Bytes)) })
		return row.ArtifactID
	}
	aliceText := put(alice, pgstore.TypeText)
	bobText := put(bob, pgstore.TypeText)
	alicePkg := put(alice, pgstore.TypePackage)

	noBob, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{cohort}, NotCreator: []string{bob},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idSet(noBob)
	if got[bobText] || !got[aliceText] || !got[alicePkg] {
		t.Errorf("-creator:bob should drop only bob's row")
	}

	noPkg, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{cohort}, NotArtifactType: []string{pgstore.TypePackage},
	})
	if err != nil {
		t.Fatal(err)
	}
	got = idSet(noPkg)
	if got[alicePkg] || !got[aliceText] || !got[bobText] {
		t.Errorf("-type:PACKAGE should drop only the PACKAGE row")
	}
}

// `-content_type:X` excludes rows whose content_type exactly matches X,
// independently of other filters — the scalar-column exclusion pattern
// mirroring -creator:/-type:.
func TestList_NegatedContentType(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	cohort := unique("negct")

	put := func(contentType string) pgtype.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: unique("t"), ContentType: contentType,
			Content: []byte("x"), Creator: "alice@example.com", Labels: []string{cohort},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.HardDeleteByID(context.Background(), uuid.UUID(row.ArtifactID.Bytes)) })
		return row.ArtifactID
	}
	md := put("text/markdown")
	plain := put("text/plain")

	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Labels: []string{cohort}, NotContentType: []string{"text/markdown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idSet(res)
	if got[md] {
		t.Errorf("-content_type:text/markdown should drop the markdown row")
	}
	if !got[plain] {
		t.Errorf("-content_type:text/markdown should keep the plain-text row")
	}
}

// `label:keep*` (glob) matches every label starting with "keep", not just the
// literal "keep" — the positive counterpart of negated label glob.
func TestList_LabelGlob(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})
	scope := unique("labelglob")
	prefix := unique("keep") // unique so the glob can't catch unrelated rows

	put := func(label string) pgtype.UUID {
		row, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: pgstore.TypeText, Title: unique("t"), ContentType: "text/plain",
			Content: []byte("x"), Creator: "alice@example.com",
			Scopes: []string{scope}, Labels: []string{label},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Hard-delete on test exit so these rows don't pollute the shared
		// test DB — TestAggregates asserts on the global top-N labels and
		// breaks if leftover cohort labels crowd out its expected ones.
		t.Cleanup(func() { _, _ = st.HardDeleteByID(context.Background(), uuid.UUID(row.ArtifactID.Bytes)) })
		return row.ArtifactID
	}
	exact := put(prefix)            // e.g. keep-1a2b
	suffixed := put(prefix + "ish") // e.g. keep-1a2bish
	other := put(unique("drop"))

	res, err := st.List(ctx, pgstore.ListInput{
		Limit: 50, Scope: &scope, Labels: []string{prefix + "*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := idSet(res)
	if !got[exact] || !got[suffixed] {
		t.Errorf("label:%s* must match both %q and %q", prefix, prefix, prefix+"ish")
	}
	if got[other] {
		t.Errorf("label:%s* must not match an unrelated label", prefix)
	}
}
