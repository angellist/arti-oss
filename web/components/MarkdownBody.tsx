"use client";

import { memo, useEffect, useMemo, useRef } from "react";

import { Frontmatter, renderMarkdown } from "@/lib/markdown";
import { renderMermaidIn } from "@/lib/mermaid";

// MarkdownBody is THE rendered form of a markdown document in arti. Extracted
// from ArtifactViewer so the markdown editor's live preview renders through the
// exact same component: a preview built on a second renderer (or a copy of the
// prose classes below) drifts from the real view, and then "what you saw" isn't
// what got published.
//
// memo'd: the viewer's width (Wide/Medium/Narrow) toggle re-renders the parent
// but doesn't change `body`. Without memo, MarkdownBody re-renders and React
// re-applies the dangerouslySetInnerHTML, which wipes the comment overlay's
// imperatively-injected <mark> highlights (they'd flash away and get re-seeded).
// Skipping the re-render when body is unchanged keeps the highlights stable.
const MarkdownBody = memo(function MarkdownBody({
  body,
  debounceMermaid = false,
}: {
  body: string;
  debounceMermaid?: boolean;
}) {
  // renderMarkdown is the shared "render markdown safely" chain
  // (splitFrontmatter → marked → heading ids → DOMPurify) — the same one
  // FullPageView and the /help viewer use, so sanitization and heading anchors
  // stay consistent across every markdown surface. Without the DOMPurify step a
  // `.md` artifact with a hidden <script> would run in arti's origin with the
  // viewer's session cookie.
  const { frontmatter, html } = useMemo(() => renderMarkdown(body), [body]);
  const ref = useRef<HTMLElement>(null);
  useEffect(() => {
    const container = ref.current;
    if (!container) return;
    const render = () => {
      void renderMermaidIn(container).catch(() => {});
    };
    if (!debounceMermaid) {
      render();
      return;
    }
    const timer = window.setTimeout(render, 200);
    return () => window.clearTimeout(timer);
  }, [debounceMermaid, html]);
  return (
    <article
      ref={ref}
      className="
        prose prose-sm prose-neutral max-w-none break-words
        leading-[1.45]
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
      "
    >
      {frontmatter !== null ? <Frontmatter raw={frontmatter} /> : null}
      <div dangerouslySetInnerHTML={{ __html: html }} />
    </article>
  );
});

export default MarkdownBody;
