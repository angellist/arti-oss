import { describe, expect, it } from "vitest";

import {
  COLUMNS,
  DEFAULT_ORDER,
  MAX_COLUMN_WIDTH,
  clampWidth,
  columnCookieFrom,
  defaultColumnPrefs,
  isDefaultPrefs,
  moveColumn,
  normalizeColumnPrefs,
  parseColumnPrefs,
  serializeColumnPrefs,
  setWidth,
  toggleColumn,
  visibleColumns,
  widthOf,
  type ColumnKey,
  type ColumnPrefs,
} from "./columns";

const keys = (p: ColumnPrefs) => visibleColumns(p).map((c) => c.key);

describe("defaults", () => {
  it("shows exactly the columns marked defaultVisible, in registry order", () => {
    expect(keys(defaultColumnPrefs())).toEqual(
      COLUMNS.filter((c) => c.defaultVisible).map((c) => c.key),
    );
  });

  it("keeps every registry column reachable through the menu", () => {
    // The menu lists COLUMNS; prefs.order must cover all of them or a column
    // could be toggled on and still never render.
    expect([...defaultColumnPrefs().order].sort()).toEqual([...DEFAULT_ORDER].sort());
  });
});

describe("round trip through the cookie", () => {
  it("restores order, visibility and widths", () => {
    let p = defaultColumnPrefs();
    p = toggleColumn(p, "comments");
    p = moveColumn(p, "comments", "slug");
    p = setWidth(p, "creator", 200);
    const restored = parseColumnPrefs(serializeColumnPrefs(p));
    expect(restored.order).toEqual(p.order);
    expect([...restored.hidden].sort()).toEqual([...p.hidden].sort());
    expect(restored.widths).toEqual({ creator: 200 });
    expect(keys(restored)).toContain("comments");
    expect(keys(restored).indexOf("comments")).toBe(keys(restored).indexOf("slug") - 1);
  });

  it("survives a percent-encoded value read straight off document.cookie", () => {
    const p = setWidth(defaultColumnPrefs(), "title", 300);
    const raw = serializeColumnPrefs(p);
    expect(raw).toContain("%"); // JSON braces are encoded
    expect(parseColumnPrefs(raw).widths.title).toBe(300);
  });

  it("falls back to defaults on a corrupt cookie instead of throwing", () => {
    expect(isDefaultPrefs(parseColumnPrefs("not json"))).toBe(true);
    expect(isDefaultPrefs(parseColumnPrefs(""))).toBe(true);
    expect(isDefaultPrefs(parseColumnPrefs(undefined))).toBe(true);
    expect(isDefaultPrefs(parseColumnPrefs("%7B%22order%22%3A5%7D"))).toBe(true);
  });
});

// The forward-compatibility contract: a cookie written by an older build is a
// year-long liability. It must never be able to hide a column that shipped
// after it was written, or leave the table empty.
describe("stale cookies from older builds", () => {
  const oldBuild = (order: ColumnKey[], hidden: ColumnKey[] = []) =>
    normalizeColumnPrefs({ order, hidden });

  it("adds columns the cookie predates, at their default position and visibility", () => {
    // Pretend this cookie was written before `comments` and `id` existed.
    const known = DEFAULT_ORDER.filter((k) => k !== "comments" && k !== "id");
    const p = oldBuild(known, ["description"]);
    expect(p.order).toEqual(DEFAULT_ORDER); // both spliced back in place
    // comments/id default to hidden, so they stay hidden — not force-shown.
    expect(p.hidden).toContain("comments");
    expect(p.hidden).toContain("id");
    expect(p.hidden).toContain("description");
  });

  it("does not re-hide an opt-in column the user turned on", () => {
    // Regression guard: `hidden` alone can't distinguish "user unhid this"
    // from "this column is new", which is why the full order is always stored.
    const p = normalizeColumnPrefs(
      JSON.parse(decodeURIComponent(serializeColumnPrefs(toggleColumn(defaultColumnPrefs(), "size")))),
    );
    expect(p.hidden).not.toContain("size");
    expect(keys(p)).toContain("size");
  });

  it("drops columns that no longer exist", () => {
    const p = normalizeColumnPrefs({
      order: ["title", "sha_256_shortened", "slug"],
      hidden: ["a_removed_column"],
    });
    expect(p.order).not.toContain("sha_256_shortened" as ColumnKey);
    expect(p.hidden).not.toContain("a_removed_column" as ColumnKey);
    expect(p.order).toEqual(expect.arrayContaining(DEFAULT_ORDER));
  });

  it("never renders an empty table", () => {
    const p = normalizeColumnPrefs({ order: DEFAULT_ORDER, hidden: DEFAULT_ORDER });
    expect(keys(p).length).toBeGreaterThan(0);
    expect(keys(p)).toContain("title");
  });
});

describe("toggling", () => {
  it("refuses to hide the last visible column", () => {
    let p: ColumnPrefs = { order: [...DEFAULT_ORDER], hidden: [], widths: {} };
    for (const k of DEFAULT_ORDER) p = toggleColumn(p, k);
    expect(keys(p).length).toBe(1);
  });

  it("is its own inverse", () => {
    const p = defaultColumnPrefs();
    expect(keys(toggleColumn(toggleColumn(p, "size"), "size"))).toEqual(keys(p));
  });
});

describe("moveColumn", () => {
  it("moves a column before the target", () => {
    const p = moveColumn(defaultColumnPrefs(), "created", "title");
    expect(p.order[0]).toBe("created");
    expect(p.order[1]).toBe("title");
  });

  it("appends when the target is null", () => {
    const p = moveColumn(defaultColumnPrefs(), "title", null);
    expect(p.order[p.order.length - 1]).toBe("title");
  });

  it("keeps every column exactly once", () => {
    const p = moveColumn(moveColumn(defaultColumnPrefs(), "type", "slug"), "slug", null);
    expect([...p.order].sort()).toEqual([...DEFAULT_ORDER].sort());
  });

  it("is a no-op when dropped on itself", () => {
    const p = defaultColumnPrefs();
    expect(moveColumn(p, "slug", "slug")).toBe(p);
  });
});

describe("widths", () => {
  it("clamps to the column's minimum and the global maximum", () => {
    expect(clampWidth("version", 1)).toBe(48);
    expect(clampWidth("title", 99999)).toBe(MAX_COLUMN_WIDTH);
    expect(clampWidth("title", 421.6)).toBe(422);
  });

  it("falls back to the registry default when unset", () => {
    const p = defaultColumnPrefs();
    expect(widthOf(p, "slug")).toBe(150);
    expect(widthOf(setWidth(p, "slug", 90), "slug")).toBe(90);
  });

  it("clamps stored widths on read, so a hand-edited cookie can't wedge a column", () => {
    const p = normalizeColumnPrefs({ widths: { version: 2, title: 50_000, slug: "nope" } });
    expect(p.widths.version).toBe(48);
    expect(p.widths.title).toBe(MAX_COLUMN_WIDTH);
    expect(p.widths.slug).toBeUndefined();
  });
});

describe("columnCookieFrom", () => {
  it("finds the cookie among others and ignores lookalikes", () => {
    expect(columnCookieFrom("a=1; arti_cols=%7B%7D; b=2")).toBe("%7B%7D");
    expect(columnCookieFrom("x_arti_cols=nope; arti_colsx=nope")).toBeNull();
    expect(columnCookieFrom("")).toBeNull();
    expect(columnCookieFrom(null)).toBeNull();
  });
});
