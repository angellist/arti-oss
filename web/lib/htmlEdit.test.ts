import { describe, it, expect } from "vitest";
import { ArtiError } from "./arti";
import { htmlEditErrorMessage, htmlEditSaveInput, isHtmlTextEditable, parseFrameEdits, parseRejected } from "./htmlEdit";

const base = { artifact_type: "TEXT" as const, content_type: "text/html; charset=utf-8", named_slug: "deck", deleted_at: null };

describe("isHtmlTextEditable", () => {
  it("needs the server flag, TEXT html and a slug", () => {
    expect(isHtmlTextEditable(base, { html_edit_enabled: true })).toBe(true);
    expect(isHtmlTextEditable(base, { html_edit_enabled: false })).toBe(false);
    expect(isHtmlTextEditable(base, undefined)).toBe(false);
    expect(isHtmlTextEditable({ ...base, content_type: "text/markdown" }, { html_edit_enabled: true })).toBe(false);
    expect(isHtmlTextEditable({ ...base, artifact_type: "PACKAGE" }, { html_edit_enabled: true })).toBe(false);
    expect(isHtmlTextEditable({ ...base, named_slug: "" }, { html_edit_enabled: true })).toBe(false);
    expect(isHtmlTextEditable({ ...base, deleted_at: "2026-01-01T00:00:00Z" }, { html_edit_enabled: true })).toBe(false);
  });
});

describe("parseFrameEdits", () => {
  it("accepts a well-formed list", () => {
    expect(parseFrameEdits([{ eid: 3, before: "a", html: "b" }])).toEqual([{ eid: 3, before: "a", html: "b" }]);
    expect(parseFrameEdits([])).toEqual([]);
  });
  it("rejects the whole list when any entry is off-shape", () => {
    expect(parseFrameEdits(null)).toBeNull();
    expect(parseFrameEdits("x")).toBeNull();
    expect(parseFrameEdits([{ eid: 1, before: "a", html: "b" }, { eid: "2", before: "a", html: "b" }])).toBeNull();
    expect(parseFrameEdits([{ eid: -1, before: "a", html: "b" }])).toBeNull();
    expect(parseFrameEdits([{ eid: 1.5, before: "a", html: "b" }])).toBeNull();
    expect(parseFrameEdits([{ eid: 1, before: "a" }])).toBeNull();
    expect(parseFrameEdits([{ eid: 1, before: "a", html: "x".repeat(200_001) }])).toBeNull();
  });
});

describe("parseRejected", () => {
  it("reads the header and ignores junk", () => {
    expect(parseRejected(null)).toEqual([]);
    expect(parseRejected("")).toEqual([]);
    expect(parseRejected("3, 7,x,-1")).toEqual([3, 7]);
  });
});

describe("htmlEditSaveInput", () => {
  it("pins the edited version and keeps title, type and description", () => {
    const bytes = new TextEncoder().encode("<p>hi</p>").buffer as ArrayBuffer;
    const input = htmlEditSaveInput(
      { title: "Deck", content_type: "text/html", named_slug: "deck", version: 17, description: "d" },
      bytes,
    );
    expect(input).toMatchObject({
      title: "Deck",
      content_type: "text/html",
      artifact_type: "TEXT",
      named_slug: "deck",
      description: "d",
      expected_latest_version: 17,
    });
    expect(atob(input.content_base64 ?? "")).toBe("<p>hi</p>");
    expect("labels" in input).toBe(false);
  });
});

describe("htmlEditErrorMessage", () => {
  it("explains a concurrent publish", () => {
    expect(htmlEditErrorMessage(new ArtiError(409, "stale", "stale-base-version"))).toMatch(/newer version/);
    expect(htmlEditErrorMessage(new Error("boom"))).toBe("boom");
  });
});
