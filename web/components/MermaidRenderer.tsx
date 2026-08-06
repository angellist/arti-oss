"use client";

import { useEffect } from "react";
import { renderMermaidIn } from "@/lib/mermaid";

export default function MermaidRenderer() {
  useEffect(() => {
    // Re-run after client navigation replaces server-rendered help HTML without remounting.
    const container = document.querySelector<HTMLElement>("[data-arti-doc]");
    if (!container) return;
    void renderMermaidIn(container).catch(() => {
      // A failed diagram is handled per block; the pass must never reject.
    });
  });
  return null;
}
