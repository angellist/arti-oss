package opensearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SearchInput mirrors pgstore.ListInput for the subset of filters
// OpenSearch needs to handle.
type SearchInput struct {
	Q               string // free-text query (already stripped of field:value tokens)
	ArtifactType    *string
	ContentType     *string
	Creator         *string
	Scope           *string
	Slug            *string
	Labels          []string
	NotArtifactType []string
	NotCreator      []string
	NotScope        []string
	NotLabels       []string
	NotContentType  []string
	CallerEmail     string   // access control
	CallerGroups    []string // group tokens for access control
	IncludeArchived bool
	OnlyArchived    bool
	LatestPerSlug   bool
	HideAppCouch    bool
	Limit           int
	Offset          int
}

// SearchHit is one result from an OpenSearch query.
type SearchHit struct {
	ArtifactID string              `json:"artifact_id"`
	Score      float64             `json:"score"`
	Highlights map[string][]string `json:"highlights,omitempty"`
}

// SearchResult is the response from Search.
type SearchResult struct {
	Hits  []SearchHit `json:"hits"`
	Total int64       `json:"total"`
}

// Search executes a full-text + filter query against the artifacts index.
// Returns matching artifact IDs with relevance scores and highlights.
// The caller is responsible for fetching full artifact rows from Postgres
// using the returned IDs (OpenSearch is not the system of record).
func (c *Client) Search(ctx context.Context, in SearchInput) (SearchResult, error) {
	if c == nil {
		return SearchResult{}, fmt.Errorf("opensearch: client not configured")
	}
	if in.Limit <= 0 || in.Limit > 500 {
		in.Limit = 50
	}
	if in.Offset < 0 {
		in.Offset = 0
	}

	// Try the rich query_string parse first; if OpenSearch rejects the input
	// (HTTP 400 — unbalanced quotes/parens/brackets, stray regex slashes, or
	// any other Lucene syntax it can't parse), retry the same search as a plain
	// multi_match so the user still gets results instead of falling all the way
	// back to Postgres. This replaces a fragile client-side syntax heuristic.
	raw, err := c.execSearch(ctx, in, false)
	if err != nil && in.Q != "" && strings.Contains(err.Error(), "HTTP 400") {
		c.logger.Warn("opensearch: query_string parse failed, retrying as plain text", "err", err)
		raw, err = c.execSearch(ctx, in, true)
	}
	if err != nil {
		return SearchResult{}, err
	}

	var osResp struct {
		Hits struct {
			Total struct {
				Value int64 `json:"value"`
			} `json:"total"`
			Hits []struct {
				ID        string              `json:"_id"`
				Score     float64             `json:"_score"`
				Source    json.RawMessage     `json:"_source"`
				Highlight map[string][]string `json:"highlight"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(raw, &osResp); err != nil {
		return SearchResult{}, fmt.Errorf("opensearch: parse response: %w", err)
	}

	result := SearchResult{
		Total: osResp.Hits.Total.Value,
		Hits:  make([]SearchHit, 0, len(osResp.Hits.Hits)),
	}
	for _, h := range osResp.Hits.Hits {
		hit := SearchHit{
			ArtifactID: h.ID,
			Score:      h.Score,
			Highlights: h.Highlight,
		}
		result.Hits = append(result.Hits, hit)
	}
	return result, nil
}

// execSearch runs one _search request and returns the raw response body.
// plain selects the free-text query type (false = query_string, true =
// multi_match fallback). Errors include the HTTP status, so the caller can
// detect a 400 and retry with plain=true.
func (c *Client) execSearch(ctx context.Context, in SearchInput, plain bool) ([]byte, error) {
	body := map[string]any{
		"query":   buildQuery(in, plain),
		"size":    in.Limit,
		"from":    in.Offset,
		"_source": []string{"artifact_id"},
		"highlight": map[string]any{
			"fields": map[string]any{
				"title":        map[string]any{"number_of_fragments": 0},
				"description":  map[string]any{"number_of_fragments": 2, "fragment_size": 150},
				"content_text": map[string]any{"number_of_fragments": 3, "fragment_size": 150},
			},
			"pre_tags":  []string{"<mark>"},
			"post_tags": []string{"</mark>"},
		},
		"track_total_hits": true,
	}
	// When no free text is provided, sort by created_at desc (same as Postgres).
	if in.Q == "" {
		body["sort"] = []map[string]any{
			{"created_at": map[string]any{"order": "desc"}},
		}
	}
	resp, err := c.do(ctx, "POST", "/"+IndexName+"/_search", body)
	if err != nil {
		return nil, fmt.Errorf("opensearch: search: %w", err)
	}
	return readBody(resp)
}

// buildQuery constructs the OpenSearch bool query from the search input.
// plain forces the free-text portion to a multi_match (used as a fallback when
// query_string rejects the input).
func buildQuery(in SearchInput, plain bool) map[string]any {
	must := []any{}
	filter := []any{}
	mustNot := []any{}

	// Free-text search. Delegated to OpenSearch's query_string (Lucene) so the
	// box supports phrases ("vr deployment"), fielded terms (title:foo,
	// title:"vr deployment"), booleans (AND/OR/NOT) and parentheses — see
	// freeTextClause.
	if in.Q != "" {
		must = append(must, freeTextClause(in.Q, plain))
	}

	// Structured filters — exact match on keyword fields.
	if in.ArtifactType != nil && *in.ArtifactType != "" {
		filter = append(filter, termOrWildcard("artifact_type", *in.ArtifactType))
	}
	if in.ContentType != nil && *in.ContentType != "" {
		filter = append(filter, termOrWildcard("content_type", *in.ContentType))
	}
	if in.Creator != nil && *in.Creator != "" {
		// creator is lowercased at index time (rowToDoc), so the filter
		// value must be lowercased too or a mixed-case creator:Alice@x.com
		// query would never match.
		filter = append(filter, termOrWildcard("creator", strings.ToLower(*in.Creator)))
	}
	if in.Scope != nil && *in.Scope != "" {
		filter = append(filter, termOrWildcard("scopes", *in.Scope))
	}
	if in.Slug != nil && *in.Slug != "" {
		filter = append(filter, termOrWildcard("named_slug", *in.Slug))
	}
	for _, l := range in.Labels {
		filter = append(filter, termOrWildcard("labels", l))
	}

	// Negated filters.
	for _, v := range in.NotArtifactType {
		mustNot = append(mustNot, termOrWildcard("artifact_type", v))
	}
	for _, v := range in.NotCreator {
		mustNot = append(mustNot, termOrWildcard("creator", strings.ToLower(v)))
	}
	for _, v := range in.NotScope {
		mustNot = append(mustNot, termOrWildcard("scopes", v))
	}
	for _, v := range in.NotLabels {
		mustNot = append(mustNot, termOrWildcard("labels", v))
	}
	for _, v := range in.NotContentType {
		mustNot = append(mustNot, termOrWildcard("content_type", v))
	}

	// Archive state.
	switch {
	case in.OnlyArchived:
		filter = append(filter, map[string]any{"term": map[string]any{"is_deleted": true}})
	case !in.IncludeArchived:
		filter = append(filter, map[string]any{"term": map[string]any{"is_deleted": false}})
	}

	// Latest-per-slug: only return the latest version of each slug.
	if in.LatestPerSlug {
		filter = append(filter, map[string]any{"term": map[string]any{"is_latest": true}})
	}

	// Hide app:couch-scoped artifacts from non-admin callers.
	if in.HideAppCouch {
		mustNot = append(mustNot, map[string]any{"term": map[string]any{"scopes": "app:couch"}})
	}

	// Access control: mirror pgstore.buildWhere's access predicate.
	// Caller must be the creator, or allowed_access must contain '*',
	// the caller's email, one of the caller's group tokens, or a glob
	// pattern (e.g. *@domain.com) that matches the caller's email.
	if in.CallerEmail != "" {
		lowerEmail := strings.ToLower(in.CallerEmail)
		accessShould := []any{
			map[string]any{"term": map[string]any{"creator": lowerEmail}},
			map[string]any{"term": map[string]any{"allowed_access": "*"}},
			map[string]any{"term": map[string]any{"allowed_access": lowerEmail}},
		}
		for _, g := range in.CallerGroups {
			accessShould = append(accessShould, map[string]any{
				"term": map[string]any{"allowed_access": g},
			})
		}
		// Glob patterns in allowed_access (e.g. *@partner.io): iterate
		// stored entries and glob-match each against the caller's email,
		// mirroring pgstore.matchGlob exactly — case-insensitive, full-string
		// anchored, `*` (any run) and `?` (one char), backtracking on `*`.
		// We deliberately do NOT use Painless regex (`=~`): that operator
		// only accepts a compile-time regex literal, not a String built at
		// runtime, and dynamic regex via java.util.regex.Pattern is not
		// allowlisted under the default `script.painless.regex.enabled:
		// limited`. A regex approach fails to compile, which made every
		// access-controlled search silently fall back to Postgres. Char
		// comparisons use ASCII codes (42='*', 63='?') since Painless
		// treats quoted literals as Strings, not chars.
		accessShould = append(accessShould, map[string]any{
			"script": map[string]any{
				"script": map[string]any{
					"source": `
						String s = params.email.toLowerCase();
						int sl = s.length();
						for (def entry : doc['allowed_access']) {
							String p = entry.toLowerCase();
							if (!p.contains('*') && !p.contains('?')) { continue; }
							int pl = p.length();
							int pi = 0, si = 0, starPi = -1, starSi = -1;
							boolean dead = false;
							while (si < sl) {
								if (pi < pl && (p.charAt(pi) == 63 || p.charAt(pi) == s.charAt(si))) {
									pi++; si++;
								} else if (pi < pl && p.charAt(pi) == 42) {
									starPi = pi; starSi = si; pi++;
								} else if (starPi != -1) {
									pi = starPi + 1; starSi++; si = starSi;
								} else {
									dead = true; break;
								}
							}
							if (dead) { continue; }
							while (pi < pl && p.charAt(pi) == 42) { pi++; }
							if (pi == pl) { return true; }
						}
						return false;
					`,
					"lang":   "painless",
					"params": map[string]any{"email": lowerEmail},
				},
			},
		})
		filter = append(filter, map[string]any{
			"bool": map[string]any{
				"should":               accessShould,
				"minimum_should_match": 1,
			},
		})
	}

	boolQuery := map[string]any{}
	if len(must) > 0 {
		boolQuery["must"] = must
	}
	if len(filter) > 0 {
		boolQuery["filter"] = filter
	}
	if len(mustNot) > 0 {
		boolQuery["must_not"] = mustNot
	}
	// When no must clause exists, match everything (filtered list).
	if len(must) == 0 {
		boolQuery["must"] = []any{map[string]any{"match_all": map[string]any{}}}
	}

	return map[string]any{"bool": boolQuery}
}

// freeTextFields is the analyzed field set (with boosts) free-text queries
// search across. Fielded queries like `title:foo` override this per-clause.
var freeTextFields = []string{"title^3", "description^1.5", "content_text", "slug_text", "creator_text", "labels"}

// freeTextClause builds the free-text portion of the query. By default it uses
// query_string — giving phrases ("a b"), fielded terms (title:foo), AND/OR/NOT
// and parentheses, with AND as the default operator (all terms required).
// fuzziness applies to explicit `term~` operators; bare terms match exactly.
// When plain is true (the caller's retry after query_string returned a parse
// error) it falls back to a lenient multi_match over the same fields, so any
// input the Lucene parser rejects still returns results.
func freeTextClause(q string, plain bool) map[string]any {
	if plain {
		return map[string]any{
			"multi_match": map[string]any{
				"query":     q,
				"fields":    freeTextFields,
				"type":      "best_fields",
				"fuzziness": "AUTO:7,11",
			},
		}
	}
	return map[string]any{
		"query_string": map[string]any{
			"query":            q,
			"fields":           freeTextFields,
			"default_operator": "AND",
			"fuzziness":        "AUTO:7,11",
			"lenient":          true,
		},
	}
}

// termOrWildcard produces a term query for exact values and a wildcard
// query for glob patterns (containing `*`). This mirrors pgstore's
// hasGlob/addFilter behavior.
func termOrWildcard(field, value string) map[string]any {
	if strings.Contains(value, "*") {
		return map[string]any{
			"wildcard": map[string]any{
				field: map[string]any{
					"value":            strings.ToLower(value),
					"case_insensitive": true,
				},
			},
		}
	}
	return map[string]any{"term": map[string]any{field: value}}
}
