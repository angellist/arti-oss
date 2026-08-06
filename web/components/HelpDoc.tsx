"use client";

import { useEffect, useState } from "react";
import MermaidRenderer from "./MermaidRenderer";

// Renders a help doc's sanitized HTML and makes its diagrams click-to-zoom.
// The body is server-rendered markdown (dangerouslySetInnerHTML); we delegate
// clicks on <img> elements to open a lightbox. Only diagram images (served
// from /help-diagrams/) are zoomable. Diagrams render on a transparent
// background, so the enlarged view sits on a white card to stay legible over
// the dark backdrop.
export default function HelpDoc({ html, className }: { html: string; className: string }) {
  const [zoom, setZoom] = useState<string | null>(null);

  useEffect(() => {
    if (!zoom) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setZoom(null);
    };
    document.addEventListener("keydown", onKey);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = prevOverflow;
    };
  }, [zoom]);

  const handleClick = (e: React.MouseEvent) => {
    const t = e.target as HTMLElement;
    if (t.tagName !== "IMG") return;
    const src = t.getAttribute("src");
    if (src && src.startsWith("/help-diagrams/")) setZoom(src);
  };

  return (
    <>
      <article
        data-arti-doc
        className={`${className} [&_img]:cursor-zoom-in`}
        onClick={handleClick}
        dangerouslySetInnerHTML={{ __html: html }}
      />
      <MermaidRenderer />
      {zoom ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Enlarged diagram"
          onClick={() => setZoom(null)}
          className="fixed inset-0 z-50 flex cursor-zoom-out items-center justify-center bg-black/70 p-4 sm:p-8"
        >
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={zoom}
            alt="Enlarged diagram"
            className="max-h-[90vh] max-w-[95vw] rounded-lg bg-white p-4 shadow-2xl sm:p-6"
          />
        </div>
      ) : null}
    </>
  );
}
