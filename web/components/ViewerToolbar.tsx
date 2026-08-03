"use client";

import { ReactNode, useEffect, useState } from "react";

export type Width = "wide" | "medium" | "narrow";
export type TextSize = "sm" | "md" | "lg";

const WIDTH_KEY = "arti.viewerWidth";
const TEXTSIZE_KEY = "arti.viewerTextSize";

// Width and text size both persist in localStorage so they stick across
// navigations. Raw Source does NOT: it's a per-view toggle that starts off on
// every artifact (ArtifactViewer also resets it when the artifact id changes),
// so opening another doc always lands in the rendered view.
export function useViewerPrefs() {
  const [width, setWidthState] = useState<Width>("medium");
  const [textSize, setTextSizeState] = useState<TextSize>("md");
  const [original, setOriginal] = useState<boolean>(false);

  useEffect(() => {
    if (typeof window === "undefined") return;
    const w = window.localStorage.getItem(WIDTH_KEY);
    if (w === "wide" || w === "medium" || w === "narrow") setWidthState(w);
    const s = window.localStorage.getItem(TEXTSIZE_KEY);
    if (s === "sm" || s === "md" || s === "lg") setTextSizeState(s);
  }, []);

  function setWidth(w: Width) {
    setWidthState(w);
    if (typeof window !== "undefined") window.localStorage.setItem(WIDTH_KEY, w);
  }
  function setTextSize(s: TextSize) {
    setTextSizeState(s);
    if (typeof window !== "undefined") window.localStorage.setItem(TEXTSIZE_KEY, s);
  }
  return { width, setWidth, textSize, setTextSize, original, setOriginal };
}

// Width breakpoints picked so a typical markdown article reads
// comfortably at "Narrow" (~70ch), "Medium" lands halfway to "Wide",
// and "Wide" gives data-heavy artifacts (tables, HTML dashboards) room
// to breathe. Narrow + Medium pulled 15% tighter than the original
// 820 / 1110 to match a more book-like reading width on Narrow.
export const WIDTH_CLASS: Record<Width, string> = {
  narrow: "max-w-[700px]",
  medium: "max-w-[944px]",
  wide:   "max-w-[1400px]",
};

// TEXT_SCALE multiplies the rendered body's font sizes. `md` === 1 reproduces
// today's sizes exactly (existing docs render pixel-identically); `sm`/`lg`
// step down/up from there. ArtifactViewer publishes it as a `--arti-text-scale`
// CSS variable on the doc container; the authoritative (unlayered) `.prose` /
// `pre` font-size rules in globals.css are `calc()`'d against it, and headings /
// inline code are sized in `em` so they ride that scaled root. Driving it via an
// inherited CSS var (not a prop) means the memo'd MarkdownBody never re-renders
// on a size change, so the comment overlay's <mark> highlights stay put.
export const TEXT_SCALE: Record<TextSize, number> = {
  sm: 0.85,
  md: 1,
  lg: 1.15,
};

// RawToggle — the per-view "show unrendered source" switch. Rendered by
// ArtifactViewer on the right side of the controls row (just before the
// access button), away from the width/Full Page cluster on the left.
export function RawToggle({
  original,
  setOriginal,
}: {
  original: boolean;
  setOriginal: (v: boolean) => void;
}) {
  return (
    <button
      type="button"
      onClick={() => setOriginal(!original)}
      title={original ? "showing raw source — click to render" : "show the artifact's raw, unrendered source"}
      className={
        "rounded-md border px-3 py-1 text-xs transition " +
        (original
          ? "border-neutral-300 bg-neutral-200 text-neutral-800"
          : "border-neutral-200 bg-white text-neutral-700 hover:bg-neutral-50")
      }
    >
      Raw
    </button>
  );
}

