"use client";

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import { normalizeSlug } from "@/lib/newartifact";

// Shown when the slug a draft wants is already taken. Creating here must never
// quietly become "add v2 to somebody else's document", so this dialog has no
// "version it anyway" path: pick a different slug, or cancel.
//
// Reached from two places that must agree: the advisory check when the slug
// field blurs, and the authoritative 409 from the create POST's ensure_new. The
// second is the one that actually protects the data (the first is access-
// filtered and races), so both land here rather than in separate messages.
export default function SlugConflictModal({
  slug,
  suggestion,
  existingVersion,
  existingCreator,
  unreadable = false,
  onUse,
  onCancel,
}: {
  slug: string;
  // Pre-filled alternative (the taken slug plus a -yymmdd-hhmm stamp).
  suggestion: string;
  existingVersion?: number | null;
  existingCreator?: string | null;
  // The slug is taken by something this caller can't see. Say so, rather than
  // implying they could go and open it.
  unreadable?: boolean;
  onUse: (slug: string) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState(suggestion);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const normalized = normalizeSlug(draft);
  // Blocking the taken slug is the point of the dialog; an empty slug would
  // silently create an unversionable artifact, which is not what someone who
  // just chose a slug is asking for.
  const invalid = normalized === "" || normalized === slug;

  const submit = () => {
    if (invalid) return;
    onUse(normalized);
  };

  const body = (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto bg-black/40 p-4 backdrop-blur-sm"
      onMouseDown={onCancel}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Slug already exists"
        className="w-full max-w-md rounded-xl border border-neutral-200 bg-white shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="border-b border-neutral-100 px-5 py-3">
          <h2 className="text-sm font-semibold text-neutral-800">That slug is taken</h2>
        </div>

        <div className="px-5 py-3">
          <p className="text-[12px] leading-snug text-neutral-700">
            <span className="font-mono text-neutral-900">s/{slug}</span> already exists
            {existingVersion != null ? <> (latest is v{existingVersion})</> : null}
            {existingCreator ? (
              <>
                {" "}
                — created by <span className="text-neutral-600">{existingCreator}</span>
              </>
            ) : null}
            . Pick a different slug for this new document.
          </p>
          {unreadable ? (
            <p className="mt-1.5 text-[11px] leading-snug text-neutral-500">
              It belongs to someone else and isn&apos;t shared with you, so you can&apos;t open or
              version it — only the name is taken.
            </p>
          ) : (
            <p className="mt-1.5 text-[11px] leading-snug text-neutral-500">
              To add a new version to that existing document instead, open it and use{" "}
              <span className="font-medium">Edit</span>.
            </p>
          )}

          <label className="mt-3 block">
            <span className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">slug</span>
            <input
              ref={inputRef}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  submit();
                }
              }}
              className="mt-1 w-full rounded-md border border-neutral-200 px-2.5 py-1.5 font-mono text-[13px] text-neutral-800 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
              aria-label="replacement slug"
            />
          </label>
          {normalized !== draft.trim() && normalized !== "" ? (
            <p className="mt-1 text-[11px] text-neutral-500">
              will be saved as <span className="font-mono">{normalized}</span>
            </p>
          ) : null}
          {normalized === slug ? (
            <p className="mt-1 text-[11px] text-rose-600">That&apos;s the slug that&apos;s already taken.</p>
          ) : null}
        </div>

        <div className="flex items-center justify-end gap-2 border-t border-neutral-100 px-5 py-3">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={invalid}
            className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700 disabled:opacity-50"
          >
            Use this slug
          </button>
        </div>
      </div>
    </div>
  );

  // Guard for non-DOM render contexts (SSR / the node-env test runner), where
  // there's no document to portal into — render the tree inline instead.
  if (typeof document === "undefined") return body;
  return createPortal(body, document.body);
}
