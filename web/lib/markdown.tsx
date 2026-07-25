// Shared helpers for rendering markdown artifacts.
//
// Skill files (and many other docs) lead with a YAML frontmatter block:
//
//     ---
//     name: html-report-style
//     description: ... write a <style> block ...
//     ---
//     # actual document
//
// Fed straight to `marked`, that header is a problem: it isn't real
// markdown, and a literal HTML token inside it (e.g. `<style>` in a
// description) is parsed as an open tag, swallowing the rest of the
// document into a phantom <style> element. So we split the frontmatter
// off, render the BODY as markdown unaffected, and show the header
// verbatim in its own distinctly-shaded block.

import type { ReactElement } from "react";
import { marked } from "marked";
import DOMPurify from "isomorphic-dompurify";

export type SplitMarkdown = { frontmatter: string | null; body: string };

// Detect a leading YAML frontmatter fence. Anchored at `^` so a mid-doc
// `---` (thematic break) is never mistaken for frontmatter. The closing
// `---` must sit on its own line; everything after it is the body.
export function splitFrontmatter(src: string): SplitMarkdown {
  const m = /^---[ \t]*\r?\n([\s\S]*?)\r?\n---[ \t]*(?:\r?\n|$)/.exec(src);
  if (!m) return { frontmatter: null, body: src };
  return { frontmatter: m[1], body: src.slice(m[0].length) };
}

// Renders a frontmatter header verbatim (React escapes the text child, so
// any `<style>`/`<tag>` shows literally and never parses) in a shade
// distinct from normal code blocks, marking it as metadata rather than body.
export function Frontmatter({ raw }: { raw: string }): ReactElement {
  return (
    <div className="not-prose mb-4">
      <div className="mb-1 text-[10px] font-medium uppercase tracking-wide text-amber-700/80">
        metadata
      </div>
      <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-md border border-amber-200 bg-amber-50/70 px-3 py-2 font-mono text-[11.5px] leading-relaxed text-amber-900">
        {raw}
      </pre>
    </div>
  );
}

// GitHub-style heading slug: lowercase, strip tags + punctuation, spaces→hyphens.
// Used so in-doc anchor links (e.g. `concepts.md#slugs`) resolve to a heading.
export function slugifyHeading(inner: string): string {
  return inner
    .replace(/<[^>]+>/g, "") // strip inline tags (e.g. <code>)
    .replace(/&[^;]+;/g, "") // drop HTML entities
    .toLowerCase()
    .trim()
    .replace(/[^\w\s-]/g, "")
    .replace(/\s+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "");
}

// Inject `id` attributes into <h1>-<h6> (marked emits none by default), with
// GitHub-style de-duplication (repeat slug → `-1`, `-2`, …). Supports an
// explicit `## Heading {#custom-id}` suffix (marked passes it through as text):
// the `{#id}` is stripped from the displayed heading and used as the anchor.
function addHeadingIds(html: string): string {
  const seen = new Map<string, number>();
  return html.replace(/<h([1-6])>([\s\S]*?)<\/h\1>/g, (_m, level: string, inner: string) => {
    let text = inner;
    let base: string;
    const explicit = inner.match(/\s*\{#([\w-]+)\}\s*$/);
    if (explicit) {
      base = explicit[1];
      text = inner.slice(0, explicit.index).trimEnd();
    } else {
      base = slugifyHeading(inner);
    }
    if (!base) return `<h${level}>${text}</h${level}>`;
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    const id = n === 0 ? base : `${base}-${n}`;
    return `<h${level} id="${id}">${text}</h${level}>`;
  });
}

// Markdown → sanitized HTML. Single source of truth used by FullPageView
// and the /help docs viewer. `marked` may emit raw <script>/event-handler
// attrs from the source; DOMPurify strips them. Frontmatter is split off
// first (see splitFrontmatter) so a literal <tag> in YAML can't swallow
// the body. Headings get GitHub-style ids so anchor links work.
export function renderMarkdown(src: string): { frontmatter: string | null; html: string } {
  const { frontmatter, body } = splitFrontmatter(src);
  const parsed = marked.parse(body, { gfm: true, breaks: false, async: false }) as string;
  const html = DOMPurify.sanitize(addHeadingIds(parsed));
  return { frontmatter, html };
}

// Shared prose styling for full-viewport markdown (FullPageView + /help).
// Kept in lockstep with MarkdownBody in ArtifactViewer (the "arti-260710"
// style, picked via the md-style-tuner artifact) — only the wrapper bits
// (centered column, page padding) differ. Font sizes for body text and
// pre blocks live in globals.css (unlayered .prose/pre rules), not here.
export const PROSE_CLASSNAME = `
  prose prose-sm prose-neutral mx-auto max-w-[800px] break-words
  px-4 py-8 leading-[1.45]
  sm:px-6 sm:py-10
  prose-headings:font-semibold prose-headings:tracking-tight
  prose-h1:text-[1.57em] prose-h2:text-[1.28em] prose-h3:text-[1.06em]
  prose-h1:mt-0 prose-h1:mb-[15px] prose-h2:mt-[23px] prose-h2:mb-[9px] prose-h3:mt-[12px] prose-h3:mb-[4px]
  prose-p:my-[10px] prose-li:my-[4.5px] prose-ul:my-[10px] prose-ol:my-[10px] prose-ul:pl-[22px] prose-ol:pl-[22px]
  prose-hr:my-[17px]
  prose-blockquote:my-[10px]
  prose-table:my-[22px] prose-table:text-[0.81em]
  prose-pre:my-[5px] prose-pre:px-[11px] prose-pre:py-[7px]
  prose-pre:bg-neutral-50 prose-pre:text-neutral-800
  prose-pre:border prose-pre:border-neutral-200 prose-pre:rounded-md
  prose-pre:shadow-none
  prose-code:text-[0.79em] prose-code:bg-neutral-100 prose-code:text-neutral-800
  prose-code:px-1 prose-code:py-px prose-code:rounded
  prose-code:font-medium
  prose-code:before:content-none prose-code:after:content-none
`;
