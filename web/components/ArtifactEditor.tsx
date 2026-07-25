"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import type { ArtifactInfo } from "@/lib/types";
import { createArtifact, latestVersionForSlug } from "@/lib/arti";
import { saveAsNewVersionInput, saveConfirmMessage } from "@/lib/edit";

// ArtifactEditor — the editable raw-source view entered via the ⋯ menu's
// "Edit" item (TEXT artifacts with a slug only; see isEditableArtifact).
// Replaces the body area with a textarea holding the stored source verbatim;
// Save POSTs the edited text as a NEW version of the slug (the server
// assigns MAX(version)+1 and inherits labels/scopes/access from the previous
// version), then navigates to it. The viewed version is never mutated.
export default function ArtifactEditor({
  info,
  body,
  onClose,
}: {
  info: ArtifactInfo;
  body: string;
  onClose: () => void;
}) {
  const router = useRouter();
  const [text, setText] = useState(body);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string>("");
  const taRef = useRef<HTMLTextAreaElement>(null);
  const slug = info.named_slug ?? "";

  // Advisory next-version probe — same best-effort semantics as the upload
  // modal's "uploads as v{N+1}" hint (access-filtered, so it can undercount);
  // the server assigns the real number on save.
  const [latest, setLatest] = useState<number | null>(null);
  useEffect(() => {
    let cancelled = false;
    latestVersionForSlug(slug)
      .then((v) => {
        if (!cancelled) setLatest(v);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [slug]);

  useEffect(() => {
    taRef.current?.focus();
  }, []);

  const dirty = text !== body;

  // Guard dirty edits against tab-close / hard navigation. Client-side
  // route changes can't be intercepted in the app router — the editor is
  // instead keyed by artifact_id at its mount site so a navigation can
  // never carry one artifact's draft onto another.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);
  const nextV = latest != null ? latest + 1 : null;
  // Editing an older version: saving still creates v(latest+1) — FROM this
  // older content — so newer versions get superseded. Warn loudly.
  const editingStale = info.version != null && latest != null && latest !== info.version;

  const cancel = () => {
    if (dirty && !window.confirm("Discard your edits?")) return;
    onClose();
  };

  const save = async () => {
    if (!dirty || saving) return;
    if (!window.confirm(saveConfirmMessage(slug, info.version, latest))) return;
    setSaving(true);
    setErr("");
    try {
      const created = await createArtifact(saveAsNewVersionInput(info, text));
      onClose();
      router.push(created.version != null ? `/s/${slug}/${created.version}` : `/a/${created.artifact_id}`);
      router.refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setSaving(false);
    }
  };

  return (
    <div>
      {/* Editor bar — what saving will do, plus Cancel/Save. */}
      <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-blue-200 bg-blue-50/60 px-3 py-2">
        <svg
          width="13"
          height="13"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
          className="shrink-0 text-blue-500"
        >
          <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
        </svg>
        <span className="text-[12px] text-neutral-700">
          Editing <span className="font-mono">s/{slug}</span>
          <span className="text-neutral-500">
            {" — "}saving creates {nextV != null ? `v${nextV}` : "a new version"};{" "}
            {info.version != null ? `v${info.version}` : "the current version"} stays unchanged.
          </span>
        </span>
        {editingStale ? (
          <span className="text-[12px] font-medium text-amber-700">
            You are editing v{info.version} — latest is v{latest}.
          </span>
        ) : null}
        <span className="ml-auto flex items-center gap-2">
          {err ? <span className="text-[11px] text-rose-600">error: {err}</span> : null}
          <button
            type="button"
            onClick={cancel}
            disabled={saving}
            className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={save}
            disabled={!dirty || saving}
            title={dirty ? "save as a new version" : "no changes yet"}
            className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700 disabled:opacity-50"
          >
            {saving ? "Saving…" : nextV != null ? `Save as v${nextV}` : "Save new version"}
          </button>
        </span>
      </div>
      <textarea
        ref={taRef}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            cancel();
          }
        }}
        disabled={saving}
        spellCheck={false}
        aria-label="artifact source"
        className="min-h-[65vh] w-full resize-y whitespace-pre-wrap break-words rounded-md border border-neutral-200 bg-neutral-50 px-6 py-5 font-mono text-[12px] leading-relaxed text-neutral-800 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
      />
    </div>
  );
}
