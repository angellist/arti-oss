"use client";

import { useEffect, useState, useSyncExternalStore } from "react";

import { hardNavigate } from "@/lib/navigate";
import { exitFullPageSearch } from "@/lib/viewer";
import { useTopLevelWindow } from "@/lib/useViewport";

const EXIT_SIZE = 18;

// The way out of the chrome-less ?v=full view: a small square ✕ wedged into
// the top-right corner with no inset, so it takes as little of the content as
// a control can. Its top offset clears --arti-top-strip, which a stale-version
// banner occupies. Flush right means it overlaps the top of a classic
// (non-overlay) scrollbar on the content frame below.
export default function FullPageExit() {
  // Not rendered inside an embed: couch's side panel iframes this view, owns
  // its own chrome, and has no normal view to return to.
  const topLevel = useTopLevelWindow();
  const dark = useHostTheme();
  if (!topLevel) return null;
  return (
    <button
      type="button"
      // Read at click time: a PACKAGE's in-content links rewrite ?file=
      // through replaceState while this button stays mounted.
      onClick={() => {
        hardNavigate(window.location.pathname + exitFullPageSearch(window.location.search));
      }}
      title="back to normal view"
      aria-label="back to normal view"
      style={{
        top: "var(--arti-top-strip, 0px)",
        right: 0,
        width: EXIT_SIZE,
        height: EXIT_SIZE,
      }}
      className={
        "fixed z-[60] grid place-items-center border-b border-l text-[#9b9b95] opacity-70 " +
        "backdrop-blur-[12px] transition hover:opacity-100 " +
        (dark
          ? "border-[rgba(255,255,255,.14)] bg-[rgba(32,32,30,.78)] hover:bg-[rgba(255,255,255,.08)] hover:text-[#f2f1ee]"
          : "border-[rgba(230,229,225,.9)] bg-white/[.72] hover:bg-[#f6f5f2] hover:text-[#33332e]")
      }
    >
      <svg
        width="11"
        height="11"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        aria-hidden="true"
      >
        <path d="M18 6 6 18M6 6l12 12" />
      </svg>
    </button>
  );
}

// True when the surface under this bubble is dark. Two kinds of full-page view
// can be: one arti renders itself, which follows the reader's theme, and an
// HTML body in an opaque-origin iframe whose background nothing out here can
// read. So the app theme is the starting answer, and a frame's own report
// overrides it — the frame is what fills the screen when there is one. A page
// with commenting off injects no overlay, so nothing reports.
function useHostTheme(): boolean {
  // The attribute is external state, written by the pre-paint script before
  // React exists, so it is read through a store rather than seeded in an
  // effect. Nothing subscribes: the theme is written once per document load,
  // and choosing another one navigates away from this view.
  const appDark = useSyncExternalStore(
    () => () => {},
    () => document.documentElement.dataset.theme === "dark",
    () => false,
  );
  const [reported, setReported] = useState<boolean | null>(null);
  useEffect(() => {
    const frames = () => Array.from(document.querySelectorAll("iframe"));
    const onMessage = (e: MessageEvent) => {
      // Only a frame this document embeds; the sandboxed page's origin is the
      // string "null", so identity is the only check available to us.
      if (!frames().some((f) => f.contentWindow === e.source)) return;
      const data = e.data as { source?: unknown; dark?: unknown } | null;
      if (!data || data.source !== "arti-theme" || typeof data.dark !== "boolean") return;
      setReported(data.dark);
    };
    window.addEventListener("message", onMessage);
    // The overlay reports once at its own mount, which on a hard load can
    // happen before this listener exists — so ask too. Whichever side is
    // second carries the handshake.
    for (const f of frames()) {
      try {
        f.contentWindow?.postMessage({ source: "arti-theme-query" }, "*");
      } catch {
        /* a frame we can't post to has no theme to report */
      }
    }
    return () => window.removeEventListener("message", onMessage);
  }, []);
  // A frame's own report wins: when there is one, the frame is what fills the
  // screen behind this bubble.
  return reported ?? appDark;
}
