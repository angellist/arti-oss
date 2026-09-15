import type { ArtifactInfo } from "./types";

// Where a row's title links: the slug view (version-pinned when the row knows
// its version) for a named artifact, the uuid view otherwise. One helper so
// every listing — the catalog table, its mobile card, the apps portal — can
// never point at different versions of the same row.
export function artifactHref(a: ArtifactInfo): string {
  if (!a.named_slug) return `/a/${a.artifact_id}`;
  return a.version != null ? `/s/${a.named_slug}/${a.version}` : `/s/${a.named_slug}`;
}

// The running-app URL for an APP row, null for every other type. /app/{ident}
// is served by the Go edge, not a Next route, so callers link it with a plain
// <a> (a full navigation), never next/link.
export function appHref(a: ArtifactInfo): string | null {
  if (a.artifact_type !== "APP") return null;
  if (!a.named_slug) return `/app/${a.artifact_id}`;
  return a.version != null ? `/app/${a.named_slug}/${a.version}` : `/app/${a.named_slug}`;
}
