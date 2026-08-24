// @vitest-environment jsdom
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

  // Emphasis delimiters can be the ONLY separator between two words (an email
  // subject quoted verbatim). The parser eats them, so the renderer has to put
  // the gap back — otherwise "PAST DUE" and "Document" render fused.
  it("adds an optical gap where emphasis abuts a word", () => {
    const { html } = renderMarkdown("Re: ***PAST DUE***Document Request\n");
    expect(html).toContain('</em><span class="arti-emph-gap"></span>Document');
  });

  it("adds the gap on the opening side too", () => {
    const { html } = renderMarkdown("Re:Subject**PAST DUE** here\n");
    expect(html).toContain('<span class="arti-emph-gap"></span><strong>');
  });

  // Same delimiter-eating shape for strikethrough; inline code is excluded on
  // purpose (its chip padding already separates it).
  it("covers strikethrough but not inline code", () => {
    expect(renderMarkdown("~~struck~~text\n").html).toContain(
      '</del><span class="arti-emph-gap"></span>text',
    );
    expect(renderMarkdown("`code`text\n").html).not.toContain("arti-emph-gap");
  });

  // Scripts without inter-word spaces write emphasis flush against the next
  // character by design — a gap there invents a word break.
  it("leaves scriptio-continua scripts alone", () => {
    for (const src of ["**粗体**文字\n", "**太字**です\n", "**볼드**텍스트\n", "**หนา**ตัวอักษร\n"]) {
      expect(renderMarkdown(src).html).not.toContain("arti-emph-gap");
    }
    // …but a Latin word after CJK emphasis still separates.
    expect(renderMarkdown("**粗体**text\n").html).toContain("arti-emph-gap");
  });

  // The gap element carries no text, so the document still reads as one
  // continuous string for copy/paste and find-in-page.
  it("keeps the gap text-free", () => {
    const { html } = renderMarkdown("Re: ***PAST DUE***Document\n");
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.body.textContent).toContain("PAST DUEDocument");
  });

  // Punctuation and real spaces already separate the words — no marker there,
  // or every `*word*.` in every doc grows a gap before the period.
  it("leaves emphasis followed by punctuation or space alone", () => {
    for (const src of ["a *word*, b\n", "a *word*. b\n", "a *word* b\n", "(*word*)\n"]) {
      expect(renderMarkdown(src).html).not.toContain("arti-emph-gap");
    }
  });

  it("renders Mermaid fences as sanitized placeholders", () => {
    const { html } = renderMarkdown('```mermaid\nflowchart TD\n  A["<script>"] --> B\n```\n');
    const doc = new DOMParser().parseFromString(html, "text/html");
    const placeholder = doc.querySelector(".mermaid[data-arti-zoom][data-mermaid-placeholder]");
    expect(placeholder).not.toBeNull();
    expect(placeholder?.querySelector("pre")?.textContent).toContain('<script>');
    expect(html).not.toContain("<script>");
  });

  it("accepts Mermaid info-string parameters", () => {
    const { html } = renderMarkdown("```mermaid theme=neutral\nflowchart TD\n A --> B\n```\n");
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.querySelector(".mermaid[data-mermaid-placeholder]")).not.toBeNull();
  });

  it("does not replace non-Mermaid fences or inline code", () => {
    const { html } = renderMarkdown("`mermaid`\n\n```javascript\nconst x = 1;\n```\n");
    expect(html).toContain("<code>mermaid</code>");
    expect(html).toContain("language-javascript");
    expect(html).not.toContain("data-mermaid-placeholder");
  });

  it("handles indented fences nested in lists", () => {
    const { html } = renderMarkdown("- item\n\n  ```mermaid\n  flowchart TD\n    A --> B\n  ```\n");
    const doc = new DOMParser().parseFromString(html, "text/html");
    expect(doc.querySelector(".mermaid[data-mermaid-placeholder]")).not.toBeNull();
  });
});
