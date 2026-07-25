import { describe, it, expect } from "vitest";
import { browseRowHref, matchScore, filterAndRankValues } from "./browse";

// Clicking a Browse-page row navigates to the main catalog filtered to that
// value. "type" is a single-select chip param (matches the sidebar's type
// chips); the other three facets are field:value tokens in `q` (matches how
// label/scope chips already link elsewhere, e.g. CatalogTable).
describe("browseRowHref", () => {
  it("type facet uses the ?type= chip param", () => {
    expect(browseRowHref("type", "TEXT")).toBe("/?type=TEXT");
  });
  it("label facet uses a label: token in q", () => {
    expect(browseRowHref("label", "quarterly-report")).toBe(
      `/?q=${encodeURIComponent("label:quarterly-report")}`,
    );
  });
  it("scope facet uses a scope: token in q, preserving internal colons", () => {
    expect(browseRowHref("scope", "topic:funds")).toBe(
      `/?q=${encodeURIComponent("scope:topic:funds")}`,
    );
  });
  it("content_type facet uses a content_type: token in q", () => {
    expect(browseRowHref("content_type", "text/markdown")).toBe(
      `/?q=${encodeURIComponent("content_type:text/markdown")}`,
    );
  });
  it("owner facet maps to a creator: token in q", () => {
    expect(browseRowHref("owner", "alice@example.com")).toBe(
      `/?q=${encodeURIComponent("creator:alice@example.com")}`,
    );
  });
});

// matchScore powers the Browse page's instant search: substring match,
// prefix matches ranked above plain substring matches, and — within a tier —
// shorter values rank higher since the query makes up a larger fraction of
// the string (closer to an exact match).
describe("matchScore", () => {
  it("returns null when the value doesn't contain the query", () => {
    expect(matchScore("text/markdown", "zzz")).toBeNull();
  });
  it("is case-insensitive", () => {
    expect(matchScore("Text/Markdown", "MARK")).not.toBeNull();
  });
  it("ranks a prefix match above a non-prefix substring match", () => {
    const prefix = matchScore("markdown", "mark");
    const substring = matchScore("bookmark", "mark");
    expect(prefix).not.toBeNull();
    expect(substring).not.toBeNull();
    expect(prefix!).toBeGreaterThan(substring!);
  });
  it("within the same match tier, a shorter value ranks higher", () => {
    const shortValue = matchScore("app", "app");
    const longValue = matchScore("application/octet-stream", "app");
    expect(shortValue!).toBeGreaterThan(longValue!);
  });
});

describe("filterAndRankValues", () => {
  const values = [
    { value: "application/octet-stream", count: 2 },
    { value: "application/zip", count: 30 },
    { value: "text/markdown", count: 32 },
    { value: "text/plain", count: 21 },
  ];

  it("returns every value unchanged when the query is empty", () => {
    expect(filterAndRankValues(values, "")).toEqual(values);
  });

  it("drops non-matching values", () => {
    const got = filterAndRankValues(values, "zzz");
    expect(got).toEqual([]);
  });

  it("ranks prefix matches before substring matches regardless of count", () => {
    // "application/zip" is a prefix match for "app"; "application/octet-stream"
    // also is, and is shorter — order should be zip-length-order among
    // prefix matches, both ahead of anything that isn't a prefix match here.
    const got = filterAndRankValues(values, "app");
    expect(got.map((v) => v.value)).toEqual([
      "application/zip",
      "application/octet-stream",
    ]);
  });

  it("a shorter prefix match ranks above a longer one even with a lower count", () => {
    const got = filterAndRankValues(
      [
        { value: "text", count: 1 },
        { value: "text/markdown", count: 100 },
      ],
      "text",
    );
    expect(got.map((v) => v.value)).toEqual(["text", "text/markdown"]);
  });

  it("ignores leading/trailing whitespace in the query when matching", () => {
    // The empty-query guard trims, so a space-padded query must match the
    // same way the trimmed text would — otherwise a stray space silently
    // returns zero matches while the UI's status bar (which trims for
    // display) claims to have searched the clean term.
    const got = filterAndRankValues(values, "  app  ");
    expect(got.map((v) => v.value)).toEqual([
      "application/zip",
      "application/octet-stream",
    ]);
  });
});
