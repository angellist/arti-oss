"use client";

import { useEffect, useRef } from "react";
import { fileParamSearch, resolvePackageEntry } from "@/lib/viewer";

// The full-page HTML iframe, plus the listener that keeps the browser URL
// (?file=<path>) in sync as the reader follows in-content links from one
// package file to another.
//
// Why a listener at all: the iframe is served CSP `sandbox` WITHOUT
// allow-same-origin (an opaque origin), so the parent CANNOT read
// iframe.contentWindow.location to see where an in-content click navigated.
// arti-server instead injects a tiny reporter into every served package HTML
// page (injectFileNavReporter) that postMessages its own file path on load;
// this component receives that and rewrites the URL. It never touches the
// iframe — the iframe already navigated itself — so there's no reload/flash.
//
// Sandbox rationale lives in FullPageView (the parent) — kept identical here.
export default function FullPageHtmlFrame({
  src,
  srcDoc,
  title,
  entries,
  entryPoint,
  initialFile,
}: {
  src?: string;
  srcDoc?: string;
  title: string;
  // Manifest entries for validation; absent for a non-package HTML artifact
  // (no sibling files ⇒ no in-content file navigation to track).
  entries?: Array<{ path: string; content_type: string }>;
  entryPoint?: string | null;
  initialFile?: string;
}) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  // The file currently reflected in the URL. Seeded from what the server
  // resolved for this load, so the entry document's own on-load report is a
  // no-op and chained navigations don't rewrite the URL redundantly.
  const currentFile = useRef<string | null>(initialFile ?? null);

  useEffect(() => {
    // No sibling files (single HTML artifact / srcDoc fallback) ⇒ nothing to
    // track. Installed once on mount so it's ready before any user-initiated
    // navigation (the initial on-load report, which we'd skip anyway, may race
    // ahead of this — that's fine).
    if (!entries || entries.length === 0) return;
    const onMessage = (e: MessageEvent) => {
      // Accept ONLY the reporter posted by our own content frame. A nested
      // iframe the package might create posts from a different source and is
      // rejected. (Origin can't be checked — the opaque-origin page's origin is
      // "null" — so we bind to the frame identity, like the APP OAuth bridge.)
      if (!iframeRef.current || e.source !== iframeRef.current.contentWindow) return;
      const data = e.data as { source?: unknown; path?: unknown } | null;
      if (!data || data.source !== "arti-file-nav" || typeof data.path !== "string") return;
      // Resolve the reported raw path exactly as the server does, and require it
      // to be a real manifest entry: a hostile page can at worst point the URL
      // at another real file in the same package, never a bogus path.
      const entry = resolvePackageEntry(entries, data.path);
      if (!entry || entry.path === currentFile.current) return;
      currentFile.current = entry.path;
      const search = fileParamSearch(window.location.search, entry.path, entryPoint ?? null);
      window.history.replaceState(null, "", window.location.pathname + search);
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [entries, entryPoint]);

  return (
    <iframe
      ref={iframeRef}
      sandbox="allow-scripts allow-popups allow-popups-to-escape-sandbox allow-top-navigation-by-user-activation allow-downloads"
      {...(src ? { src } : { srcDoc })}
      title={title}
      className="arti-fullpage-layer block w-full border-0 bg-white"
    />
  );
}
