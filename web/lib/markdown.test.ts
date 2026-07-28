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

  // A single "~" means "approximately" in prose and must NOT trigger GFM
  // strikethrough — marked's stock del tokenizer would pair the two lone tildes
  // and wrap everything between them in <del>.
  it("does not treat a single tilde as strikethrough", () => {
    const { html } = renderMarkdown("re-skinning ~12 components (~+1 wk).\n");
    expect(html).not.toContain("<del>");
    expect(html).toContain("~12 components (~+1 wk)");
  });

  it("leaves lone approximation tildes literal across a line", () => {
    const { html } = renderMarkdown("Unit economics: ~1000× cheaper (~$5).\n");
    expect(html).not.toContain("<del>");
    expect(html).toContain("~1000×");
    expect(html).toContain("(~$5)");
  });

  // Double-tilde strikethrough must still render as <del>.
  it("still renders double-tilde strikethrough", () => {
    const { html } = renderMarkdown("this is ~~struck~~ out\n");
    expect(html).toContain("<del>struck</del>");
  });

  // Mixed line: real strike renders, approximation stays literal.
  it("renders ~~strike~~ while leaving ~approx~ literal on the same line", () => {
    const { html } = renderMarkdown("mix ~~real~~ and ~approx~ here\n");
    expect(html).toContain("<del>real</del>");
    expect(html).toContain("~approx~");
  });
});
