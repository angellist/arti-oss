"use client";

import { useEffect, type RefObject } from "react";

// The APP consent page arti serves inside a sandboxed (opaque-origin) frame
// cannot post its own form: that frame's requests carry no cookies. It asks
// the parent instead, and the parent — a real arti page — records the consent
// and reloads the frame, which now serves the app.
export async function handleAppConsentMessage(
  e: MessageEvent,
  frame: HTMLIFrameElement | null,
  post: (url: string, scope: string) => Promise<Response> = (url, scope) =>
    fetch(url, {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({ scope }).toString(),
    }),
): Promise<boolean> {
  if (!frame || e.source !== frame.contentWindow) return false;
  const data = e.data as { source?: unknown; appId?: unknown; scope?: unknown } | null;
  if (!data || data.source !== "arti-app-consent" || typeof data.appId !== "string") return false;
  // Only the two scopes the server knows; anything else records this version alone.
  const scope = data.scope === "slug" ? "slug" : "";
  // The framed document is untrusted, so the app it names must be the one the
  // parent put in the frame. frame.src is the parent-set attribute, not
  // wherever the frame has navigated since.
  const framed = /\/api\/artifacts\/([0-9a-f-]{36})\//i.exec(frame.src ?? "")?.[1];
  if (!framed || framed.toLowerCase() !== data.appId.toLowerCase()) return false;
  const res = await post(`/app/${framed}/consent`, scope);
  if (!res.ok) return false;
  // Same URL, fresh navigation: the consent cookie now rides along.
  frame.src = frame.src;
  return true;
}

export function useAppConsentMessage(iframeRef: RefObject<HTMLIFrameElement | null>): void {
  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      void handleAppConsentMessage(e, iframeRef.current);
    };
    window.addEventListener("message", onMessage);
    return () => window.removeEventListener("message", onMessage);
  }, [iframeRef]);
}
