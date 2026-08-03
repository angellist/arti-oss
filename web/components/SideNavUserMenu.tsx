"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import type { Me } from "@/lib/types";
import { listApiKeys } from "@/lib/arti";

const EXPIRY_WARN_DAYS = 7;

// SideNavUserMenu turns the footer email into a dropdown menu trigger. The
// caret signals there's a menu; it opens UPWARD (the footer sits at the bottom
// of the rail). "User Groups" is admin-only; "Settings" is a disabled
// placeholder for future per-user settings. More items can be added later.
interface ExpiringKey {
  name: string;
  daysLeft: number;
}

export default function SideNavUserMenu({ me }: { me: Me | null }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const [expiringKey, setExpiringKey] = useState<ExpiringKey | null>(null);

  // On mount, check for API keys expiring within EXPIRY_WARN_DAYS.
  useEffect(() => {
    listApiKeys()
      .then((keys) => {
        const now = Date.now();
        const warnMs = EXPIRY_WARN_DAYS * 24 * 60 * 60 * 1000;
        for (const k of keys) {
          if (k.revoked_at) continue;
          const exp = new Date(k.expires_at).getTime();
          if (exp > now && exp - now < warnMs) {
            const daysLeft = Math.ceil((exp - now) / (24 * 60 * 60 * 1000));
            setExpiringKey({ name: k.name, daysLeft });
            return;
          }
        }
      })
      .catch(() => {
        // Best-effort; don't surface errors for the badge.
      });
  }, []);

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

  // No identity yet (initial load) or /api/me failed: there's no email to use
  // as a menu trigger, so fall back to plain links — Archived and Settings
  // stay reachable (they don't depend on identity), alongside sign out.
  if (!me?.email) {
    return (
      <div className="mt-2 space-y-1 text-[11px]">
        <Link href="/settings" className="block text-neutral-600 hover:underline">
          Settings
        </Link>
        <Link href="/archived" className="block text-neutral-600 hover:underline">
          Archived
        </Link>
        <Link href="/help" className="block text-neutral-600 hover:underline">
          Help &amp; Docs
        </Link>
        <a className="block text-blue-600 hover:underline" href="/auth/logout">
          sign out
        </a>
      </div>
    );
  }

  return (
    <div ref={ref} className="relative mt-2">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        title={me.email}
        className="flex w-full items-center justify-between gap-1 rounded px-1 py-1 text-left text-[11px] text-neutral-600 hover:bg-neutral-100"
      >
        <span className="flex min-w-0 items-center gap-1">
          <span className="truncate">{me.email}</span>
          {expiringKey && (
            <Link
              href="/settings/keys"
              onClick={(e) => e.stopPropagation()}
              title={`API key '${expiringKey.name}' expires in ${expiringKey.daysLeft} day${expiringKey.daysLeft === 1 ? "" : "s"}. Keys don't auto-refresh — create a new one in Settings → API Keys, update your agent's ARTI_TOKEN, then revoke the old key.`}
              className="shrink-0 rounded bg-yellow-400 px-1 text-[10px] font-bold leading-none text-white"
            >
              !
            </Link>
          )}
        </span>
        <Chevron open={open} />
      </button>
      {open ? (
        <div
          role="menu"
          className="absolute bottom-full left-0 z-30 mb-1 w-full min-w-[160px] overflow-hidden rounded-md border border-neutral-200 bg-white py-1 shadow-lg"
        >
          <Link
            href="/settings"
            role="menuitem"
            onClick={() => setOpen(false)}
            className="block px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            Settings
          </Link>
          <Link
            href="/archived"
            role="menuitem"
            onClick={() => setOpen(false)}
            className="block px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            Archived
          </Link>
          <Link
            href="/help"
            role="menuitem"
            onClick={() => setOpen(false)}
            className="block px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            Help &amp; Docs
          </Link>
          <div className="my-1 border-t border-neutral-100" />
          <a
            role="menuitem"
            href="/auth/logout"
            className="block px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            Sign out
          </a>
        </div>
      ) : null}
    </div>
  );
}

function Chevron({ open }: { open: boolean }) {
  return (
    <svg
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={"shrink-0 text-neutral-400 transition-transform " + (open ? "rotate-180" : "")}
      aria-hidden="true"
    >
      <polyline points="6 9 12 15 18 9" />
    </svg>
  );
}
