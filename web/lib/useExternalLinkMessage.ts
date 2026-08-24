"use client";

import { useEffect, type RefObject } from "react";

export function handleExternalLinkMessage(
  e: MessageEvent,
  frame: HTMLIFrameElement | null,
): void {
  if (!frame || e.source !== frame.contentWindow) return;
  const data = e.data as { source?: unknown; url?: unknown } | null;
  if (!data || data.source !== "arti-open-external" || typeof data.url !== "string") return;
  let url: URL;
  try {
    url = new URL(data.url);
  } catch {
    return;
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") return;
  window.open(url.href, "_blank", "noopener");
}

export function useExternalLinkMessage(
  iframeRef: RefObject<HTMLIFrameElement | null>,
): void {
  useEffect(() => {
    const onMessage = (e: MessageEvent) => handleExternalLinkMessage(e, iframeRef.current);
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [iframeRef]);
}
