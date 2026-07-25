"use client";

import { useState } from "react";

export default function StaleVersionBanner({
  currentVersion,
  latestVersion,
  latestHref,
}: {
  currentVersion: number;
  latestVersion: number;
  latestHref: string;
}) {
  const [dismissed, setDismissed] = useState(false);

  if (dismissed) {
    return null;
  }

  return (
    <div className="fixed left-1/2 top-4 z-[70] w-[calc(100%-2rem)] max-w-3xl -translate-x-1/2">
      <div className="flex items-start gap-3 rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-amber-950 shadow-xl ring-1 ring-amber-200">
        <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-amber-200 text-sm font-semibold text-amber-900">
          i
        </div>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-semibold">Newer version available</p>
          <p className="mt-0.5 text-sm leading-5 text-amber-900">
            You&apos;re viewing version <span className="font-semibold">v{currentVersion}</span>. A newer version{" "}
            <span className="font-semibold">(v{latestVersion})</span> is available.
          </p>
          <a
            href={latestHref}
            className="mt-2 inline-flex items-center rounded-md bg-amber-600 px-3 py-1.5 text-[13px] font-medium text-white shadow-sm transition hover:bg-amber-700"
          >
            View latest →
          </a>
        </div>
        <button
          type="button"
          onClick={() => setDismissed(true)}
          aria-label="dismiss newer version banner"
          className="-mt-1 rounded p-1 text-amber-700 transition hover:bg-amber-100 hover:text-amber-950"
        >
          ×
        </button>
      </div>
    </div>
  );
}