// WidthControl — the three-way reading-width segmented control. Exported on its
// own because two surfaces need it without the rest of the toolbar: the "New
// artifact" page (which has no text-size or Full Page affordance) and the
// viewer, which hides it entirely for diagrams rather than leave a control that
// does nothing.
export function WidthControl({
  width,
  setWidth,
}: {
  width: Width;
  setWidth: (w: Width) => void;
}) {
  // Three centered "content lines" that shrink from full-bleed (Wide) to a
  // narrow column (Narrow), mirroring the reading width each option sets. The
  // svg renders at 16px so the pill's height matches the text-size and Full Page
  // pills (all a 16px line-box + py-1 = 26px). Tooltip + aria-label carry the
  // word for anyone unsure of the glyph.
  const widthIcon = (x1: number, x2: number) => (
    <svg
      width="16"
      height="16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.2"
      strokeLinecap="round"
      aria-hidden="true"
    >
      <line x1={x1} y1="7" x2={x2} y2="7" />
      <line x1={x1} y1="12" x2={x2} y2="12" />
      <line x1={x1} y1="17" x2={x2} y2="17" />
    </svg>
  );

  const widthBtn = (w: Width, icon: ReactNode, title: string) => (
    <button
      key={w}
      onClick={() => setWidth(w)}
      title={title}
      aria-label={`width — ${title.toLowerCase()}`}
      aria-pressed={width === w}
      className={
        "inline-flex items-center justify-center px-2.5 py-1 text-xs transition " +
        (width === w
          ? "bg-neutral-200 text-neutral-800"
          : "bg-white text-neutral-600 hover:bg-neutral-50")
      }
    >
      {icon}
    </button>
  );

  return (
    <div
      className="inline-flex overflow-hidden rounded-md border border-neutral-200"
      role="group"
      aria-label="reading width"
    >
      {widthBtn("wide", widthIcon(3, 21), "Wide")}
      {widthBtn("medium", widthIcon(6, 18), "Medium")}
      {widthBtn("narrow", widthIcon(8, 16), "Narrow")}
    </div>
  );
}

export default function ViewerToolbar({
  width,
  setWidth,
  textSize,
  setTextSize,
  visitHref,
  fullHref,
  showWidth = true,
}: {
  width: Width;
  setWidth: (w: Width) => void;
  textSize: TextSize;
  setTextSize: (s: TextSize) => void;
  visitHref?: string;
  fullHref?: string;
  // Diagrams always render at full width, so the control would be inert —
  // hidden rather than shown doing nothing (same reasoning as hiding Raw while
  // the editor is open).
  showWidth?: boolean;
}) {
  // Text-size toggle mirrors the width control's segmented style. A graduated
  // capital "A" (S/M/L). "A" is bottom-heavy, so a geometric center reads high —
  // each glyph is nudged down a hair (more for the smaller ones) to sit
  // optically centered. lineHeight is PINNED to 16px: Tailwind v4's `text-xs`
  // line-height is the unitless ratio calc(1/.75)≈1.333, which this child <span>
  // inherits and multiplies by ITS OWN font-size — so the 17px "A" would balloon
  // the line box to ~23px and make this pill taller than the width control and
  // the rest of the row. Pinning to an absolute 16px keeps all three cells (and
  // the whole controls row) the same height.
  const sizeGlyph = (fontPx: number, nudgePx: number) => (
    <span
      className="inline-block"
      style={{ fontSize: `${fontPx}px`, lineHeight: "16px", transform: `translateY(${nudgePx}px)` }}
    >
      A
    </span>
  );

  const sizeBtn = (s: TextSize, glyph: ReactNode, title: string) => (
    <button
      key={s}
      onClick={() => setTextSize(s)}
      title={title}
      aria-label={title}
      aria-pressed={textSize === s}
      className={
        "inline-flex items-center justify-center px-2.5 py-1 text-xs transition " +
        (textSize === s
          ? "bg-neutral-200 text-neutral-800"
          : "bg-white text-neutral-600 hover:bg-neutral-50")
      }
    >
      {glyph}
    </button>
  );

  return (
    <>
      {showWidth ? <WidthControl width={width} setWidth={setWidth} /> : null}

      <div
        className="inline-flex overflow-hidden rounded-md border border-neutral-200"
        role="group"
        aria-label="text size"
      >
        {sizeBtn("sm", sizeGlyph(9, 1.5), "text size — small")}
        {sizeBtn("md", sizeGlyph(12.5, 0.3), "text size — medium (default)")}
        {sizeBtn("lg", sizeGlyph(17, 0), "text size — large")}
      </div>

      {fullHref ? (
        <a
          href={fullHref}
          className="inline-flex items-center gap-1.5 rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
        >
          <svg
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden="true"
            className="text-neutral-400"
          >
            <polyline points="15 3 21 3 21 9" />
            <polyline points="9 21 3 21 3 15" />
            <line x1="21" y1="3" x2="14" y2="10" />
            <line x1="3" y1="21" x2="10" y2="14" />
          </svg>
          Full Page
        </a>
      ) : null}

      {/* APP artifacts take this slot instead of Full Page: the running app IS
          the chrome-less view, so showing both read as two names for one thing
          (ArtifactViewer suppresses fullHref for APP). Same trailing position
          so the row's shape doesn't shift between types. */}
      {visitHref ? (
        <a
          href={visitHref}
          className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700"
          title="open the running app, full-page"
        >
          Visit app ↗
        </a>
      ) : null}
    </>
  );
}
