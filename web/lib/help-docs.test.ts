import { describe, it, expect } from "vitest";
import { resolve } from "node:path";
import { loadHelpManifest, loadHelpDoc } from "./help-docs";

const ROOT = resolve(__dirname, "__fixtures__/help");

describe("loadHelpManifest", () => {
  it("groups docs by section in SECTIONS order (guides before reference)", () => {
    const m = loadHelpManifest(ROOT);
    expect(m.map((s) => s.dir)).toEqual(["guides", "reference"]);
  });
  it("derives slug from path and title from frontmatter or first H1", () => {
    const guides = loadHelpManifest(ROOT).find((s) => s.dir === "guides")!;
    const bySlug = Object.fromEntries(guides.docs.map((d) => [d.slug, d]));
    expect(bySlug["guides/getting-started"].title).toBe("Getting started");
    expect(bySlug["guides/no-frontmatter"].title).toBe("Bare Title From H1");
  });
  it("orders docs within a section by frontmatter order then title", () => {
    const guides = loadHelpManifest(ROOT).find((s) => s.dir === "guides")!;
    expect(guides.docs[0].slug).toBe("guides/getting-started"); // order:1 first
  });
});

describe("loadHelpDoc", () => {
  it("returns the markdown for a known slug", () => {
    const r = loadHelpDoc("reference/concepts", ROOT);
    expect(r?.markdown).toContain("# Concepts");
  });
  it("returns null for an unknown slug", () => {
    expect(loadHelpDoc("nope/missing", ROOT)).toBeNull();
  });
});
