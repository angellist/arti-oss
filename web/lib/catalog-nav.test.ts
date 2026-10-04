import { describe, expect, it } from "vitest";
import { catalogURL } from "./catalog-nav";

describe("catalogURL", () => {
  it("keeps the catalog's own params and drops paging and a slug drill-in", () => {
    const sp = new URLSearchParams("q=memo&order_by=title&page=3&slug=abc");
    expect(catalogURL(sp, { q: "ignored", type: "TEXT" }, { find: "1" })).toBe("/?q=memo&order_by=title&find=1");
  });

  it("rebuilds the sticky filter off the catalog", () => {
    expect(catalogURL(null, { q: "label:x", type: "PACKAGE" }, { find: "1" })).toBe("/?q=label%3Ax&type=PACKAGE&find=1");
  });

  it("deletes a param set to null or empty", () => {
    expect(catalogURL(new URLSearchParams("type=TEXT&q=a"), { q: "", type: "" }, { type: null, q: "" })).toBe("/?");
  });
});
