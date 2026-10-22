"use client";

import { useEffect, type RefObject } from "react";
import { getMe } from "./arti";
import { hostDictation } from "./dictationBridge";

let enabled: Promise<boolean> | null = null;
const dictationEnabled = () =>
  (enabled ??= getMe().then(
    (me) => !!me.dictation_enabled,
    () => {
      enabled = null; // a failed lookup is retried by the next frame, not cached as "off"
      return false;
    },
  ));

// Unarmed, the frame never gets its "ready", so it shows no mic and its
// author's scripts cannot raise the record prompt.
export function useDictationBridge(iframeRef: RefObject<HTMLIFrameElement | null>): void {
  useEffect(() => {
    let off: (() => void) | null = null;
    let gone = false;
    void dictationEnabled().then((on) => {
      if (on && !gone) off = hostDictation(() => iframeRef.current);
    });
    return () => {
      gone = true;
      off?.();
    };
  }, [iframeRef]);
}
