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
import { Marked, type TokenizerThis, type Tokens } from "marked";
import DOMPurify from "isomorphic-dompurify";

// GFM strikethrough, strictly double-tilde ("~~struck~~").
//
// marked's stock `del` tokenizer (like goldmark's — see the Go twin in
// internal/artifacts/markdown_strikethrough.go) opens on a run of EITHER one or
// two tildes. That means ordinary prose using "~" for "approximately" — e.g.
// "re-skinning ~12 components (~+1 wk)" or "~1000× cheaper (~$5)" — has its two
// lone tildes paired and everything between them wrapped in <del>. We override
// `del` to open only on "~~", leaving a single "~" as literal text.
//
// The override returns `undefined` (NOT `false`) when there's no double-tilde
// match: marked's `use()` wrapper falls back to the permissive built-in del only
// on a strict `=== false`, so `undefined` correctly skips strikethrough and lets
// the "~" fall through to normal text. Double-tilde still renders identically.
const DOUBLE_TILDE_DEL = /^~~(?=[^\s~])((?:\\[\s\S]|[^\\])*?(?:\\[\s\S]|[^\s~\\]))~~(?=[^~]|$)/;

const md = new Marked({ gfm: true, breaks: false, async: false });
md.use({
  tokenizer: {
    del(this: TokenizerThis, src: string): Tokens.Del | undefined {
      const m = DOUBLE_TILDE_DEL.exec(src);
      if (!m) return undefined;
      const text = m[1];
      return { type: "del", raw: m[0], text, tokens: this.lexer.inlineTokens(text) };
    },
  },
  renderer: {
    code({ text, lang }: Tokens.Code): string | false {
      if (lang?.trim().split(/\s+/, 1)[0].toLowerCase() !== "mermaid") return false;
      const escaped = text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
      return `<div class="mermaid not-prose my-3 overflow-x-auto rounded-md border border-neutral-200 bg-neutral-50" data-arti-zoom data-mermaid-placeholder><pre class="m-0 whitespace-pre-wrap px-3 py-2 font-mono text-[12px] leading-relaxed text-neutral-700">${escaped}</pre></div>\n`;
    },
  },
});

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
//
// Collapsed by default: frontmatter is bookkeeping (id/owner/status), not the
// document, and an 8-line YAML block above the fold pushes the actual prose off
// screen. Built on native <details>/<summary> rather than useState so it works
// unchanged in server components (the /help viewer) and keeps the raw text in
// the DOM for in-page find/copy — only the disclosure triangle is ours.
//
// Open/closed lives in the DOM, so callers must remount this on a change of
// DOCUMENT (see MarkdownBody's `docKey`) — the app router keeps the viewer
// mounted across artifact→artifact and package-file→file navigation, and a
// reused <details> node would carry an expanded block onto the next doc.
// Keying on document identity rather than on `raw` is deliberate: `raw` changes
// on every keystroke in the editor's live preview, which would slam the block
// shut while you're editing the very lines it shows.
export function Frontmatter({ raw }: { raw: string }): ReactElement {
  return (
    <details className="group not-prose mb-4">
      <summary className="inline-flex cursor-pointer list-none items-center gap-1 text-[10px] font-medium uppercase tracking-wide text-amber-700/80 transition hover:text-amber-800 [&::-webkit-details-marker]:hidden">
        <svg
          width="8"
          height="8"
          viewBox="0 0 10 10"
          fill="currentColor"
          aria-hidden="true"
          className="shrink-0 transition-transform group-open:rotate-90"
        >
          <polygon points="2,1 8,5 2,9" />
        </svg>
        metadata
      </summary>
      <pre className="mt-1 overflow-x-auto whitespace-pre-wrap break-words rounded-md border border-amber-200 bg-amber-50/70 px-3 py-2 font-mono text-[11.5px] leading-relaxed text-amber-900">
        {raw}
      </pre>
    </details>
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

// Emphasis delimiters are the only separator between two words surprisingly
// often — an email subject carried into a doc verbatim ("Re: ***PAST
// DUE***Document Request") is the canonical case. The parser consumes the
// asterisks, so the render collides: "PAST DUEDocument". Nothing is wrong with
// the markdown, and the source is usually not ours to edit (it's someone
// else's subject line), so the renderer supplies the optical gap the
// delimiters used to provide: a zero-content marker element carrying a small
// inline padding (see .arti-emph-gap in globals.css, and the Go twin in
// internal/artifacts/embed_serve.go).
//
// Only a TIGHT boundary qualifies — emphasis directly against a letter or
// digit. `*word*.`, `(*word*)` and `*word* text` are untouched: punctuation
// needs no gap and a real space is already there.
//
// Covers the three delimiter-consuming inline marks (em/strong/del). Inline
// `code` is deliberately excluded: its chip padding and background already
// separate it from what follows.
//
// The marker carries no text, so nothing downstream shifts — the comments
// overlay anchors text threads by an offset into the concatenated text nodes
// and pins by top-level block index (see lib/commentsOverlay.ts), and an empty
// inline span changes neither. Selection, copy/paste and find-in-page likewise
// still see one continuous string.
// "Tight" only means something in a script that separates words with spaces.
// Chinese, Japanese, Korean and the Southeast Asian scripts below run words
// together by design, so `**粗体**文字` is ordinary continuous text — a gap
// there would insert a word break the author never wrote. The abutting rune
// decides, and these scripts opt out. (The Go twin makes the same call in
// code, since RE2 has no lookaround.)
const NO_SPACE_SCRIPTS = String.raw`\p{sc=Han}\p{sc=Hiragana}\p{sc=Katakana}\p{sc=Hangul}\p{sc=Thai}\p{sc=Lao}\p{sc=Khmer}\p{sc=Myanmar}\p{sc=Tibetan}`;
const EMPH_GAP = '<span class="arti-emph-gap"></span>';

const TIGHT_CLOSE = new RegExp(
  String.raw`</(em|strong|del)>(?=[\p{L}\p{N}])(?![${NO_SPACE_SCRIPTS}])`,
  "gu",
);
const TIGHT_OPEN = new RegExp(
  String.raw`(?<=[\p{L}\p{N}])(?<![${NO_SPACE_SCRIPTS}])<(em|strong|del)>`,
  "gu",
);

export function spaceTightEmphasis(html: string): string {
  return html
    .replace(TIGHT_CLOSE, `</$1>${EMPH_GAP}`)
    .replace(TIGHT_OPEN, `${EMPH_GAP}<$1>`);
}

// Markdown → sanitized HTML. Single source of truth used by FullPageView
// and the /help docs viewer. `marked` may emit raw <script>/event-handler
// attrs from the source; DOMPurify strips them. Frontmatter is split off
// first (see splitFrontmatter) so a literal <tag> in YAML can't swallow
// the body. Headings get GitHub-style ids so anchor links work.
export function renderMarkdown(src: string): { frontmatter: string | null; html: string } {
  const { frontmatter, body } = splitFrontmatter(src);
  const parsed = md.parse(body, { async: false }) as string;
  const html = DOMPurify.sanitize(spaceTightEmphasis(addHeadingIds(parsed)));
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
