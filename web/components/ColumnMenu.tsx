"use client";

import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  COLUMNS,
  isVisible,
  type ColumnKey,
  type ColumnPrefs,
} from "@/lib/columns";

// ColumnMenu is the catalog header's right-click menu: one checkbox row per
// known column, plus a reset. Rendered through a portal at viewport
// coordinates so the table's `overflow-x-auto` wrapper can't clip it.
//
// It stays open across toggles on purpose — turning on three columns should be
// three clicks, not three right-clicks. Escape, an outside click, or scrolling
// the page closes it.
export default function ColumnMenu({
  x,
  y,
  prefs,
  onToggle,
  onReset,
  onClose,
}: {
  x: number;
  y: number;
  prefs: ColumnPrefs;
  onToggle: (key: ColumnKey) => void;
  onReset: () => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: x, top: y });

  // Flip the menu back inside the viewport when opened near an edge. Measured
  // after layout (not in an effect after paint) so it never renders once at the
  // overflowing position and then jumps.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const pad = 8;
    let left = x;
    let top = y;
    if (left + r.width > window.innerWidth - pad) left = Math.max(pad, window.innerWidth - r.width - pad);
    if (top + r.height > window.innerHeight - pad) top = Math.max(pad, window.innerHeight - r.height - pad);
    setPos({ left, top });
  }, [x, y]);

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    // Close when the page scrolls out from under the menu — but NOT when the
    // menu's own list scrolls. capture:true is required to see scrolls on
    // inner elements at all, which is precisely why the menu has to exclude
    // itself: without this check, ticking a box far enough down the list
    // scrolls it and the menu closes on the user mid-selection.
    const onScroll = (e: Event) => {
      if (ref.current?.contains(e.target as Node)) return;
      onClose();
    };
    // capture:true on keydown — the catalog's own document-level handler would
    // otherwise treat the same keystrokes as type-to-search.
    document.addEventListener("mousedown", onDown, true);
    document.addEventListener("keydown", onKey, true);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", onClose);
    return () => {
      document.removeEventListener("mousedown", onDown, true);
      document.removeEventListener("keydown", onKey, true);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", onClose);
    };
  }, [onClose]);

  return createPortal(
    <div
      ref={ref}
      role="menu"
      aria-label="choose columns"
      style={{ position: "fixed", left: pos.left, top: pos.top }}
      className="z-50 max-h-[70vh] w-64 overflow-y-auto rounded-md border border-neutral-200 bg-white py-1 text-[12px] shadow-lg"
    >
      <div className="px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wide text-neutral-400">
        columns
      </div>
      {COLUMNS.map((c) => {
        const on = isVisible(prefs, c.key);
        return (
          <button
            key={c.key}
            type="button"
            role="menuitemcheckbox"
            aria-checked={on}
            onClick={() => onToggle(c.key)}
            title={c.help}
            className="flex w-full items-start gap-2 px-3 py-1.5 text-left transition hover:bg-neutral-50"
          >
            <span
              aria-hidden
              className={
                "mt-[1px] inline-flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-[3px] border text-[10px] leading-none " +
                (on
                  ? "border-blue-600 bg-blue-600 text-white"
                  : "border-neutral-300 bg-white text-transparent")
              }
            >
              ✓
            </span>
            <span className="min-w-0">
              <span className="block text-neutral-800">{c.label}</span>
              <span className="block truncate text-[11px] text-neutral-400">{c.help}</span>
            </span>
          </button>
        );
      })}
      <div className="my-1 border-t border-neutral-100" />
      <button
        type="button"
        role="menuitem"
        onClick={onReset}
        className="w-full px-3 py-1.5 text-left text-neutral-600 transition hover:bg-neutral-50"
      >
        reset columns, order &amp; widths
      </button>
      <div className="px-3 pb-1 pt-0.5 text-[10px] leading-tight text-neutral-400">
        drag a header to reorder · drag its right edge to resize
      </div>
    </div>,
    document.body,
  );
}
