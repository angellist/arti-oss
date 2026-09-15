import type { ArtifactInfo } from "./types";

export type AppSort = "views" | "recent" | "versions";

export const APP_SORTS: ReadonlyArray<{ value: AppSort; label: string }> = [
  { value: "recent", label: "recently updated" },
  { value: "views", label: "most views (30d)" },
  { value: "versions", label: "most versions" },
];

// What the portal's instant filter box matches against: everything visible on
// the card plus the owner, so "tian", "dashboard" and "couch-" all find the
// apps a person would expect from what they can see.
function haystack(a: ArtifactInfo): string {
  return [a.title, a.named_slug ?? "", a.creator, ...a.labels].join(" ").toLowerCase();
}

function views(a: ArtifactInfo): number {
  return a.view_count_30d ?? 0;
}

function versions(a: ArtifactInfo): number {
  return a.version ?? 0;
}

// selectApps applies the portal's filter box, the "owned by me" toggle and the
// sort, in that order. Sorting is over the whole loaded set, so it only means
// what the caller thinks it means while every app is on the page — see the
// note in app/apps/page.tsx.
//
// Most-recently-updated is both the default and every other sort's tiebreak:
// 30-day view counts are 0 for most apps, so ranking on them alone would leave
// the long tail in whatever order the server happened to return.
export function selectApps(
  rows: readonly ArtifactInfo[],
  opts: { query?: string; owner?: string | null; sort?: AppSort },
): ArtifactInfo[] {
  const q = (opts.query ?? "").trim().toLowerCase();
  const owner = opts.owner?.toLowerCase() ?? null;
  const sort = opts.sort ?? "recent";

  const kept = rows.filter((a) => {
    if (owner && a.creator.toLowerCase() !== owner) return false;
    if (q && !haystack(a).includes(q)) return false;
    return true;
  });

  const byRecent = (a: ArtifactInfo, b: ArtifactInfo) =>
    Date.parse(b.modified_at) - Date.parse(a.modified_at);

  return kept.sort((a, b) => {
    if (sort === "views" && views(a) !== views(b)) return views(b) - views(a);
    if (sort === "versions" && versions(a) !== versions(b)) return versions(b) - versions(a);
    return byRecent(a, b);
  });
}
