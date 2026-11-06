"use client";

import { useEffect } from "react";

import { isTopLevelWindow } from "@/lib/viewer";

// Tells an embedding host (e.g. a side panel) which version this frame shows,
// on every load. The host pins a version in the frame URL, and the stale-version
// banner's "View latest" navigates inside the frame, so without this the host's
// header keeps naming the version it asked for. The payload names only what the
// host chose to load, and frame-ancestors limits who can embed, so any origin.
export default function EmbedViewerReport({
  slug,
  id,
  version,
  latestVersion,
}: {
  slug?: string | null;
  id: string;
  version: number | null;
  latestVersion?: number | null;
}) {
  useEffect(() => {
    if (!version || isTopLevelWindow(window)) return;
    window.parent.postMessage(
      { type: "arti:viewer", slug: slug ?? undefined, id, version, latest_version: latestVersion ?? undefined },
      "*",
    );
  }, [slug, id, version, latestVersion]);
  return null;
}
