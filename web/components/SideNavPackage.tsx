"use client";

import Link from "next/link";
import type { PackageManifest } from "@/lib/types";

export default function SideNavPackage({
  manifest,
  selected,
  onSelect,
}: {
  manifest: PackageManifest;
  selected: string | null;
  onSelect: (path: string | null) => void;
}) {
  // Sort paths so directory headings group; the existing manifest is a
  // flat list, so this is the cheapest reasonable rendering. A nested-
  // tree pass can come later — flat-with-folder-prefix is enough to
  // navigate dozens of files.
  const entries = [...manifest.entries].sort((a, b) => a.path.localeCompare(b.path));

  return (
    <div className="space-y-3 text-sm">
      <Link
        href="/"
        className="inline-flex items-center gap-1 text-[12px] text-neutral-500 hover:text-neutral-800"
      >
        <span aria-hidden>◂</span> back to catalog
      </Link>

      <h3 className="mt-2 text-[10px] font-semibold uppercase tracking-widest text-neutral-400">
        files
      </h3>

      {entries.length === 0 ? (
        <p className="text-[11px] italic text-neutral-400">no files</p>
      ) : (
        <ul className="space-y-0.5">
          {entries.map((e) => {
            const active = selected === e.path;
            const base = e.path.split("/").pop() || e.path;
            const lower = base.toLowerCase();
            const isIndex = lower === "index.html" || lower === "index.htm";
            const isRenderable = lower.endsWith(".html") || lower.endsWith(".htm") || lower.endsWith(".md") || lower.endsWith(".markdown");
            // Visual hierarchy when the row isn't selected: index.html
            // pops the most (folder-entry-point vibe), other HTML/MD get
            // a subtle nudge, everything else is muted mono text.
            const idleClass = isIndex
              ? "font-semibold text-emerald-700 hover:bg-emerald-50"
              : isRenderable
                ? "text-neutral-800 hover:bg-neutral-100"
                : "text-neutral-500 hover:bg-neutral-100";
            return (
              <li key={e.path}>
                <button
                  type="button"
                  onClick={() => onSelect(e.path)}
                  title={e.content_type ? `${e.path} — ${e.content_type}` : e.path}
                  className={
                    "block w-full truncate rounded px-2 py-1 text-left text-[12px] " +
                    (active
                      ? "bg-blue-100 font-semibold text-blue-800"
                      : idleClass)
                  }
                >
                  {isIndex ? "★ " : ""}{e.path}
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
