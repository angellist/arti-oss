"use client";

import { useState } from "react";

import { KINDS, type NewKind } from "@/lib/newartifact";
import AccessModal from "./AccessModal";
import { DraftLabelEditor, DraftScopeEditor } from "./ChipEditors";
import { WidthControl, type Width } from "./ViewerToolbar";

// DraftHeader is the top of a "New artifact" page. It deliberately mirrors the
// VIEWER header's information architecture — title, then identifiers, then
// labels/scopes, then controls — so the page you create in and the page you read
// in are recognisably the same object.
//
// What it does NOT copy is the viewer's click-to-edit-in-place affordance. In
// the viewer, editing the title commits a real PATCH the moment you leave the
// field. Here nothing is written until Create, so the text fields are persistent
// input boxes rather than click-to-reveal: no control on this page may look like
// it confirmed anything. There is exactly one commit, and it is Create.
//
// Labels and scopes are the exception, and deliberately so — they use the
// viewer's own chip presentation (bare chips + a "+"). A chip is a value, not a
// text field, so it can't imply a save the way a filled-in box can.

// accessSummary renders the draft's read-access as one short phrase for the
// button face, using the same tiering the viewer's AccessButton shows.
function accessSummary(access: string[]): { label: string; dot: string } {
  if (access.length === 0) return { label: "private", dot: "bg-rose-500" };
  if (access.includes("*")) return { label: "anyone signed in", dot: "bg-emerald-400" };
  const allDomain = access.every((p) => /^\*@[^*?]+$/.test(p));
  if (allDomain) return { label: access.map((p) => p.slice(2)).join(", "), dot: "bg-amber-400" };
  return {
    label: access.length === 1 ? access[0] : `${access.length} entries`,
    dot: "bg-rose-500",
  };
}

export default function DraftHeader({
  kind,
  title,
  onTitle,
  slug,
  onSlug,
  onSlugBlur,
  resolvedSlug,
  labels,
  onLabels,
  scopes,
  onScopes,
  access,
  write,
  onAccess,
  width,
  setWidth,
  showWidth,
  creating,
  error,
  onCreate,
  onCancel,
}: {
  kind: NewKind;
  title: string;
  onTitle: (v: string) => void;
  // The raw contents of the slug box. Empty means "follow the title / default";
  // resolvedSlug is what will actually be created.
  slug: string;
  onSlug: (v: string) => void;
  onSlugBlur: () => void;
  resolvedSlug: string;
  labels: string[];
  onLabels: (v: string[]) => void;
  scopes: string[];
  onScopes: (v: string[]) => void;
  access: string[];
  write: string[] | null;
  // write is null in mirror mode (write follows read) — a meaningfully
  // different state from an explicit list, so it must survive as null.
  onAccess: (access: string[], write: string[] | null) => void;
  width: Width;
  setWidth: (w: Width) => void;
  showWidth: boolean;
  creating: boolean;
  error: string;
  onCreate: () => void;
  onCancel: () => void;
}) {
  const [accessOpen, setAccessOpen] = useState(false);
  const spec = KINDS[kind];
  const summary = accessSummary(access);

  return (
    <div className="border-b border-neutral-200 bg-neutral-50/80">
      <div className="mx-auto max-w-[1400px] px-6 pt-4 pb-3">
        {/* Row 1 — title. An input, not an h1 you click: nothing here is saved
            until Create, and a rename-in-place affordance would imply it was. */}
        <div className="flex flex-wrap items-start gap-3">
          <input
            value={title}
            onChange={(e) => onTitle(e.target.value)}
            disabled={creating}
            aria-label="title"
            placeholder={spec.defaultTitle}
            className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-base font-semibold text-neutral-900 placeholder:font-normal placeholder:text-neutral-400 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200 disabled:opacity-60"
          />
          <span className="flex shrink-0 items-center gap-2">
            <button
              type="button"
              onClick={onCancel}
              disabled={creating}
              className="rounded-md border border-neutral-200 bg-white px-3 py-1.5 text-xs text-neutral-700 transition hover:bg-neutral-50 disabled:opacity-50"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={onCreate}
              disabled={creating}
              className="rounded-md bg-blue-600 px-4 py-1.5 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700 disabled:opacity-50"
            >
              {creating ? "Creating…" : "Create"}
            </button>
          </span>
        </div>

        {/* Row 2 — the identifiers, mirroring the viewer's type + slug row. The
            content type is shown, not chosen: it follows from the kind. */}
        <div className="mt-2 flex flex-wrap items-center gap-2 text-[11px] text-neutral-500">
          <span className="rounded bg-neutral-100 px-2 py-0.5 text-neutral-600">{spec.artifactType}</span>
          <span className="font-mono">{spec.contentType}</span>
          <label className="flex items-center gap-1">
            <span className="shrink-0 font-mono text-neutral-400">s/</span>
            {/* Sized to the slug, not the row: a slug is a short identifier, and
                a full-width box implied it wanted a sentence. */}
            <input
              value={slug}
              onChange={(e) => onSlug(e.target.value)}
              onBlur={onSlugBlur}
              disabled={creating}
              aria-label="slug"
              placeholder={resolvedSlug}
              className="w-[30ch] max-w-full rounded-md border border-neutral-200 bg-white px-2 py-1 font-mono text-[12px] text-neutral-800 placeholder:text-neutral-400 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200 disabled:opacity-60"
            />
          </label>
        </div>

        {/* Row 3 — labels then scopes, presented exactly as the viewer does:
            bare chips plus a "+" affordance, no input-box chrome. Same
            ChipEditor component, bound to draft state instead of a PATCH. */}
        <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[11px]">
          <div className="flex flex-wrap items-center gap-1.5">
            <DraftLabelEditor labels={labels} onChange={onLabels} />
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <DraftScopeEditor scopes={scopes} onChange={onScopes} />
          </div>
        </div>

        {/* Row 4 — view controls + access, matching the viewer's controls row. */}
        <div className="mt-2.5 flex flex-wrap items-center gap-3">
          {showWidth ? <WidthControl width={width} setWidth={setWidth} /> : null}
          <span className="ml-auto" />
          <span className="text-[11px] text-neutral-500">
            {resolvedSlug ? (
              <>
                creates <span className="font-mono text-neutral-700">s/{resolvedSlug}</span>
              </>
            ) : (
              "no slug — this artifact won't be versionable"
            )}
          </span>
          <button
            type="button"
            onClick={() => setAccessOpen(true)}
            aria-haspopup="dialog"
            disabled={creating}
            title="who will be able to read this once it's created"
            className="inline-flex items-center gap-1.5 rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50 disabled:opacity-50"
          >
            <span className={`inline-block h-1.5 w-1.5 rounded-full ${summary.dot}`} aria-hidden="true" />
            Access
            <span className="text-neutral-400">· {summary.label}</span>
          </button>
        </div>

        {error ? <div className="mt-2 text-[11px] text-rose-600">error: {error}</div> : null}
      </div>

      {accessOpen ? (
        <AccessModal
          draft
          access={access}
          write={write}
          hasOtherVersions={false}
          canEdit
          onCommit={onAccess}
          onClose={() => setAccessOpen(false)}
          onSaved={() => {}}
        />
      ) : null}
    </div>
  );
}
