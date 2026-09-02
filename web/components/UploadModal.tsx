"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useRouter } from "next/navigation";
import {
  createArtifact,
  getAggregates,
  getBySlug,
  latestVersionForSlug,
  suggestMetadata,
  ArtiError,
} from "@/lib/arti";
import type { ArtifactType } from "@/lib/types";
import {
  bundleFiles,
  bytesToBase64,
  detectType,
  humanBytes,
  isTextualContentType,
  sha256Hex,
  slugFromFilename,
  textSample,
  titleFromFilename,
} from "@/lib/upload";
import { classifyTypeChange, type UploadTarget } from "@/lib/upload-target";
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
  ATTACHMENT: "A single binary file, served for download or preview.",
};

// The slug's latest version as read when the modal opened (or re-read after a
// losing a publish race). Everything a version upload inherits or is checked
// against comes from here, never from the version the viewer happens to show.
interface Latest {
  version: number | null;
  artifactType: ArtifactType;
  contentType: string;
  title: string;
  description: string;
  labels: string[];
  scopes: string[];
  sha256: string | null;
}

// LatestRead is either the snapshot or the reason there isn't one. A failed
// read is not fatal: the upload proceeds unpinned, which is what it did before
// this check existed.
interface LatestRead {
  snap?: Latest;
  err?: string;
}

async function fetchLatest(slug: string): Promise<LatestRead> {
  try {
    const info = await getBySlug(slug);
    return {
      snap: {
        version: info.version,
        artifactType: info.artifact_type,
        contentType: info.content_type,
        title: info.title,
        description: info.description ?? "",
        labels: info.labels ?? [],
        scopes: info.scopes ?? [],
        sha256: info.sha256,
      },
    };
  } catch (e) {
    return {
      err:
        `Couldn't re-read s/${slug} (${e instanceof Error ? e.message : "unknown error"}). ` +
        "Publishing without the base-version check.",
    };
  }
}

const TYPE_CHIP = "rounded-full px-2 py-0.5 font-mono text-[11px] leading-4";

// TypeChip renders one side of a type change. Same geometry both sides — the
// "before" and "after" differ only in fill and the strikethrough, so the pair
// reads as one before/after statement rather than two unrelated tokens.
function TypeChip({ value, tone }: { value: string; tone: "from" | "to" }) {
  return (
    <span
      className={
        TYPE_CHIP +
        (tone === "from"
          ? " bg-neutral-100 text-neutral-500 line-through"
          : " bg-amber-100 text-amber-900")
      }
    >
      {value}
    </span>
  );
}

