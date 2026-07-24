"use client";

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useRouter } from "next/navigation";
import {
  createArtifact,
  getAggregates,
  latestVersionForSlug,
  suggestMetadata,
  ArtiError,
} from "@/lib/arti";
import type { ArtifactType } from "@/lib/types";
import {
  bundleFiles,
  bytesToBase64,
  detectType,
  guessContentType,
  humanBytes,
  isTextualContentType,
  slugFromFilename,
  textSample,
  titleFromFilename,
} from "@/lib/upload";
import ChipInput from "./ChipInput";

// A file's type is fixed by its shape, so we never offer a type the server
// would reject:
//   single non-zip, textual content  → TEXT only (never ATTACHMENT)
//   single non-zip, binary content   → ATTACHMENT only (never TEXT)
//   zip                              → PACKAGE or APP (real choice)
const ZIP_TYPES: ArtifactType[] = ["PACKAGE", "APP"];

// Short rationale shown under the type chips so a user understands why a
// type was auto-selected (and what overriding implies).
const TYPE_HINT: Record<ArtifactType, string> = {
  TEXT: "A single text document, shown in the viewer.",
  PACKAGE: "A zip of multiple files, browsable in the package viewer.",
  APP: "A zip containing arti-app.json — served as a live app.",
  ATTACHMENT: "A single binary file (download only); no slug.",
};

