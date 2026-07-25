"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { KINDS, type NewKind } from "@/lib/newartifact";

// A boxes-and-connector glyph, drawn at the same weight as SearchIcon /
// BrowseIcon / UploadButton's arrow so the rail links read as one set.
function DiagramIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <rect x="3" y="3" width="7" height="6" rx="1" />
      <rect x="14" y="15" width="7" height="6" rx="1" />
      <path d="M6.5 9v4a2 2 0 0 0 2 2h9" />
    </svg>
  );
}

// A lined-page glyph for the markdown kind.
function TextIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <path d="M5 3h9l5 5v13H5z" />
      <path d="M9 12h6M9 16h6" />
    </svg>
  );
}

function PlusIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
    >
      <line x1="12" y1="5" x2="12" y2="19" />
      <line x1="5" y1="12" x2="19" y2="12" />
    </svg>
  );
}

const ICONS: Record<NewKind, (p: { className?: string }) => React.ReactElement> = {
  text: TextIcon,
  diagram: DiagramIcon,
};

const ORDER: NewKind[] = ["text", "diagram"];

// NewMenu is the rail's authoring entry point: NEW, revealing the kinds arti can
// create in the browser. Upload stays a separate rail link — it's a different
// gesture (file picker / drag-and-drop, its own modal) rather than another kind
// of document.
//
// Opens on hover AND on click/Enter: hover alone is unreachable by keyboard and
// unusable on touch.
export default function NewMenu() {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  // Viewport coordinates for the panel. The rail scrolls (overflow-y-auto /
  // overflow-x-hidden), which CLIPS an absolutely-positioned child: the panel
  // showed as a 12px sliver at the rail's edge. So it's portalled to <body> and
  // positioned from the trigger's rect instead of being laid out inside the rail.
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  // Closing on pointer-out is delayed a beat: the gap between the trigger and
  // the panel would otherwise snap the menu shut mid-travel.
  const closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Drops BELOW the trigger, indented under its label, so it reads as NEW's own
  // submenu instead of floating off to the right of the rail.
  const place = useCallback(() => {
    const r = triggerRef.current?.getBoundingClientRect();
    if (!r) return;
    setPos({ top: r.bottom + 6, left: r.left + 14 });
  }, []);

  const cancelClose = () => {
    if (closeTimer.current) {
      clearTimeout(closeTimer.current);
      closeTimer.current = null;
    }
  };
  const scheduleClose = () => {
    cancelClose();
    closeTimer.current = setTimeout(() => setOpen(false), 180);
  };

  useEffect(() => cancelClose, []);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      // The panel is portalled out of the rail, so a click inside it is NOT
      // inside wrapRef — check it separately or the menu closes on its own items.
      if (!wrapRef.current?.contains(t) && !panelRef.current?.contains(t)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    // Portalled coordinates go stale when the page or the rail scrolls.
    window.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey);
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [open, place]);

  const openNow = () => {
    cancelClose();
    place();
    setOpen(true);
  };

  const panel =
    open && pos ? (
      <div
        ref={panelRef}
        role="menu"
        aria-label="new artifact"
        // Fixed + portalled: laid out against the viewport so the rail's
        // overflow-x-hidden can't clip it (it did — the panel showed as a ~12px
        // sliver). Overlays the chips below rather than pushing them around.
        style={{ top: pos.top, left: pos.left }}
        onMouseEnter={cancelClose}
        onMouseLeave={scheduleClose}
        className="fixed z-50 w-48 rounded-md border border-neutral-200 bg-white py-1 shadow-lg"
      >
        {ORDER.map((kind) => {
          const Icon = ICONS[kind];
          return (
            <Link
              key={kind}
              role="menuitem"
              href={`/new/${kind}`}
              onClick={() => setOpen(false)}
              className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-neutral-700 no-underline transition hover:bg-neutral-50 hover:text-neutral-900"
            >
              <Icon className="h-3.5 w-3.5 shrink-0 text-neutral-400" />
              {KINDS[kind].label}
            </Link>
          );
        })}
      </div>
    ) : null;

  return (
    <div ref={wrapRef} onMouseEnter={openNow} onMouseLeave={scheduleClose}>
      <button
        ref={triggerRef}
        type="button"
        onClick={() => (open ? setOpen(false) : openNow())}
        aria-haspopup="menu"
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 text-[10px] font-semibold uppercase tracking-widest text-neutral-400 transition hover:text-neutral-600"
      >
        <PlusIcon className="h-3 w-3 shrink-0" />
        New
      </button>

      {typeof document === "undefined" ? panel : panel && createPortal(panel, document.body)}
    </div>
  );
}
