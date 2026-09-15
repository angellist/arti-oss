"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { listApiKeys } from "@/lib/arti";

const EXPIRY_WARN_DAYS = 7;

interface ExpiringKey {
  name: string;
  daysLeft: number;
}

// The SETTINGS rail link, styled as one of the rail's action rows. It also
// carries the expiring-API-key badge, because Settings → API Keys is where the
// reader has to go to fix one.
export default function SideNavSettings() {
  const pathname = usePathname();
  const [expiringKey, setExpiringKey] = useState<ExpiringKey | null>(null);

  useEffect(() => {
    listApiKeys()
      .then((keys) => {
        const now = Date.now();
        const warnMs = EXPIRY_WARN_DAYS * 24 * 60 * 60 * 1000;
        for (const k of keys) {
          if (k.revoked_at) continue;
          const exp = new Date(k.expires_at).getTime();
          if (exp > now && exp - now < warnMs) {
            setExpiringKey({ name: k.name, daysLeft: Math.ceil((exp - now) / (24 * 60 * 60 * 1000)) });
            return;
          }
        }
      })
      .catch(() => {
        // Best-effort; don't surface errors for the badge.
      });
  }, []);

  return (
    <Link
      href="/settings"
      className={
        "flex w-full items-center gap-1.5 text-[10px] font-semibold tracking-widest uppercase transition " +
        (pathname.startsWith("/settings")
          ? "text-neutral-900"
          : "text-neutral-400 hover:text-neutral-600")
      }
    >
      <GearIcon className="h-3 w-3 shrink-0" />
      Settings
      {expiringKey && (
        <span
          title={`API key '${expiringKey.name}' expires in ${expiringKey.daysLeft} day${expiringKey.daysLeft === 1 ? "" : "s"}. Keys don't auto-refresh — create a new one in Settings → API Keys, update your agent's ARTI_TOKEN, then revoke the old key.`}
          className="rounded bg-yellow-400 px-1 text-[10px] leading-none font-bold text-white"
        >
          !
        </span>
      )}
    </Link>
  );
}

function GearIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 1 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 1 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 1 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 1 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
    </svg>
  );
}
