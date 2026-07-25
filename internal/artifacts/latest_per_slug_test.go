package artifacts

import (
	"net/http/httptest"
	"testing"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// latestPerSlugFor decides whether the catalog collapses to one row per slug.
// The load-bearing distinction: a `slug:` token typed in the search box is a
// *search* (mergeFilters sets in.Slug so it still filters), NOT a drill-in, so
// the collapse must stay ON. Only the dedicated ?slug= param (a deterministic
// drill-in from a slug chip) turns it off. This mirrors catalogView on the web
// side — both must key the drill-in off the dedicated param, never off a
// slug: token, or the version/archived toggles go missing on slug searches.
func TestLatestPerSlugFor(t *testing.T) {
	slug := "my-report"
	cases := []struct {
		name string
		url  string
		in   pgstore.ListInput
		want bool
	}{
		{"plain catalog collapses to latest-per-slug", "/api/artifacts", pgstore.ListInput{}, true},
		{
			// The regression guard: in.Slug is set (mergeFilters folded the
			// token in for filtering) but the collapse must still apply.
			name: "slug: token in q is a search — still collapses",
			url:  "/api/artifacts/search?q=slug:my-report",
			in:   pgstore.ListInput{Slug: &slug},
			want: true,
		},
		{
			name: "dedicated ?slug= param is a drill-in — no collapse",
			url:  "/api/artifacts?slug=my-report",
			in:   pgstore.ListInput{Slug: &slug},
			want: false,
		},
		{"?all_versions=true — no collapse", "/api/artifacts?all_versions=true", pgstore.ListInput{}, false},
		{"archived-only listing — no collapse", "/api/artifacts?archived=only", pgstore.ListInput{OnlyArchived: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.url, nil)
			if got := latestPerSlugFor(r, tc.in); got != tc.want {
				t.Fatalf("latestPerSlugFor(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
