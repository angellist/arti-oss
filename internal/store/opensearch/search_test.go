package opensearch

import "testing"

// NotContentType must produce a must_not clause, mirroring
// NotArtifactType/NotCreator/NotScope/NotLabels exactly — otherwise a
// -content_type: exclusion silently vanishes on the OpenSearch path while
// still working on the Postgres fallback, giving inconsistent search results
// depending on which backend served the request.
func TestBuildQuery_NotContentType(t *testing.T) {
	q := buildQuery(SearchInput{
		Q:              "release notes",
		NotContentType: []string{"text/markdown"},
	}, false)

	boolQuery, ok := q["bool"].(map[string]any)
	if !ok {
		t.Fatalf("query missing bool clause: %#v", q)
	}
	mustNot, ok := boolQuery["must_not"].([]any)
	if !ok || len(mustNot) == 0 {
		t.Fatalf("expected a must_not clause for NotContentType, got %#v", boolQuery["must_not"])
	}
	want := termOrWildcard("content_type", "text/markdown")
	found := false
	for _, m := range mustNot {
		if clause, ok := m.(map[string]any); ok {
			if term, ok := clause["term"].(map[string]any); ok {
				if term["content_type"] == want["term"].(map[string]any)["content_type"] {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("must_not clauses = %#v, want a content_type term clause for text/markdown", mustNot)
	}
}
