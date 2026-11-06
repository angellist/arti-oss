"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import type { ArtifactInfo } from "@/lib/types";
import { createArtifact, latestVersionForSlug } from "@/lib/arti";
import { saveConfirmMessage } from "@/lib/edit";
import { useExternalLinkMessage } from "@/lib/useExternalLinkMessage";
import {
  HTML_EDIT_FRAME_SOURCE,
  HTML_EDIT_HOST_SOURCE,
  applyHtmlTextEdits,
  htmlEditErrorMessage,
  htmlEditFrameSrc,
  htmlEditSaveInput,
  parseFrameEdits,
  type HtmlTextEdit,
} from "@/lib/htmlEdit";

type Mode = "edit" | "view";

// HtmlTextEditor edits the text of a rendered HTML artifact in place. The page
// keeps its own scripts and layout; Edit/Preview toggles typing on and off
// without reloading it, so edits survive switching back and forth. Save
// publishes one new version with every changed block.
export default function HtmlTextEditor({
  info,
  onClose,
  onEditSource,
  zoom = 1,
}: {
  info: ArtifactInfo;
  onClose: () => void;
  onEditSource: () => void;
  zoom?: number;
}) {
  const router = useRouter();
  const frameRef = useRef<HTMLIFrameElement>(null);
  useExternalLinkMessage(frameRef);
  const [mode, setMode] = useState<Mode>("edit");
  const modeRef = useRef<Mode>("edit");
  const [ready, setReady] = useState(false);
  const [editable, setEditable] = useState<number | null>(null);
  const [dirty, setDirty] = useState(0);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  const [latest, setLatest] = useState<number | null>(null);
  const pending = useRef(new Map<string, (edits: unknown) => void>());
  const slug = info.named_slug ?? "";

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

  const post = useCallback((msg: Record<string, unknown>) => {
    frameRef.current?.contentWindow?.postMessage({ ...msg, source: HTML_EDIT_HOST_SOURCE }, "*");
  }, []);

  useEffect(() => {
    const requests = pending.current;
    const onMessage = (e: MessageEvent) => {
      const frame = frameRef.current?.contentWindow;
      if (!frame || e.source !== frame) return;
      const d = e.data as Record<string, unknown> | null;
      if (!d || d.source !== HTML_EDIT_FRAME_SOURCE) return;
      switch (d.type) {
        case "ready":
          setReady(true);
          post({ type: "mode", editing: modeRef.current === "edit" });
          break;
        case "mode":
          if (typeof d.editable === "number") setEditable(d.editable);
          break;
        case "changed":
          if (typeof d.count === "number" && d.count >= 0) setDirty(d.count);
          break;
        case "edits": {
          const resolve = typeof d.requestId === "string" ? requests.get(d.requestId) : undefined;
          if (resolve) resolve(d.edits);
          break;
        }
      }
    };
    window.addEventListener("message", onMessage);
    // The frame also announces itself on load; this covers a load that finished first.
    post({ type: "hello" });
    return () => {
      window.removeEventListener("message", onMessage);
      requests.clear();
    };
  }, [post]);

  useEffect(() => {
    if (dirty === 0) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const switchMode = (next: Mode) => {
    modeRef.current = next;
    setMode(next);
    post({ type: "mode", editing: next === "edit" });
  };

  const collect = () =>
    new Promise<HtmlTextEdit[]>((resolve, reject) => {
      const requestId = Math.random().toString(36).slice(2);
      const timer = window.setTimeout(() => {
        pending.current.delete(requestId);
        reject(new Error("The page did not return its edits. Reload and try again."));
      }, 10_000);
      pending.current.set(requestId, (raw) => {
        window.clearTimeout(timer);
        pending.current.delete(requestId);
        const edits = parseFrameEdits(raw);
        if (edits) resolve(edits);
        else reject(new Error("The page returned an invalid edit list."));
      });
      post({ type: "collect", requestId });
    });

  const discardOk = () => dirty === 0 || window.confirm("Discard your text edits?");

  const cancel = () => {
    if (discardOk()) onClose();
  };

  const editSource = () => {
    if (discardOk()) onEditSource();
  };

  const nextV = latest != null ? latest + 1 : null;
  // Save pins the edited version, so editing an older one can only end in 409.
  const editingStale = info.version != null && latest != null && latest !== info.version;

  const save = async () => {
    if (saving || dirty === 0 || editingStale) return;
    setErr("");
    setSaving(true);
    try {
      const edits = await collect();
      if (edits.length === 0) {
        setSaving(false);
        return;
      }
      if (!window.confirm(saveConfirmMessage(slug, info.version, latest))) {
        setSaving(false);
        return;
      }
      const applied = await applyHtmlTextEdits(info.artifact_id, edits);
      if (applied.rejected.length > 0) {
        post({ type: "rejected", eids: applied.rejected });
        const kept = edits.length - applied.rejected.length;
        const ok = window.confirm(
          `${applied.rejected.length} of ${edits.length} edited blocks can't be saved: their text is produced by the page's script, not stored in it. They are outlined in red.\n\nSave the other ${kept}?`,
        );
        if (!ok) {
          setSaving(false);
          return;
        }
      }
      const created = await createArtifact(htmlEditSaveInput(info, applied.bytes));
      setDirty(0);
      onClose();
      router.push(created.version != null ? `/s/${slug}/${created.version}` : `/a/${created.artifact_id}`);
      router.refresh();
    } catch (e) {
      setErr(htmlEditErrorMessage(e));
      setSaving(false);
    }
  };

  const status = !ready
    ? "Loading page…"
    : editable === 0
      ? "No editable text found on this page."
      : dirty > 0
        ? `${dirty} changed block${dirty === 1 ? "" : "s"}`
        : mode === "edit"
          ? "Click any outlined text to edit it."
          : "Preview: the page works as normal; your edits are kept.";

  const segment = (m: Mode, label: string) => (
    <button
      type="button"
      onClick={() => switchMode(m)}
      aria-pressed={mode === m}
      disabled={saving}
      className={`px-3 py-1 text-xs transition disabled:opacity-50 ${
        mode === m ? "bg-blue-600 font-medium text-white" : "bg-white text-neutral-700 hover:bg-neutral-50"
      }`}
    >
      {label}
    </button>
  );

  return (
    <div>
      <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-blue-200 bg-blue-50/60 px-3 py-2">
        <span className="inline-flex overflow-hidden rounded-md border border-neutral-200" role="group" aria-label="editor mode">
          {segment("edit", "Edit text")}
          {segment("view", "Preview")}
        </span>
        <span className="text-[12px] text-neutral-700">
          <span className="font-mono">s/{slug}</span>
          <span className="text-neutral-500">
            {" · "}saving creates {nextV != null ? `v${nextV}` : "a new version"}
          </span>
        </span>
        <span className="text-[12px] text-neutral-500" aria-live="polite">
          {status}
        </span>
        {editingStale ? (
          <span className="text-[12px] font-medium text-amber-700">
            This is v{info.version}; only the latest version can be edited here.{" "}
            <a href={`/s/${slug}/${latest}`} className="underline underline-offset-2">
              Open v{latest}
            </a>
          </span>
        ) : null}
        <span className="ml-auto flex items-center gap-2">
          {err ? <span className="text-[11px] text-rose-600">error: {err}</span> : null}
          <button
            type="button"
            onClick={editSource}
            disabled={saving}
            className="text-xs text-neutral-500 underline-offset-2 hover:text-neutral-800 hover:underline disabled:opacity-50"
          >
            Edit source
          </button>
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
            disabled={dirty === 0 || saving || editingStale}
            title={editingStale ? "open the latest version to edit" : dirty > 0 ? "save as a new version" : "no changes yet"}
            className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700 disabled:opacity-50"
          >
            {saving ? "Saving…" : nextV != null ? `Save as v${nextV}` : "Save new version"}
          </button>
        </span>
      </div>
      <div className="h-[calc(100vh-180px)] w-full overflow-hidden rounded border border-neutral-200 bg-white">
        <iframe
          ref={frameRef}
          src={htmlEditFrameSrc(info.artifact_id)}
          sandbox="allow-scripts allow-downloads"
          title="editable artifact content"
          onLoad={() => post({ type: "hello" })}
          className="block h-full w-full border-0 bg-white"
          style={zoom === 1 ? undefined : { zoom }}
        />
      </div>
    </div>
  );
}
