"use client";

import { useEffect, useRef, useState } from "react";

// StaleVersionBanner — one-line notice shown when the viewer is pinned to an
// older version than the slug's latest.
//
// Deliberately a THIN STRIP, not a floating card: it used to render as a
// centred fixed panel with a shadow, which sat on top of the artifact header
// (title, type/size/time row, label + scope chips, toolbar) and hid exactly the
// metadata a reader arriving on an old version wants to check. As a strip it
// costs one text line and blocks nothing.
//
// Two placements:
//   - default (in-flow): first child of the right pane, above the sticky
//     artifact topbar, so it scrolls away with the rest of the header.
//   - `floating`: for ?v=full, where FullPageView paints a fixed, viewport-
//     filling layer — an in-flow strip would be covered, so pin the strip to
//     the top edge above it. Because that layer is fixed, nothing in normal
//     flow can push it down; instead the strip measures its own height and
//     publishes it as --arti-top-strip on <html>, which `.arti-fullpage-layer`
//     reads as its `top`. Measured (not hardcoded) so a two-line wrap on a
//     narrow viewport still clears, and cleared on dismiss/unmount so the
//     content reclaims the line.
export default function StaleVersionBanner({
  currentVersion,
  latestVersion,
  latestHref,
  floating = false,
}: {
  currentVersion: number;
  latestVersion: number;
  latestHref: string;
  floating?: boolean;
}) {
  const [dismissed, setDismissed] = useState(false);
  const stripRef = useRef<HTMLDivElement | null>(null);

  // While floating, keep --arti-top-strip on <html> equal to the strip's real
  // height so the fixed full-page layer starts below it.
  useEffect(() => {
    const el = stripRef.current;
    const root = document.documentElement;
    const clear = () => root.style.removeProperty("--arti-top-strip");
    if (!floating || dismissed || !el) {
      clear();
      return clear;
    }
    const publish = () => {
      root.style.setProperty("--arti-top-strip", `${Math.round(el.getBoundingClientRect().height)}px`);
    };
    publish();
    const ro = new ResizeObserver(publish);
    ro.observe(el);
    return () => {
      ro.disconnect();
      clear();
    };
  }, [floating, dismissed]);

  if (dismissed) {
    return null;
  }

  return (
    <div
      ref={stripRef}
      className={
        floating
          ? "fixed inset-x-0 top-0 z-[70] border-b border-amber-200 bg-amber-50/95 backdrop-blur"
          : "border-b border-amber-200 bg-amber-50"
      }
    >
      <div className="flex items-center gap-2 px-4 py-1 text-[12px] leading-5 text-amber-900">
        <span className="min-w-0 truncate">
          Viewing version <span className="font-semibold">v{currentVersion}</span> — a newer version{" "}
          <span className="font-semibold">v{latestVersion}</span> is available.
        </span>
        <a
          href={latestHref}
          className="shrink-0 font-semibold text-amber-800 underline decoration-amber-400 underline-offset-2 transition hover:text-amber-950"
        >
          View latest →
        </a>
        <button
          type="button"
          onClick={() => setDismissed(true)}
          aria-label="dismiss newer version banner"
          className="ml-auto shrink-0 rounded px-1 leading-none text-amber-600 transition hover:bg-amber-100 hover:text-amber-900"
        >
          ×
        </button>
      </div>
    </div>
  );
}
