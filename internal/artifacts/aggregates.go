package artifacts

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

// ScopeTypeCount mirrors pgstore.ScopeTypeCount on the wire.
type ScopeTypeCount struct {
	Type  string `json:"type"`
	Count int64  `json:"count"`
}

// ScopeCount mirrors pgstore.ScopeCount on the wire.
type ScopeCount struct {
	Scope string `json:"scope"`
	Count int64  `json:"count"`
}

// LabelCount mirrors pgstore.LabelCount on the wire.
type LabelCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// ContentTypeCount mirrors pgstore.ContentTypeCount on the wire.
type ContentTypeCount struct {
	ContentType string `json:"content_type"`
	Count       int64  `json:"count"`
}

// AggregatesResponse is the body of GET /api/artifacts/aggregates.
type AggregatesResponse struct {
	ScopeTypes   []ScopeTypeCount   `json:"scope_types"`
	Scopes       []ScopeCount       `json:"scopes"`
	Labels       []LabelCount       `json:"labels"`
	ContentTypes []ContentTypeCount `json:"content_types"`
}

// aggregatesCacheTTL bounds how stale the cached aggregates may be.
// 60s is well under any UX-noticeable window for these counts.
const aggregatesCacheTTL = 60 * time.Second

// per-caller cache: aggregates depend on which artifacts the caller
// can read, so a global single-slot cache would leak labels/scopes
// across users. Empty-key entries serve the admin path. Cache is
// bounded only by the active-caller set (small in practice).
type aggregatesEntry struct {
	at     time.Time
	cached AggregatesResponse
}

var (
	aggCacheMu sync.Mutex
	aggCache   = map[string]aggregatesEntry{}
)

// Aggregates returns label/scope histograms restricted to artifacts
// `caller` can read. Admins should pass "" so the filter is bypassed
// and the global histogram is returned.
func (s *Service) Aggregates(ctx context.Context, caller string) (AggregatesResponse, error) {
	aggCacheMu.Lock()
	if entry, ok := aggCache[caller]; ok && time.Since(entry.at) < aggregatesCacheTTL {
		aggCacheMu.Unlock()
		return entry.cached, nil
	}
	aggCacheMu.Unlock()

	res, err := s.store.Aggregates(ctx, pgstore.AggregatesInput{
		// 30 to feed the sidebar's top-30 labels list; scopes are sliced
		// to a handful in the UI, so the extra rows are harmless.
		Limit:       30,
		CallerEmail: caller,
	})
	if err != nil {
		return AggregatesResponse{}, err
	}
	out := AggregatesResponse{
		ScopeTypes:   make([]ScopeTypeCount, 0, len(res.ScopeTypes)),
		Scopes:       make([]ScopeCount, 0, len(res.Scopes)),
		Labels:       make([]LabelCount, 0, len(res.Labels)),
		ContentTypes: make([]ContentTypeCount, 0, len(res.ContentTypes)),
	}
	for _, r := range res.ScopeTypes {
		out.ScopeTypes = append(out.ScopeTypes, ScopeTypeCount{Type: r.Type, Count: r.Count})
	}
	for _, r := range res.Scopes {
		out.Scopes = append(out.Scopes, ScopeCount{Scope: r.Scope, Count: r.Count})
	}
	for _, r := range res.Labels {
		out.Labels = append(out.Labels, LabelCount{Label: r.Label, Count: r.Count})
	}
	for _, r := range res.ContentTypes {
		out.ContentTypes = append(out.ContentTypes, ContentTypeCount{ContentType: r.ContentType, Count: r.Count})
	}

	aggCacheMu.Lock()
	aggCache[caller] = aggregatesEntry{at: time.Now(), cached: out}
	aggCacheMu.Unlock()
	return out, nil
}

func (s *Service) httpAggregates(w http.ResponseWriter, r *http.Request) {
	caller, err := s.callerForList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	res, err := s.Aggregates(r.Context(), caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// browseFacets are the facet keys the Browse page may request — validated
// here before ever reaching pgstore.BrowseAggregates.
var browseFacets = map[string]bool{
	"type": true, "label": true, "scope": true, "content_type": true,
	// "owner" is special: counts are unfiltered (all artifacts by that email)
	// so visitors can see who creates what in aggregate, but the click-through
	// to the catalog naturally applies access filtering — only artifacts the
	// viewer can read are returned there.
	"owner": true,
}

// BrowseValueCount mirrors pgstore.BrowseValueCount on the wire.
type BrowseValueCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// BrowseAggregatesResponse is the body of GET /api/artifacts/aggregates/browse.
type BrowseAggregatesResponse struct {
	Values []BrowseValueCount `json:"values"`
	Total  int64              `json:"total"`
}

// clampBrowsePageSize caps page_size at pgstore.BrowseAggregates' own no-op
// threshold (a Limit >500 there silently resets to 50, same pattern as
// List). Clamping here — before Offset is computed from pageSize — keeps
// the two in agreement; otherwise a page_size >500 makes Offset assume a
// far larger window than the Limit that actually reaches Postgres, and most
// rows in between are never returned on any page.
func clampBrowsePageSize(n int32) int32 {
	if n > 500 {
		return 500
	}
	return n
}

// httpBrowseAggregates serves the Browse page: every distinct value for one
// facet, paged and sorted. Unlike httpAggregates (fixed top-N, 60s-cached
// for the sidebar's frequent polling), this is uncached — the Browse page is
// visited far less often and its result varies by facet/sort/page, which
// would make a correct cache key as large as the query itself.
func (s *Service) httpBrowseAggregates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	facet := q.Get("facet")
	if !browseFacets[facet] {
		writeError(w, http.StatusBadRequest, "bad_request",
			"facet must be one of: type, label, scope, content_type, owner")
		return
	}
	pageSize := int32(50)
	if v := q.Get("page_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pageSize = int32(n)
		}
	}
	pageSize = clampBrowsePageSize(pageSize)
	page := 1
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}

	caller, err := s.callerForList(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// The "owner" facet shows counts across ALL artifacts regardless of who
	// the viewer is: the intent is "how many artifacts does this person own in
	// total". The click-through to the catalog filters by access normally, so
	// a viewer only sees the subset they can actually read. Every other facet
	// still respects the per-caller access filter.
	browseCallerEmail := caller
	if facet == "owner" {
		browseCallerEmail = ""
	}
	res, err := s.store.BrowseAggregates(r.Context(), pgstore.BrowseAggregatesInput{
		Facet:       facet,
		Sort:        q.Get("sort"),
		Dir:         q.Get("dir"),
		Limit:       pageSize,
		Offset:      int32(page-1) * pageSize,
		CallerEmail: browseCallerEmail,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	out := BrowseAggregatesResponse{Values: make([]BrowseValueCount, 0, len(res.Values)), Total: res.Total}
	for _, v := range res.Values {
		out.Values = append(out.Values, BrowseValueCount{Value: v.Value, Count: v.Count})
	}
	writeJSON(w, http.StatusOK, out)
}
