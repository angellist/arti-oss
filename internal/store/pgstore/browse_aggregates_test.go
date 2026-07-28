//go:build integration

package pgstore_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/angellist/arti-oss/internal/store/blob"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// BrowseAggregates must return every distinct value for a facet (not a
// top-N), paged and sorted — the Browse page needs to page through all
// labels/content-types/scopes, unlike the sidebar's top-N Aggregates.
//
// "type" is a closed 4-value enum, so seeding one row of each guarantees a
// deterministic universe regardless of what the shared test DB already
// holds: Total must be exactly 4, and paging by name ASC at page_size=2
// must split them alphabetically across two pages.
func TestBrowseAggregates_Type_PaginatesByName(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	for _, typ := range []string{pgstore.TypeText, pgstore.TypePackage, pgstore.TypeApp, pgstore.TypeAttachment} {
		_, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: typ, Title: unique("browsetype"), ContentType: "text/plain",
			Content: []byte("x"), Creator: "alice@example.com",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	page1, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{
		Facet: "type", Sort: "name", Dir: "asc", Limit: 2, Offset: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 4 {
		t.Fatalf("Total = %d, want 4", page1.Total)
	}
	got1 := []string{page1.Values[0].Value, page1.Values[1].Value}
	if !reflect.DeepEqual(got1, []string{"APP", "ATTACHMENT"}) {
		t.Fatalf("page1 = %v, want [APP ATTACHMENT]", got1)
	}

	page2, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{
		Facet: "type", Sort: "name", Dir: "asc", Limit: 2, Offset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if page2.Total != 4 {
		t.Fatalf("page2 Total = %d, want 4", page2.Total)
	}
	got2 := []string{page2.Values[0].Value, page2.Values[1].Value}
	if !reflect.DeepEqual(got2, []string{"PACKAGE", "TEXT"}) {
		t.Fatalf("page2 = %v, want [PACKAGE TEXT]", got2)
	}
}

// sort=count must order by count, not name — verified with two unique
// content_type values of known, distinct counts, isolated from shared-DB
// pollution by uniqueness (same approach as TestAggregates_ContentTypes).
// Positions come from paging the whole facet, not from one big request:
// BrowseAggregates clamps Limit above 500 back down to 50, so asking for
// one huge page silently returns a short one and our two values drop off
// it as soon as the shared test DB accumulates enough content_types.
func TestBrowseAggregates_ContentType_SortByCount(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	mk := func(contentType string, n int) {
		for i := 0; i < n; i++ {
			_, err := st.Put(ctx, pgstore.PutInput{
				ArtifactType: pgstore.TypeText, Title: unique("browsect"), ContentType: contentType,
				Content: []byte("x"), Creator: "alice@example.com",
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	multi := "application/x-" + unique("browsect-multi")   // 3 rows
	single := "application/x-" + unique("browsect-single") // 1 row
	mk(multi, 3)
	mk(single, 1)

	// Overall position of each value across every page, so the assertion
	// holds no matter how many other content_types exist.
	rank := func(dir string) (int, int) {
		const page int32 = 500 // the store's documented maximum
		multiIdx, singleIdx := -1, -1
		for offset := int32(0); ; offset += page {
			res, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{
				Facet: "content_type", Sort: "count", Dir: dir, Limit: page, Offset: offset,
			})
			if err != nil {
				t.Fatal(err)
			}
			for i, r := range res.Values {
				switch r.Value {
				case multi:
					multiIdx = int(offset) + i
				case single:
					singleIdx = int(offset) + i
				}
			}
			done := multiIdx >= 0 && singleIdx >= 0
			exhausted := len(res.Values) == 0 || int64(offset)+int64(len(res.Values)) >= res.Total
			if done || exhausted {
				if !done {
					t.Fatalf("count %s: expected both %s and %s across all %d values", dir, multi, single, res.Total)
				}
				return multiIdx, singleIdx
			}
		}
	}

	multiIdx, singleIdx := rank("desc")
	if multiIdx >= singleIdx {
		t.Fatalf("count desc: %s (count 3) should sort before %s (count 1)", multi, single)
	}

	multiIdx, singleIdx = rank("asc")
	if singleIdx >= multiIdx {
		t.Fatalf("count asc: %s (count 1) should sort before %s (count 3)", single, multi)
	}
}

// Total must reflect the real distinct-value count even when the requested
// page is past the last page (offset beyond the data): COUNT(*) OVER() only
// materializes on scanned rows, so a naive implementation leaves Total at 0
// once LIMIT/OFFSET yield zero rows, breaking "showing X-Y of Z" and
// look-ahead pagination on the Browse page.
func TestBrowseAggregates_TotalSurvivesPastLastPage(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	for _, typ := range []string{pgstore.TypeText, pgstore.TypePackage, pgstore.TypeApp, pgstore.TypeAttachment} {
		_, err := st.Put(ctx, pgstore.PutInput{
			ArtifactType: typ, Title: unique("browsetypepastend"), ContentType: "text/plain",
			Content: []byte("x"), Creator: "alice@example.com",
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Total distinct `type` values is a closed set of (at most) 4, so an
	// offset of 1000 is guaranteed past the last page.
	res, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{
		Facet: "type", Sort: "name", Dir: "asc", Limit: 2, Offset: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Values) != 0 {
		t.Fatalf("expected 0 values past the last page, got %v", res.Values)
	}
	if res.Total != 4 {
		t.Fatalf("Total = %d, want 4 (must survive an empty page)", res.Total)
	}
}

// An unrecognized facet is a caller bug (the HTTP layer validates against a
// fixed allow-list before calling this), so BrowseAggregates errors loudly
// rather than silently returning an empty result.
func TestBrowseAggregates_UnknownFacet(t *testing.T) {
	ctx := context.Background()
	st := pgstore.New(newPool(t), blob.NewInMemory(), pgstore.Config{})

	_, err := st.BrowseAggregates(ctx, pgstore.BrowseAggregatesInput{Facet: "bogus", Limit: 10})
	if err == nil {
		t.Fatal("expected an error for an unknown facet")
	}
}
