// Single source of truth for translating the catalog's URL params into the
// view state that both the server query (app/page.tsx) and the table
// rendering (components/CatalogTable.tsx) key off. Both read the SAME keys
// through this helper so their interpretation can never drift — divergent
// per-consumer parsing of one query string is a class of bug this catalog
// has hit before.

// URL keys for the catalog control-bar toggles. Kept short, and omitted from
// the URL when at their default (latest-only on, archived off) so a plain
// catalog link stays clean.
export const ALL_VERSIONS_KEY = "allv";
export const SHOW_ARCHIVED_KEY = "arch";

// URL key for the reveal-on-demand search bar. UI-only (see `searchOpen`): it
// governs whether the search/toggle bar is shown at the top of the listing,
// not what the server query returns, so app/page.tsx never reads it. Clicking
// SEARCH in the rail sets `find=1`; it's omitted at the default so a plain
// catalog link stays clean.
export const SEARCH_OPEN_KEY = "find";

export interface CatalogView {
  // A single slug is in focus via the dedicated `slug` URL param — a
  // deterministic drill-in (e.g. clicking a slug chip). The catalog then shows
  // that slug's full version history with inline archive controls, so the two
  // control-bar toggles are hidden there. A `slug:` token typed in the search
  // box is a *search*, not a drill-in: it keeps the toggles and collapses to
  // latest-per-slug by default. Mirrors latestPerSlugFor server-side.
  drilledIntoSlug: boolean;
  // "Latest version only" toggle is OFF: show every matching version per slug
  // instead of collapsing to the latest. URL: `allv=1`.
  allVersions: boolean;
  // "Show archived" toggle is ON: include archived versions in the candidate
  // set (which also widens what "latest version only" collapses over). URL:
  // `arch=1`.
  showArchived: boolean;
  // The search bar was explicitly opened via the rail's SEARCH item (URL:
  // `find=1`). UI-only — it reveals an empty bar. The bar also shows whenever a
  // query or a toggle is already active, so this only matters for the
  // nothing-active case. app/page.tsx ignores it (no effect on the row set).
  searchOpen: boolean;
}

// catalogView reads the view state via a param getter, so it works with both
// Next's ReadonlyURLSearchParams (client) and the server component's plain
// searchParams record.
export function catalogView(
  get: (key: string) => string | null | undefined,
): CatalogView {
  return {
    drilledIntoSlug: !!get("slug"),
    allVersions: get(ALL_VERSIONS_KEY) === "1",
    showArchived: get(SHOW_ARCHIVED_KEY) === "1",
    searchOpen: get(SEARCH_OPEN_KEY) === "1",
  };
}

// Every URL param that changes WHICH rows the catalog shows — the filters and
// sort that app/page.tsx passes to the API, plus the page number. `find` is
// deliberately absent: it only reveals the search bar and leaves the row set
// alone.
//
// The rows scroll inside their own box, so a navigation that swaps them out
// has to rewind that box by hand (the browser only restores document scroll).
// Deciding "did the rows change?" from a hand-listed set of params is exactly
// where that drifts — a toggle gets added to the URL and nobody remembers the
// scroll effect — so the list lives here, next to the params themselves.
export const ROW_SET_KEYS = [
  "q",
  "slug",
  "type",
  "order_by",
  "order_dir",
  "page",
  ALL_VERSIONS_KEY,
  SHOW_ARCHIVED_KEY,
] as const;

/** A value that changes exactly when the catalog's row set does. */
export function rowSetKey(get: (key: string) => string | null | undefined): string {
  return ROW_SET_KEYS.map((k) => `${k}=${get(k) ?? ""}`).join("&");
}