export default function UploadModal({
  onClose,
  initialFile,
}: {
  onClose: () => void;
  initialFile?: File | null;
}) {
  const router = useRouter();

  const [file, setFile] = useState<File | null>(null);
  const [buf, setBuf] = useState<ArrayBuffer | null>(null);
  const [dragOver, setDragOver] = useState(false);

  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [artifactType, setArtifactType] = useState<ArtifactType>("TEXT");
  const [isZip, setIsZip] = useState(false);
  const [contentType, setContentType] = useState("");
  const [scopes, setScopes] = useState<string[]>([]);
  const [labels, setLabels] = useState<string[]>([]);
  // Mirror the chip sets in refs so onSubmit reads the latest values even
  // when a ChipInput commits pending typed text on blur during the very
  // click that triggers Upload (the state update isn't visible to the
  // submit closure yet, but the ref write is synchronous).
  const scopesRef = useRef<string[]>([]);
  const labelsRef = useRef<string[]>([]);
  const commitScopes = (next: string[]) => {
    scopesRef.current = next;
    setScopes(next);
  };
  const commitLabels = (next: string[]) => {
    labelsRef.current = next;
    setLabels(next);
  };

  const [slugVersion, setSlugVersion] = useState<number | null>(null);
  const [scopeSug, setScopeSug] = useState<string[]>([]);
  const [labelSug, setLabelSug] = useState<string[]>([]);

  const [autofilling, setAutofilling] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Which type chips are valid for the current file, and the type that will
  // actually be submitted. Non-zip is fully determined by textual-ness;
  // only a zip offers a real PACKAGE/APP choice.
  const allowedTypes: ArtifactType[] = isZip
    ? ZIP_TYPES
    : [isTextualContentType(contentType) ? "TEXT" : "ATTACHMENT"];
  const effectiveType: ArtifactType = isZip
    ? ZIP_TYPES.includes(artifactType)
      ? artifactType
      : "PACKAGE"
    : allowedTypes[0];

  // Suggestions for the scope/label typeaheads.
  useEffect(() => {
    getAggregates()
      .then((a) => {
        setScopeSug(a.scopes.map((s) => s.scope));
        setLabelSug(a.labels.map((l) => l.label));
      })
      .catch(() => {
        /* suggestions are optional */
      });
  }, []);

  // Escape closes (unless mid-upload).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);

  // Debounced slug-existence check → "uploads as v{N+1}".
  useEffect(() => {
    const s = slug.trim();
    if (!s) {
      setSlugVersion(null);
      return;
    }
    const t = setTimeout(() => {
      latestVersionForSlug(s)
        .then(setSlugVersion)
        .catch(() => setSlugVersion(null));
    }, 350);
    return () => clearTimeout(t);
  }, [slug]);

  const onPick = async (f: File) => {
    setError(null);
    const ab = await f.arrayBuffer();
    const d = detectType(f, ab);
    setFile(f);
    setBuf(ab);
    setIsZip(d.artifactType === "PACKAGE" || d.artifactType === "APP");
    setArtifactType(d.artifactType);
    setContentType(d.contentType);
    // Sensible immediate defaults; the user can refine or hit auto-fill.
    setTitle((cur) => cur || titleFromFilename(f.name));
    setSlug((cur) => cur || slugFromFilename(f.name));
  };

  // When opened via a page drop, pre-fill from the dropped file on mount.
  useEffect(() => {
    if (initialFile) void onPick(initialFile);
    // Run once for the file the modal was opened with.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const onAutofill = async () => {
    if (!file || !buf) return;
    setAutofilling(true);
    setError(null);
    try {
      const textual = isTextualContentType(contentType);
      const res = await suggestMetadata({
        filename: file.name,
        content_type: contentType,
        artifact_type: artifactType,
        sample: textual ? textSample(buf, 1000) : "",
      });
      if (res.title) setTitle(res.title);
      if (res.slug) setSlug(res.slug);
      if (res.labels?.length) commitLabels(Array.from(new Set([...labelsRef.current, ...res.labels])));
    } catch (e) {
      setError(e instanceof Error ? e.message : "auto-fill failed");
    } finally {
      setAutofilling(false);
    }
  };

  const onSubmit = async () => {
    if (!file || !buf) {
      setError("Choose a file to upload.");
      return;
    }
    if (!title.trim()) {
      setError("Title is required.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const info = await createArtifact({
        title: title.trim(),
        content_type: contentType,
        artifact_type: effectiveType,
        content_base64: bytesToBase64(buf),
        named_slug: slug.trim() || undefined,
        scopes: scopesRef.current,
        labels: labelsRef.current,
      });
      const dest = info.named_slug
        ? `/s/${info.named_slug}` + (info.version ? `/${info.version}` : "")
        : `/a/${info.artifact_id}`;
      router.push(dest);
      onClose();
    } catch (e) {
      if (e instanceof ArtiError) setError(`${e.message} (HTTP ${e.status})`);
      else setError(e instanceof Error ? e.message : "upload failed");
      setBusy(false);
    }
  };

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault();
    setDragOver(false);
    const fs = e.dataTransfer.files ? Array.from(e.dataTransfer.files) : [];
    // 2+ files get zipped into a flat PACKAGE; a single file is used as-is.
    if (fs.length) void bundleFiles(fs).then(onPick);
  };

  const fileInputRef = useRef<HTMLInputElement>(null);

  const body = (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/40 p-4 py-10 backdrop-blur-sm"
      onMouseDown={() => {
        if (!busy) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Upload artifact"
        className="w-full max-w-lg rounded-xl border border-neutral-200 bg-white shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        {/* header */}
        <div className="flex items-center justify-between border-b border-neutral-100 px-5 py-3">
          <h2 className="text-[15px] font-semibold text-neutral-900">Upload artifact</h2>
          <button
            type="button"
            onClick={() => !busy && onClose()}
            aria-label="close"
            className="rounded p-1 text-neutral-400 hover:bg-neutral-100 hover:text-neutral-700"
          >
            ✕
          </button>
        </div>

        <div className="space-y-4 px-5 py-4">
          {/* drop zone / file chip */}
          <div
            onDragOver={(e) => {
              e.preventDefault();
              setDragOver(true);
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={onDrop}
            onClick={() => fileInputRef.current?.click()}
            className={
              "cursor-pointer rounded-lg border-2 border-dashed px-4 py-6 text-center transition " +
              (dragOver
                ? "border-blue-400 bg-blue-50"
                : "border-neutral-300 bg-neutral-50 hover:border-neutral-400")
            }
          >
            <input
              ref={fileInputRef}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                const fs = e.target.files ? Array.from(e.target.files) : [];
                if (fs.length) void bundleFiles(fs).then(onPick);
              }}
            />
            {file ? (
              <div className="text-[13px]">
                <div className="font-medium text-neutral-900">{file.name}</div>
                <div className="mt-0.5 text-neutral-500">
                  {humanBytes(file.size)} · detected{" "}
                  <span className="font-mono text-neutral-700">{artifactType}</span>
                </div>
                <div className="mt-1 text-[11px] text-blue-600">click or drop to replace</div>
              </div>
            ) : (
              <div className="text-[13px] text-neutral-500">
                <div className="font-medium text-neutral-700">Drag &amp; drop a file here</div>
                <div className="mt-0.5">or click to choose — drop several to bundle them into a package</div>
              </div>
            )}
          </div>

          {file ? (
            <>
              {/* title + auto-fill */}
              <div>
                <div className="mb-1 flex items-center justify-between">
                  <label className="text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                    title
                  </label>
                  <button
                    type="button"
                    onClick={onAutofill}
                    disabled={autofilling}
                    title="Use the built-in LLM to suggest title, slug, and labels from the content"
                    className="rounded-full border border-neutral-200 px-2.5 py-0.5 text-[11px] text-neutral-600 hover:bg-neutral-50 disabled:opacity-50"
                  >
                    {autofilling ? "✨ thinking…" : "✨ Auto-fill metadata"}
                  </button>
                </div>
                <input
                  value={title}
                  onChange={(e) => setTitle(e.target.value)}
                  placeholder="A human-readable title"
                  className="w-full rounded-md border border-neutral-200 px-3 py-1.5 text-[13px] text-neutral-900 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
                />
              </div>

              {/* slug */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  slug <span className="font-normal normal-case text-neutral-400">(optional)</span>
                </label>
                <input
                  value={slug}
                  onChange={(e) => setSlug(e.target.value)}
                  placeholder="kebab-case-name"
                  className="w-full rounded-md border border-neutral-200 px-3 py-1.5 font-mono text-[12px] text-neutral-900 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
                />
                {slugVersion !== null ? (
                  <p className="mt-1 text-[11px] text-amber-600">
                    slug exists (latest v{slugVersion}) — this uploads as{" "}
                    <span className="font-semibold">v{slugVersion + 1}</span>
                  </p>
                ) : slug.trim() ? (
                  <p className="mt-1 text-[11px] text-neutral-400">new slug — uploads as v1</p>
                ) : null}
              </div>

              {/* type chips */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  type
                </label>
                <div className="flex flex-wrap gap-1.5">
                  {allowedTypes.map((t) => (
                    <button
                      key={t}
                      type="button"
                      onClick={() => setArtifactType(t)}
                      disabled={allowedTypes.length === 1}
                      className={
                        "rounded-full px-2.5 py-0.5 text-[11px] transition " +
                        (effectiveType === t
                          ? "bg-blue-600 text-white"
                          : "border border-neutral-200 text-neutral-600 hover:bg-neutral-50") +
                        (allowedTypes.length === 1 ? " cursor-default" : "")
                      }
                    >
                      {t}
                    </button>
                  ))}
                </div>
                <p className="mt-1 text-[11px] text-neutral-400">{TYPE_HINT[effectiveType]}</p>
              </div>

              {/* content type */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  content type <span className="font-normal normal-case text-neutral-400">(auto-detected)</span>
                </label>
                <input
                  value={contentType}
                  onChange={(e) => setContentType(e.target.value)}
                  className="w-full rounded-md border border-neutral-200 px-3 py-1.5 font-mono text-[12px] text-neutral-700 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
                />
              </div>

              {/* scopes */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  scopes
                </label>
                <ChipInput
                  values={scopes}
                  onChange={commitScopes}
                  suggestions={scopeSug}
                  placeholder="add a scope…"
                  noun="scope"
                  chipClass="bg-purple-50 ring-purple-200 text-purple-800"
                />
              </div>

              {/* labels */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  labels
                </label>
                <ChipInput
                  values={labels}
                  onChange={commitLabels}
                  suggestions={labelSug}
                  placeholder="add a label…"
                  noun="label"
                  chipClass="bg-neutral-100 ring-neutral-200 text-neutral-700"
                />
              </div>
            </>
          ) : null}

          {error ? (
            <div className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
              {error}
            </div>
          ) : null}
        </div>

        {/* footer */}
        <div className="flex items-center justify-end gap-2 border-t border-neutral-100 px-5 py-3">
          <button
            type="button"
            onClick={() => !busy && onClose()}
            className="rounded-md px-3 py-1.5 text-[13px] text-neutral-600 hover:bg-neutral-100"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onSubmit}
            disabled={busy || !file || !title.trim()}
            className="rounded-md bg-blue-600 px-4 py-1.5 text-[13px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {busy ? "Uploading…" : "Upload"}
          </button>
        </div>
      </div>
    </div>
  );

  return createPortal(body, document.body);
}
