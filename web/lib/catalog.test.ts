import { describe, it, expect } from "vitest";
import { catalogView, rowSetKey, ALL_VERSIONS_KEY, SHOW_ARCHIVED_KEY, SEARCH_OPEN_KEY } from "./catalog";

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

describe("rowSetKey", () => {
  const of = (qs: string) => {
    const p = new URLSearchParams(qs);
    return rowSetKey((k) => p.get(k));
  };

  it("is stable for params that don't change the rows", () => {
    // `find` only reveals the search bar; an unrelated param is not ours.
    expect(of("find=1")).toBe(of(""));
    expect(of("utm=x")).toBe(of(""));
  });

  it("changes for every filter, sort and page param", () => {
    const base = of("");
    for (const qs of [
      "q=hello",
      "slug=weekly-report",
      "type=TEXT",
      "order_by=title",
      "order_dir=asc",
      "page=2",
      `${ALL_VERSIONS_KEY}=1`,
      `${SHOW_ARCHIVED_KEY}=1`,
    ]) {
      expect(of(qs), `${qs} must be treated as a new row set`).not.toBe(base);
    }
  });

  it("does not collide across params", () => {
    expect(of("q=a")).not.toBe(of("type=a"));
  });
});
