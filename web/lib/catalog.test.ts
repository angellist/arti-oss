import { describe, it, expect } from "vitest";
import { catalogView, ALL_VERSIONS_KEY, SHOW_ARCHIVED_KEY, SEARCH_OPEN_KEY } from "./catalog";

const view = (params: Record<string, string>) => catalogView((k) => params[k]);

describe("catalogView", () => {
  it("defaults: latest-only on, archived off, not drilled in, search closed", () => {
    expect(view({})).toEqual({
      drilledIntoSlug: false,
      allVersions: false,
      showArchived: false,
      searchOpen: false,
    });
  });

  it("allv=1 turns off latest-only (show all versions)", () => {
    expect(view({ [ALL_VERSIONS_KEY]: "1" }).allVersions).toBe(true);
  });

  it("arch=1 turns on show-archived", () => {
    expect(view({ [SHOW_ARCHIVED_KEY]: "1" }).showArchived).toBe(true);
  });

  it("only the exact value '1' enables a toggle", () => {
    expect(view({ [ALL_VERSIONS_KEY]: "true" }).allVersions).toBe(false);
  });

  it("find=1 marks the search bar as explicitly opened", () => {
    expect(view({ [SEARCH_OPEN_KEY]: "1" }).searchOpen).toBe(true);
    expect(view({}).searchOpen).toBe(false);
  });

  it("a slug: token in q is a search, not a drill-in (toggles stay visible)", () => {
    // Typing slug:foo (or a glob slug:foo*) in the search box is a search:
    // it keeps the control-bar toggles and collapses to latest-per-slug by
    // default. Only the dedicated `slug` param is a deterministic drill-in.
    expect(view({ q: "slug:my-report foo" }).drilledIntoSlug).toBe(false);
    expect(view({ q: "slug:pr-review-knowledge*" }).drilledIntoSlug).toBe(false);
  });

  it("the dedicated slug param counts as drilled in", () => {
    expect(view({ slug: "my-report" }).drilledIntoSlug).toBe(true);
  });

  it("free text merely containing 'slug' is not a drill-in", () => {
    expect(view({ q: "a slugger" }).drilledIntoSlug).toBe(false);
  });
});
