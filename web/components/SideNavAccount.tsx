"use client";

import { useEffect, useRef, useState } from "react";
import type { Me } from "@/lib/types";
import { RailTip, railTileClass } from "./RailTile";

// The rail foot: who you are signed in as, and the one action that belongs to
// that identity. Everything else that used to hang off this menu is a tab under
// Settings, so the menu holds a single item.
function initials(email: string): string {
  const local = email.split("@")[0] ?? "";
  const parts = local.split(/[\s._-]+/).filter(Boolean);
  const take = parts.length >= 2 ? [parts[0], parts[1]] : parts;
  return take
    .map((p) => p[0] ?? "")
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

// `compact` shows only the avatar, for the collapsed rail; the menu opens to its right.
export default function SideNavAccount({ me, compact = false }: { me: Me | null; compact?: boolean }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click / Escape (only while open, to avoid idle listeners).
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // No identity yet (initial load) or /api/me failed: there is no email to sit
  // behind a menu, so the action stands on its own.
  if (!me?.email) {
    if (compact) {
      return (
        <a href="/auth/logout" aria-label="Log Out" className={railTileClass()}>
          <SignOutIcon />
          <RailTip>Log Out</RailTip>
        </a>
      );
    }
    return (
      <a href="/auth/logout" className="block text-[11px] text-neutral-600 hover:underline">
        Log Out
      </a>
    );
  }

  return (
    <div ref={ref} className="relative">
      <button
        type="button"
        onClick={(e) => {
          // Stop this toggle click from reaching SideNav's mobile-drawer
          // delegate-close (any <a>/<button> click closes the drawer) — the
          // menu item below doesn't call stopPropagation, so signing out still
          // closes the drawer.
          e.stopPropagation();
          setOpen((o) => !o);
        }}
        aria-haspopup="menu"
        aria-expanded={open}
        title={me.email}
        aria-label={compact ? `Account: ${me.email}` : undefined}
        className={
          compact
            ? "grid h-8 w-8 place-items-center rounded-full hover:bg-neutral-100 focus-visible:bg-neutral-100 focus-visible:outline-none"
            : "flex w-full items-center justify-between gap-1 rounded px-1 py-1 text-left text-[11px] text-neutral-600 hover:bg-neutral-100"
        }
      >
        {compact ? (
          <span className="grid h-6 w-6 place-items-center rounded-full bg-emerald-600 text-[10px] font-semibold text-white">
            {initials(me.email)}
          </span>
        ) : (
        <span className="flex min-w-0 items-center gap-1.5">
          <span className="grid h-5 w-5 shrink-0 place-items-center rounded-full bg-emerald-600 text-[10px] font-semibold text-white">
            {initials(me.email)}
          </span>
          <span className="truncate">{me.email}</span>
        </span>
        )}
        {!compact && <KebabIcon />}
      </button>
      {open ? (
        <div
          role="menu"
          className={
            (compact ? "absolute bottom-0 left-full z-30 ml-2 " : "absolute bottom-full left-0 z-30 mb-1 w-full ") +
            "min-w-[160px] overflow-hidden rounded-md border border-neutral-200 bg-white py-1 shadow-lg"
          }
        >
          {compact && <div className="truncate px-3 py-1.5 text-[11px] text-neutral-500">{me.email}</div>}
          <a
            role="menuitem"
            href="/auth/logout"
            className="flex items-center gap-2 px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            <SignOutIcon />
            Log Out
          </a>
        </div>
      ) : null}
    </div>
  );
}

function KebabIcon() {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="currentColor"
      className="shrink-0 text-neutral-400"
      aria-hidden="true"
    >
      <circle cx="12" cy="5" r="1.6" />
      <circle cx="12" cy="12" r="1.6" />
      <circle cx="12" cy="19" r="1.6" />
    </svg>
  );
}

function SignOutIcon() {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="shrink-0 text-neutral-400"
      aria-hidden="true"
    >
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <polyline points="16 17 21 12 16 7" />
      <line x1="21" y1="12" x2="9" y2="12" />
    </svg>
  );
}
