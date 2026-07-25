"use client";

import { useEffect } from "react";
import { mountCommentsOverlay } from "@/lib/commentsOverlay";
import type { Me } from "@/lib/types";

// Mounts the comments overlay over arti-rendered content (markdown / text /
// plain / JSON), anchoring into the prose via the [data-arti-doc] marker.
//
// HTML — whether a standalone artifact or a PACKAGE file — renders in a
// sandboxed iframe loaded via `src` from arti-server, whose served page gets
// the overlay INJECTED in-page (the comments-embed bundle). That injected
// overlay does full text-select + pin commenting inside the iframe. The outer
// overlay can't reach across the opaque-iframe boundary, so for HTML and
// packages we suppress it — otherwise you'd get two sets of controls.
//
// Known limitation: a PACKAGE whose viewed file is markdown/plain (rendered in
// React via PackageBody, not an iframe) gets no overlay at all — it's
// suppressed here yet has nothing injected. We don't anchor those because all
// files in a package share one artifact_id, so per-file comments would mix;
// package commenting is scoped to the served-HTML surface. Standalone
// markdown/text/json still get the outer overlay below.
export default function CommentsLayer({
  artifactId,
  me,
  contentType,
  artifactType,
}: {
  artifactId: string;
  me?: Me | null;
  contentType?: string;
  artifactType?: string;
  fullPage?: boolean;
  fullPageHref?: string;
}) {
  const isHTML = !!contentType && contentType.startsWith("text/html");
  const suppressed = isHTML || artifactType === "PACKAGE"; // injected in-page overlay handles these

  useEffect(() => {
    if (suppressed) return;
    const container = document.querySelector<HTMLElement>("[data-arti-doc]");
    const dispose = mountCommentsOverlay({
      container,
      artifactId,
      me: me ? { email: me.email, name: me.name, picture: me.picture, is_admin: me.is_admin } : null,
      allowPin: false, // pins are for served HTML pages (injected overlay); arti-rendered docs use text-select
    });
    return dispose;
  }, [artifactId, me, contentType, suppressed]);

  return null;
}
