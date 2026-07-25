import type { BrowseFacet, BrowseValueCount } from "./types";

// browseRowHref builds the main-catalog URL a Browse-page row links to. The
// "type" facet is a single-select chip param (matches the sidebar's type
// chips); the other three facets are field:value tokens in `q` (matches how
// label/scope chips already link elsewhere, e.g. CatalogTable).
export function browseRowHref(facet: BrowseFacet, value: string): string {
  if (facet === "type") {
    return `/?type=${encodeURIComponent(value)}`;
  }
  // "owner" maps to the catalog's `creator:` field token.
  if (facet === "owner") {
    return `/?q=${encodeURIComponent(`creator:${value}`)}`;
  }
  return `/?q=${encodeURIComponent(`${facet}:${value}`)}`;
}

// matchScore ranks how well `value` matches the Browse page's instant-search
// `query` (a plain substring match): a prefix match always outranks a
// non-prefix substring match, and within a tier a shorter value ranks
// higher — the query makes up a larger fraction of it, i.e. closer to an
// exact match. Case-insensitive. Returns null when `value` doesn't contain
// `query` at all.
export function matchScore(value: string, query: string): number | null {
  const v = value.toLowerCase();
  const q = query.toLowerCase();
  if (q === "") return 0;
  const idx = v.indexOf(q);
  if (idx === -1) return null;
  const isPrefix = idx === 0;
  return (isPrefix ? 1_000_000 : 0) - v.length;
}

// filterAndRankValues is the Browse page's instant search: drop values that
// don't contain `query`, then sort the rest by matchScore (best match
// first). An empty query is a no-op — the caller's existing count/name sort
// order stands. Array.prototype.sort is stable, so equally-scored values
// keep their incoming (server-sorted) relative order.
export function filterAndRankValues(
  values: BrowseValueCount[],
  query: string,
): BrowseValueCount[] {
  const trimmed = query.trim();
  if (trimmed === "") return values;
  return values
    .map((v) => ({ v, score: matchScore(v.value, trimmed) }))
    .filter((r): r is { v: BrowseValueCount; score: number } => r.score !== null)
    .sort((a, b) => b.score - a.score)
    .map((r) => r.v);
}