export default function UploadModal({
  onClose,
  initialFile,
  target,
}: {
  onClose: () => void;
  initialFile?: File | null;
  // The document this upload versions. Absent (or switched away from with the
  // mode toggle) means the modal behaves exactly as it always has.
  target?: UploadTarget | null;
}) {
  const router = useRouter();

  const [file, setFile] = useState<File | null>(null);
  const [buf, setBuf] = useState<ArrayBuffer | null>(null);
  const [dragOver, setDragOver] = useState(false);

  const [mode, setMode] = useState<"version" | "new">(target ? "version" : "new");
  const versioning = !!target && mode === "version";

  const [latest, setLatest] = useState<Latest | null>(null);
  // False until the slug read has come back either way. Publishing before it
  // does would send no expected_latest_version — an unpinned publish, and one
  // made before any type-change warning could be shown.
  const [latestRead, setLatestRead] = useState(false);
  // A notice above the form: the slug moved under us, or its latest couldn't
  // be read. Never fatal — the upload still works, minus the base-version pin.
  const [notice, setNotice] = useState<string | null>(null);

  const [title, setTitle] = useState("");
  const [titleTouched, setTitleTouched] = useState(false);
  const [slug, setSlug] = useState("");
  const [artifactType, setArtifactType] = useState<ArtifactType>("TEXT");
  const [isZip, setIsZip] = useState(false);
  const [contentType, setContentType] = useState("");
  const [fileSha, setFileSha] = useState<string | null>(null);
  const [scopes, setScopes] = useState<string[]>([]);
  const [labels, setLabels] = useState<string[]>([]);
  // Untouched chip sets are OMITTED from the POST so the server inherits the
  // slug's own labels/scopes. Sending [] instead — which this modal used to do
  // unconditionally — clears them, so a version uploaded from the web wiped
  // metadata the document had since v1.
  const [scopesTouched, setScopesTouched] = useState(false);
  const [labelsTouched, setLabelsTouched] = useState(false);
  // Mirror the chip sets in refs so onSubmit reads the latest values even
  // when a ChipInput commits pending typed text on blur during the very
  // click that triggers Upload (the state update isn't visible to the
  // submit closure yet, but the ref write is synchronous).
  const scopesRef = useRef<string[]>([]);
  const labelsRef = useRef<string[]>([]);
  const commitScopes = (next: string[]) => {
    scopesRef.current = next;
    setScopes(next);
    setScopesTouched(true);
  };
  const commitLabels = (next: string[]) => {
    labelsRef.current = next;
    setLabels(next);
    setLabelsTouched(true);
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

  // Inherited values, and the fields derived from them. Deriving rather than
  // copying into state means a slug re-read (after a lost race) updates every
  // field the user hasn't edited, with no sync effect to get wrong.
  const inheritedTitle = latest?.title ?? target?.title ?? "";
  const inheritedLabels = latest?.labels ?? target?.labels ?? [];
  const inheritedScopes = latest?.scopes ?? target?.scopes ?? [];
  const inheritedDescription = latest?.description ?? target?.description ?? "";
  const effectiveTitle = titleTouched
    ? title
    : versioning
      ? inheritedTitle
      : title;
  const effectiveSlug = versioning ? (target?.slug ?? "") : slug;
  const effectiveLabels = labelsTouched ? labels : versioning ? inheritedLabels : labels;
  const effectiveScopes = scopesTouched ? scopes : versioning ? inheritedScopes : scopes;

  const latestVersion = versioning ? (latest?.version ?? null) : null;
  const nextVersion = latestVersion != null ? latestVersion + 1 : null;

  // What this upload changes about the document itself. Compared against the
  // slug's latest — the version we are actually publishing on top of.
  const change =
    versioning && file && latest
      ? classifyTypeChange(
          {
            slug: target!.slug,
            artifactType: latest.artifactType,
            contentType: latest.contentType,
          },
          { artifactType: effectiveType, contentType },
        )
      : null;
  // The acknowledgement is keyed to the exact transition it was given for, so
  // replacing the file (or the slug moving under us) re-arms it instead of
  // carrying consent across to a different change.
  const changeKey = change ? `${change.kind}:${change.from}>${change.to}` : "";
  const [ackedKey, setAckedKey] = useState("");
  const acked = !!changeKey && ackedKey === changeKey;
  const needsAck = change?.severity === "hard";

  const sameBytes = !!fileSha && !!latest?.sha256 && fileSha === latest.sha256;
  // Version mode waits for the slug read before it will publish anything.
  const awaitingLatest = versioning && !latestRead;

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

  // Apply a slug read. Split from the fetch so the state writes happen after
  // an await rather than synchronously inside an effect.
  const applyLatest = useCallback((r: LatestRead) => {
    setLatestRead(true);
    if (r.snap) {
      setLatest(r.snap);
      setNotice(null);
    } else {
      setNotice(r.err ?? null);
    }
    return r.snap?.version ?? null;
  }, []);

  const refreshLatest = useCallback(
    async (slugName: string) => applyLatest(await fetchLatest(slugName)),
    [applyLatest],
  );

  // Read the slug's CURRENT latest version on open. The viewer may be showing
  // an older one, and someone may have published since the page loaded, so
  // every inherited value and the base-version pin come from this read — not
  // from the payload the page was rendered with. `alive` drops a response that
  // lands after the modal closed.
  useEffect(() => {
    if (!target) return;
    let alive = true;
    void (async () => {
      const r = await fetchLatest(target.slug);
      if (alive) applyLatest(r);
    })();
    return () => {
      alive = false;
    };
  }, [target, applyLatest]);

  // Debounced slug-existence check → "uploads as v{N+1}". New-document mode
  // only: in version mode the slug is fixed and `latest` already has the real
  // number, from an unfiltered read.
  useEffect(() => {
    const s = slug.trim();
    if (versioning || !s) {
      setSlugVersion(null);
      return;
    }
    const t = setTimeout(() => {
      latestVersionForSlug(s)
        .then(setSlugVersion)
        .catch(() => setSlugVersion(null));
    }, 350);
    return () => clearTimeout(t);
  }, [slug, versioning]);

  const onPick = async (f: File) => {
    setError(null);
    const ab = await f.arrayBuffer();
    const d = detectType(f, ab);
    setFile(f);
    setBuf(ab);
    setIsZip(d.artifactType === "PACKAGE" || d.artifactType === "APP");
    setArtifactType(d.artifactType);
    setContentType(d.contentType);
    setFileSha(await sha256Hex(ab));
    // Filename-derived defaults for a new document. A version keeps the
    // document's own title and slug — a dropped file is new content for an
    // existing document, not a rename of it.
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
      if (res.title) {
        setTitle(res.title);
        setTitleTouched(true);
      }
      // Never in version mode: the slug is the document's identity, and
      // retargeting one is what the mode switch is for.
      if (res.slug && !versioning) setSlug(res.slug);
      if (res.labels?.length) {
        commitLabels(Array.from(new Set([...(labelsTouched ? labelsRef.current : inheritedLabels), ...res.labels])));
      }
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
    if (!effectiveTitle.trim()) {
      setError("Title is required.");
      return;
    }
    if (needsAck && !acked) {
      setError("Confirm the type change before publishing.");
      return;
    }
    if (awaitingLatest) {
      setError("Still reading the document — try again in a moment.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const info = await createArtifact({
        title: effectiveTitle.trim(),
        content_type: contentType,
        artifact_type: effectiveType,
        content_base64: bytesToBase64(buf),
        named_slug: effectiveSlug.trim() || undefined,
        // Omitted when untouched so the server inherits from the slug's fresh
        // prior version; sent (even empty) once edited, which is how you clear.
        scopes: scopesTouched ? scopesRef.current : undefined,
        labels: labelsTouched ? labelsRef.current : undefined,
        // The server inherits scopes/labels/access but not the description, so
        // a version has to carry it over explicitly or the document loses it.
        description: versioning && inheritedDescription ? inheritedDescription : undefined,
        expected_latest_version: versioning && latestVersion != null ? latestVersion : undefined,
        allow_type_change: versioning && change ? true : undefined,
      });
      const dest = info.named_slug
        ? `/s/${info.named_slug}` + (info.version ? `/${info.version}` : "")
        : `/a/${info.artifact_id}`;
      router.push(dest);
      onClose();
    } catch (e) {
      // Someone published while this form was open (or the document changed
      // shape under us). Re-read the slug, re-inherit from the version that is
      // actually there now, re-arm the acknowledgement, and let the user
      // confirm against the new base — the file is still here, so nothing is
      // half-committed.
      if (
        versioning &&
        e instanceof ArtiError &&
        (e.code === "stale-base-version" || e.code === "type-change")
      ) {
        setAckedKey("");
        const v = await refreshLatest(target!.slug);
        setNotice(
          v != null
            ? `${e.message} Re-checked: s/${target!.slug} is at v${v}, so this would publish as v${v + 1}.`
            : e.message,
        );
        setBusy(false);
        return;
      }
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

  const publishLabel = busy
    ? "Uploading…"
    : awaitingLatest
      ? `Reading s/${target!.slug}…`
      : versioning
      ? (nextVersion != null ? `Publish v${nextVersion}` : "Publish new version") +
        (change ? ` as ${change.to}` : "")
      : "Upload";

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
        aria-label={versioning ? "Publish a new version" : "Upload artifact"}
        className="w-full max-w-lg rounded-xl border border-neutral-200 bg-white shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        {/* header */}
        <div className="flex items-center justify-between border-b border-neutral-100 px-5 py-3">
          <h2 className="flex items-center gap-2 text-[15px] font-semibold text-neutral-900">
            {versioning ? (
              <>
                <span>
                  New version of <span className="font-mono text-[13px]">s/{target!.slug}</span>
                </span>
                {latestVersion != null ? (
                  <span className="rounded-full bg-neutral-100 px-2 py-0.5 font-mono text-[11px] font-normal text-neutral-600">
                    v{latestVersion} → v{latestVersion + 1}
                  </span>
                ) : null}
              </>
            ) : (
              "Upload artifact"
            )}
          </h2>
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
          {notice ? (
            <div className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-[12px] text-amber-800">
              {notice}
            </div>
          ) : null}

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
              {/* the type change this file makes to the document */}
              {change ? (
                <div
                  className={
                    "rounded-md border px-3 py-2 text-[12px] " +
                    (change.severity === "hard"
                      ? "border-rose-200 bg-rose-50 text-rose-800"
                      : "border-amber-200 bg-amber-50 text-amber-800")
                  }
                >
                  <div className="flex items-center gap-1.5 font-semibold">
                    <span aria-hidden="true">{change.severity === "hard" ? "⛔" : "⚠"}</span>
                    {change.headline}
                  </div>
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                    <TypeChip value={change.from} tone="from" />
                    <span aria-hidden="true">→</span>
                    <TypeChip value={change.to} tone="to" />
                    <span>
                      {nextVersion != null ? `v${nextVersion}` : "this version"} publishes as{" "}
                      <span className="font-mono">{change.to}</span>
                      {change.note ? ` — ${change.note}` : ""}.
                    </span>
                  </div>
                  <div className="mt-1 opacity-80">
                    {latestVersion != null && latestVersion > 1
                      ? `v1–v${latestVersion} are untouched.`
                      : "Earlier versions are untouched."}
                  </div>
                  {change.severity === "hard" ? (
                    <label className="mt-2 flex items-center gap-2 font-medium">
                      <input
                        type="checkbox"
                        checked={acked}
                        onChange={(e) => setAckedKey(e.target.checked ? changeKey : "")}
                      />
                      I understand — publish {nextVersion != null ? `v${nextVersion}` : "this version"} as{" "}
                      <span className="font-mono">{change.to}</span>
                    </label>
                  ) : null}
                  <div className="mt-1.5">
                    <button
                      type="button"
                      onClick={() => setMode("new")}
                      className="underline underline-offset-2"
                    >
                      Upload as a new document instead →
                    </button>
                  </div>
                </div>
              ) : null}

              {/* publishing ahead of the version on screen */}
              {versioning &&
              latestVersion != null &&
              target?.viewedVersion != null &&
              target.viewedVersion !== latestVersion ? (
                <p className="text-[11px] text-amber-600">
                  You&apos;re viewing v{target.viewedVersion}; this publishes as v{latestVersion + 1},
                  ahead of v{latestVersion}.
                </p>
              ) : null}

              {/* identical bytes */}
              {versioning && sameBytes ? (
                <p className="text-[11px] text-neutral-500">
                  These bytes are identical to v{latestVersion} — publishing makes a version that
                  differs only in its metadata.
                </p>
              ) : null}

              {/* title + auto-fill */}
              <div>
                <div className="mb-1 flex items-center justify-between">
                  <label className="text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                    title
                    {versioning && !titleTouched ? (
                      <span className="ml-1.5 font-normal normal-case tracking-normal text-neutral-400">
                        (inherited{latestVersion != null ? ` from v${latestVersion}` : ""})
                      </span>
                    ) : null}
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
                  value={effectiveTitle}
                  onChange={(e) => {
                    setTitle(e.target.value);
                    setTitleTouched(true);
                  }}
                  placeholder="A human-readable title"
                  className="w-full rounded-md border border-neutral-200 px-3 py-1.5 text-[13px] text-neutral-900 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
                />
              </div>

              {/* slug — a locked chip while versioning: retargeting a version
                  upload by editing the slug is how you version someone else's
                  document by accident. The mode switch is the way out. */}
              <div>
                <label className="mb-1 block text-[11px] font-semibold uppercase tracking-widest text-neutral-400">
                  slug{" "}
                  {versioning ? null : (
                    <span className="font-normal normal-case text-neutral-400">(optional)</span>
                  )}
                </label>
                {versioning ? (
                  <div className="flex items-center gap-2 rounded-md border border-neutral-200 bg-neutral-50 px-3 py-1.5">
                    <span aria-hidden="true" className="text-neutral-400">
                      🔒
                    </span>
                    <span className="font-mono text-[12px] text-neutral-700">s/{target!.slug}</span>
                  </div>
                ) : (
                  <>
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
                  </>
                )}
                {target ? (
                  <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-neutral-600">
                    <label className="flex items-center gap-1.5">
                      <input
                        type="radio"
                        name="upload-mode"
                        checked={mode === "version"}
                        onChange={() => setMode("version")}
                      />
                      New version of <span className="font-mono">s/{target.slug}</span>
                    </label>
                    <label className="flex items-center gap-1.5">
                      <input
                        type="radio"
                        name="upload-mode"
                        checked={mode === "new"}
                        onChange={() => setMode("new")}
                      />
                      New document
                    </label>
                  </div>
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
                  {versioning && !scopesTouched && inheritedScopes.length > 0 ? (
                    <span className="ml-1.5 font-normal normal-case tracking-normal text-neutral-400">
                      (inherited)
                    </span>
                  ) : null}
                </label>
                <ChipInput
                  values={effectiveScopes}
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
                  {versioning && !labelsTouched && inheritedLabels.length > 0 ? (
                    <span className="ml-1.5 font-normal normal-case tracking-normal text-neutral-400">
                      (inherited)
                    </span>
                  ) : null}
                </label>
                <ChipInput
                  values={effectiveLabels}
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
            disabled={busy || !file || !effectiveTitle.trim() || (needsAck && !acked) || awaitingLatest}
            className="rounded-md bg-blue-600 px-4 py-1.5 text-[13px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {publishLabel}
          </button>
        </div>
      </div>
    </div>
  );

  return createPortal(body, document.body);
}
