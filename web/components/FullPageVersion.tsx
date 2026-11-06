"use client";

import { EXIT_SIZE, cornerButtonClass, useHostTheme } from "./FullPageExit";
import { useTopLevelWindow } from "@/lib/useViewport";
import { slugVersionsHref } from "@/lib/viewer";
import type { ArtifactInfo } from "@/lib/types";

// Third in the ?v=full corner strip, after the ✕ and the access dot: the
// version number, linking to the slug's version list. Unnamed documents have
// no version list, so they show nothing here.
export default function FullPageVersion({ info }: { info: ArtifactInfo }) {
  const topLevel = useTopLevelWindow();
  const dark = useHostTheme();
  if (!topLevel || !info.named_slug || info.version == null) return null;
  return (
    <a
      href={slugVersionsHref(info.named_slug)}
      title="show every version of this slug"
      aria-label={`version ${info.version}: show every version`}
      style={{
        top: "var(--arti-top-strip, 0px)",
        left: 2 * EXIT_SIZE,
        height: EXIT_SIZE,
      }}
      className={`${cornerButtonClass(dark)} px-1 font-mono text-[10px] leading-none no-underline`}
    >
      v{info.version}
    </a>
  );
}
