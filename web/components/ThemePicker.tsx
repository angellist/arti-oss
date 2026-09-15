"use client";

// The theme picker. Selecting applies immediately (the click is the
// confirmation) and persists to localStorage — there is nothing to save and no
// server call. Each card previews itself by carrying its own data-theme, so a
// new choice needs no preview asset.

import { useSyncExternalStore } from "react";
import { SYSTEM_THEME, THEMES, themePref } from "@/lib/appearance";

export default function ThemePicker() {
  // Read from storage, not from local state, so the picker always agrees with
  // what the pre-paint script put on <html> — and with another tab.
  const current = useSyncExternalStore(themePref.subscribe, themePref.read, () => themePref.fallback);

  return (
    <div role="radiogroup" aria-label="Theme" className="grid max-w-2xl grid-cols-[repeat(auto-fill,minmax(170px,1fr))] gap-2">
      {THEMES.map((t) => (
        <button
          key={t.id}
          type="button"
          role="radio"
          aria-checked={current === t.id}
          onClick={() => {
            themePref.apply(t.id);
            themePref.store(t.id);
          }}
          className={
            "overflow-hidden rounded-md border text-left " +
            (current === t.id
              ? "border-blue-500 ring-1 ring-blue-500"
              : "border-neutral-200 hover:border-neutral-300")
          }
        >
          {/* The swatch reads the theme's own tokens instead of repeating its
              colours here, so the two cannot drift apart. System carries no
              attribute, so it previews whatever the OS currently resolves to. */}
          <div
            data-theme={t.id === SYSTEM_THEME ? undefined : t.id}
            aria-hidden="true"
            className="space-y-1.5 bg-white p-2"
          >
            <div className="flex gap-1.5">
              <div className="h-8 w-6 rounded-sm bg-neutral-100" />
              <div className="flex-1 space-y-1 py-0.5">
                <div className="h-1.5 w-full rounded-full bg-neutral-800" />
                <div className="h-1.5 w-3/4 rounded-full bg-neutral-400" />
                <div className="h-1.5 w-1/2 rounded-full bg-neutral-300" />
              </div>
            </div>
            <div className="flex items-center gap-1">
              <span className="rounded-sm bg-blue-100 px-1 text-[9px] text-blue-700">latest</span>
              <span className="rounded-sm bg-amber-100 px-1 text-[9px] text-amber-800">draft</span>
            </div>
          </div>
          <div className="border-t border-neutral-200 px-2 py-1.5">
            <div className="text-[13px] font-medium text-neutral-900">{t.label}</div>
            <div className="text-[11px] text-neutral-500">{t.blurb}</div>
          </div>
        </button>
      ))}
    </div>
  );
}
