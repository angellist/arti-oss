import { describe, it, expect } from "vitest";
import { slugifyHeading, renderMarkdown } from "./markdown";

describe("slugifyHeading", () => {
  it("lowercases and hyphenates", () => {
    expect(slugifyHeading("Slugs and versions")).toBe("slugs-and-versions");
  });
  it("strips inline tags and punctuation", () => {
    expect(slugifyHeading("Using <code>arti add</code>!")).toBe("using-arti-add");
  });
});

describe("renderMarkdown", () => {
  it("adds GitHub-style ids to headings", () => {
    const { html } = renderMarkdown("# Getting started\n\n## Share and fetch\n");
    expect(html).toContain('id="getting-started"');
    expect(html).toContain('id="share-and-fetch"');
  });
  it("honors explicit {#id} and strips it from the heading text", () => {
    const { html } = renderMarkdown("## Slug {#slug}\n");
    expect(html).toContain('id="slug"');
    expect(html).toContain(">Slug</h2>");
    expect(html).not.toContain("{#slug}");
  });
  it("de-duplicates repeated heading slugs", () => {
    const { html } = renderMarkdown("## Notes\n\ntext\n\n## Notes\n");
    expect(html).toContain('id="notes"');
    expect(html).toContain('id="notes-1"');
  });
  it("splits frontmatter off the body", () => {
    const { frontmatter, html } = renderMarkdown("---\ntitle: X\n---\n# Body\n");
    expect(frontmatter).toContain("title: X");
    expect(html).toContain("<h1");
    expect(html).not.toContain("title: X");
  });
});
