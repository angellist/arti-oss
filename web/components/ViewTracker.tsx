"use client";

import { useEffect, useRef } from "react";
import { getViewStats, recordView } from "@/lib/arti";

const SESSION_KEY = "arti.viewedArtifacts";

function viewedInSession(id: string): boolean {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    return raw ? (JSON.parse(raw) as unknown[]).includes(id) : false;
  } catch {
    return false;
  }
}

function markViewed(id: string): void {
  try {
    const raw = sessionStorage.getItem(SESSION_KEY);
    const ids = raw ? (JSON.parse(raw) as unknown[]) : [];
    if (!ids.includes(id)) sessionStorage.setItem(SESSION_KEY, JSON.stringify([...ids, id]));
  } catch {
    // Storage is optional; the server-side dedupe remains the backstop.
  }
}

export default function ViewTracker({
  artifactID,
  onCount,
}: {
  artifactID: string;
  onCount?: (total: number, last30d: number) => void;
}) {
  const onCountRef = useRef(onCount);
  useEffect(() => {
    onCountRef.current = onCount;
  }, [onCount]);

  useEffect(() => {
    let cancelled = false;
    const record = viewedInSession(artifactID)
      ? Promise.resolve()
      : recordView(artifactID).then(() => markViewed(artifactID));
    void record
      .then(() => getViewStats(artifactID))
      .then((stats) => {
        if (!cancelled) onCountRef.current?.(stats.total, stats.last_30d);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [artifactID]);
  return null;
}
