import { describe, expect, it } from "vitest";
import { selectApps } from "./apps";
import type { ArtifactInfo } from "./types";

function app(over: Partial<ArtifactInfo>): ArtifactInfo {
  return {
    artifact_id: over.named_slug ?? "id",
    artifact_type: "APP",
    named_slug: null,
    version: 1,
    title: "an app",
    content_type: "application/zip",
    size_bytes: 1,
    creator: "someone@example.com",
    created_at: "2026-09-01T00:00:00Z",
    modified_at: "2026-09-01T00:00:00Z",
    labels: [],
    scopes: [],
    ...over,
  } as ArtifactInfo;
}

describe("selectApps", () => {
  const rows = [
    app({ named_slug: "quiet", title: "Quiet", view_count_30d: 0, version: 90, modified_at: "2026-09-09T00:00:00Z" }),
    app({ named_slug: "popular", title: "Popular", view_count_30d: 56, version: 2, modified_at: "2026-09-01T00:00:00Z" }),
    app({ named_slug: "fresh", title: "Fresh", view_count_30d: 0, version: 1, modified_at: "2026-09-12T00:00:00Z" }),
  ];

  it("defaults to most-recently-updated, because that is what the portal opens on", () => {
    expect(selectApps(rows, {}).map((a) => a.named_slug)).toEqual(["fresh", "quiet", "popular"]);
  });

  it("ranks by views when asked", () => {
    expect(selectApps(rows, { sort: "views" }).map((a) => a.named_slug)).toEqual([
      "popular",
      "fresh",
      "quiet",
    ]);
  });

  // Most apps have 0 views in any 30-day window, so a views sort without a
  // tiebreak would hand back the server's arbitrary order for the long tail.
  it("breaks every sort's ties by most-recently-updated", () => {
    expect(selectApps(rows, { sort: "versions" }).map((a) => a.named_slug)).toEqual([
      "quiet",
      "popular",
      "fresh",
    ]);
    expect(selectApps(rows, { sort: "recent" }).map((a) => a.named_slug)).toEqual([
      "fresh",
      "quiet",
      "popular",
    ]);
  });

  it("matches the filter against everything the card shows, plus the owner", () => {
    const set = [
      app({ named_slug: "a", title: "Budget vs Actuals", creator: "gina@example.com" }),
      app({ named_slug: "couch-probe", title: "Probe", labels: ["dry-run"] }),
    ];
    expect(selectApps(set, { query: "gina" }).map((a) => a.named_slug)).toEqual(["a"]);
    expect(selectApps(set, { query: "COUCH" }).map((a) => a.named_slug)).toEqual(["couch-probe"]);
    expect(selectApps(set, { query: "dry-run" }).map((a) => a.named_slug)).toEqual(["couch-probe"]);
    expect(selectApps(set, { query: "actuals" }).map((a) => a.named_slug)).toEqual(["a"]);
  });

  // The toggle is an identity check, not a text match: "tian" as a query must
  // not stand in for owning the app.
  it("owned-by-me compares the whole address, case-insensitively", () => {
    const set = [
      app({ named_slug: "mine", creator: "Ada.Lovelace@example.com" }),
      app({ named_slug: "theirs", creator: "ada.lovelace@other.example" }),
    ];
    expect(selectApps(set, { owner: "ada.lovelace@example.com" }).map((a) => a.named_slug)).toEqual([
      "mine",
    ]);
  });

  it("does not mutate the caller's array", () => {
    const src = [...rows];
    selectApps(src, { sort: "recent" });
    expect(src.map((a) => a.named_slug)).toEqual(["quiet", "popular", "fresh"]);
  });
});

