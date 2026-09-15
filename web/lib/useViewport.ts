"use client";

import { useSyncExternalStore } from "react";
import { htmlFitZoom, isTopLevelWindow } from "./viewer";

// Browser facts read during render — am I framed? how wide is the window? —
// go through a store, so SSR gets a snapshot and the client re-reads after
// hydration without a mismatch. Both server snapshots answer as if EMBEDDED:
// chrome that flashes inside couch's side panel is worse than chrome that
// arrives a frame late in a normal tab.

// Framing cannot change for a document's lifetime, so nothing to publish.
const subscribeNever = () => () => {};

const subscribeResize = (onChange: () => void) => {
  window.addEventListener("resize", onChange);
  return () => window.removeEventListener("resize", onChange);
};

/** True once hydrated in a top-level (non-embedded) window; false on the server. */
export function useTopLevelWindow(): boolean {
  return useSyncExternalStore(
    subscribeNever,
    () => isTopLevelWindow(window),
    () => false,
  );
}

/**
 * The fit-to-width zoom for an HTML artifact body (see htmlFitZoom). 1 on the
 * server and inside an embed, where the host sizes the frame and passes its
 * own `?ts=` — compounding the two would render the page twice as small as
 * either asked for.
 */
export function useHtmlFitZoom(): number {
  return useSyncExternalStore(
    subscribeResize,
    () => (isTopLevelWindow(window) ? htmlFitZoom(window.innerWidth) : 1),
    () => 1,
  );
}
