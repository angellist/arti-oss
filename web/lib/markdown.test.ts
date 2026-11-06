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

describe("renderMarkdown diff blocks", () => {
  const src = "```diff\n--- a/a.md\n+++ b/a.md\n@@ -1,2 +1,2 @@ intro\n keep <x>\n-old\n+new\n--- removed dashes\n```\n";

  it("renders a header with file and counts, and one row per line", () => {
    const { html } = renderMarkdown(src);
    expect(html).toContain('<span class="arti-diff-file">a.md</span>');
    expect(html).toContain(">+1</span>");
    expect(html).toContain(">−2</span>");
    expect(html).toContain('arti-diff-hunk"><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">intro</span>');
    expect(html).toContain('arti-diff-ctx" data-t="keep <x>"><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">keep &lt;x&gt;</span>');
    expect(html).toContain('arti-diff-del"><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">old</span>');
    expect(html).toContain('arti-diff-add"><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">new</span>');
  });

  it("treats an unpaired --- line as a removed line", () => {
    const { html } = renderMarkdown(src);
    expect(html).toContain('<span class="arti-diff-text">-- removed dashes</span>');
  });

  it("folds long unchanged runs into a details element", () => {
    const ctx = Array.from({ length: 50 }, (_, i) => ` line ${i}`).join("\n");
    const { html } = renderMarkdown("```diff\n+top\n" + ctx + "\n-bottom\n```\n");
    expect(html).toContain('<details class="arti-diff-fold"><summary>⋯ 44 unchanged lines</summary>');
  });

  it("labels a multi-file diff by count and marks each file's start", () => {
    const two = "```diff\ndiff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\ndiff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-p\n+q\n```\n";
    const { html } = renderMarkdown(two);
    expect(html).toContain('<span class="arti-diff-file">2 files</span>');
    expect(html.indexOf('arti-diff-file-row"><span class="arti-diff-gutter" aria-hidden="true"></span><span class="arti-diff-text">a.go</span>')).toBeGreaterThan(-1);
    const b = html.indexOf('<span class="arti-diff-text">b.go</span>');
    expect(b).toBeGreaterThan(html.indexOf('<span class="arti-diff-text">y</span>'));
  });

  it("leaves blank lines empty so copied code has no zero-width space", () => {
    const { html } = renderMarkdown("```diff\n def f():\n\n+    return 1\n```\n");
    expect(html).not.toContain("\u200b");
    expect(html).toContain('<span class="arti-diff-text"></span>');
  });

  it("groups each removed run with the added run after it, and adds the view toggle", () => {
    const { html } = renderMarkdown(src);
    expect(html).toMatch(/<div class="arti-diff-chg"><div class="arti-diff-row arti-diff-del">.*old.*<div class="arti-diff-row arti-diff-add">.*new.*<\/div><\/div><div class="arti-diff-chg"><div class="arti-diff-row arti-diff-del">/);
    expect(html).toContain('<button type="button" class="arti-diff-toggle" data-arti-diff-toggle=""');
  });

  it("leaves other languages alone", () => {
    const { html } = renderMarkdown("```go\n-x\n```\n");
    expect(html).not.toContain("arti-diff");
  });
});
