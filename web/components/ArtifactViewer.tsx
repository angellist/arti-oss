"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import type { ArtifactInfo, Me, PackageManifest } from "@/lib/types";
import { formatBytes } from "@/lib/format";
import { relativeTime } from "@/lib/time";
import { useRailMode } from "@/lib/rail-context";
import { isTextualContentType, isJSONContentType, prettyPrintJSON } from "@/lib/viewer";
import { isEditableArtifact } from "@/lib/edit";
import { isComparableArtifact } from "@/lib/diff";
import MarkdownBody from "./MarkdownBody";
import { LabelEditor, ScopeEditor } from "./ChipEditors";
import { archiveArtifact, encodeFilePath, fetchPackageFile, hasPerm, latestVersionForSlug, listShares, sameEmail, unarchiveArtifact, updateArtifactCommentsEnabled, updateArtifactTitle } from "@/lib/arti";
import ViewerToolbar, { RawToggle, TEXT_SCALE, WIDTH_CLASS, useViewerPrefs, type Width } from "./ViewerToolbar";
import AccessModal from "./AccessModal";
import CommentsLayer from "./CommentsLayer";
import CommentsMenuItem from "./CommentsMenuItem";
import { MENU_ROW, MENU_SVG, MenuIcon } from "./MenuRow";
import { ShareModal } from "./ShareModal";
import CreatorName from "./CreatorName";
import ArtifactEditor from "./ArtifactEditor";
import ArtifactCompare from "./ArtifactCompare";
import DiagramView from "./DiagramView";
import DiagramArtifactEditor from "./DiagramArtifactEditor";
import { isDiagramContentType } from "@/lib/diagram";
import { useExternalLinkMessage } from "@/lib/useExternalLinkMessage";
import { useUpload } from "@/lib/upload-context";
import { targetFromInfo } from "@/lib/upload-target";

// BackButton — small left-chevron that pops one step in browser history
// if there is one, falling back to the catalog root. Lives at the very
// start of the title row so users can always get out of a viewer.
function BackButton() {
  const router = useRouter();
  return (
    <button
      type="button"
      onClick={() => {
        if (window.history.length > 1) router.back();
        else router.push("/");
      }}
      title="back to listing"
      aria-label="back"
      className="rounded-md border border-neutral-200 bg-white px-2 py-1 text-neutral-500 transition hover:bg-neutral-100 hover:text-neutral-800"
    >
      <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <polyline points="15 18 9 12 15 6" />
      </svg>
    </button>
  );
}

function isMarkdown(ct: string) {
  return ct.startsWith("text/markdown");
}
function isHTML(ct: string) {
  return ct.startsWith("text/html");
}
function isPlainCode(ct: string) {
  if (ct.startsWith("text/plain")) return true;
  if (ct.startsWith("text/x-")) return true;
  // Strip params (e.g. "; charset=utf-8") so a JSON/YAML content_type with a
  // charset still matches — mirrors isPDF's normalization in this file.
  const base = ct.split(";")[0].trim().toLowerCase();
  return base === "application/json" || base === "application/yaml";
}
function isImage(ct: string) {
  return ct.startsWith("image/");
}
function isPDF(ct: string) {
  return ct.split(";")[0].trim().toLowerCase() === "application/pdf";
}

// Maps a content-type (possibly with parameters like "; charset=utf-8")
// to a filename extension for the download attribute. Empty string when
// unknown — caller falls back to the bare slug/title.
function extFromContentType(ct: string): string {
  const t = ct.split(";")[0].trim().toLowerCase();
  switch (t) {
    case "text/markdown": return "md";
    case "text/html": return "html";
    case "text/plain": return "txt";
    case "text/x-shellscript": return "sh";
    case "text/x-python": return "py";
    case "text/x-go": return "go";
    case "application/json": return "json";
    case "application/yaml": return "yaml";
    case "application/javascript": return "js";
    case "application/zip": return "zip";
  }
  if (t.endsWith("+json")) return "json";
  return "";
}

function safeBaseName(info: ArtifactInfo): string {
  const raw = info.named_slug || info.title || info.artifact_id;
  return raw.replace(/[\\/:*?"<>|]+/g, "_");
}

// EditableTitle — h1 that turns into a single-line input on click for
// the artifact's owner or an admin. Enter saves, Escape reverts. While
// saving the input is disabled; on error we revert and surface the
// message inline so the next click can retry.
export function EditableTitle({
  initial,
  canEdit,
  isPackage,
  onSave,
  onCollapse,
}: {
  initial: string;
  canEdit: boolean;
  isPackage: boolean;
  onSave: (next: string) => Promise<void>;
  onCollapse: () => void;
}) {
  const [title, setTitle] = useState(initial);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(initial);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string>("");
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);
  // Keep the displayed title in sync when server-side info.title changes between
  // renders. This is load-bearing and was verified as such: rename the artifact
  // out-of-band and call router.refresh() — which re-renders without remounting —
  // and with this block removed the <h1> keeps the stale title while the
  // server-rendered document <title> updates. Adjusted during render rather than
  // in an effect so the new title lands in the same commit as the new prop.
  //
  // Keying this component on `initial` would also work, but it would reset
  // `editing`, `saving` and `err` too — discarding an in-progress rename if a
  // refresh happens to land mid-edit.
  const [syncedTitle, setSyncedTitle] = useState(initial);
  if (initial !== syncedTitle) {
    setSyncedTitle(initial);
    setTitle(initial);
    setDraft(initial);
  }
  const startEdit = () => {
    if (!canEdit) return;
    setErr("");
    setDraft(title);
    setEditing(true);
  };
  const cancel = () => {
    setDraft(title);
    setEditing(false);
    setErr("");
  };
  const commit = async () => {
    const next = draft.trim();
    if (next === "" || next === title) {
      cancel();
      return;
    }
    setSaving(true);
    setErr("");
    try {
      await onSave(next);
      setTitle(next);
      setEditing(false);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  // Single click → collapse; double-click → edit (if canEdit).
  //
  // Collapse fires on the click itself, with no disambiguation delay. The
  // previous version armed a 400 ms timer on the first click so a second one
  // could still mean "rename", which made every collapse via the title feel
  // broken next to the identical toggle on the triangle — 400 ms is well past
  // the ~100 ms where a click stops reading as instant, and the delay was paid
  // by every viewer, including the read-only ones who can't rename at all.
  //
  // No timer is needed because collapse is a *toggle* and the browser fires
  // click, click, dblclick: the two clicks of a double-click cancel out, so the
  // header is back where it started by the time startEdit runs. The transient
  // flip is a couple of frames and is then covered by the edit input.
  const handleClick = () => {
    if (editing) return;
    onCollapse();
  };
  const handleDoubleClick = () => {
    if (editing || !canEdit) return;
    startEdit();
  };

  if (editing) {
    return (
      <h1 className="flex items-center gap-2">
        <input
          ref={inputRef}
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            } else if (e.key === "Escape") {
              e.preventDefault();
              cancel();
            }
          }}
          onBlur={cancel}
          disabled={saving}
          className="rounded-md border border-blue-300 bg-white px-2 py-0.5 text-base font-semibold text-neutral-900 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
          style={{ width: `min(${Math.max(draft.length, 8) + 4}ch, 60ch)` }}
          aria-label="edit title"
        />
        {isPackage ? <span aria-label="package">📦</span> : null}
        {err ? <span className="text-[11px] text-rose-600">error: {err}</span> : null}
      </h1>
    );
  }
  return (
    <h1
      className="flex select-none items-center gap-2 rounded -mx-1 px-1 text-base font-semibold text-neutral-900 cursor-pointer transition hover:bg-neutral-100"
      onClick={handleClick}
      onDoubleClick={handleDoubleClick}
      title={canEdit ? "click to collapse · double-click to rename" : "click to collapse"}
    >
      {title}
      {isPackage ? (
        <span title="multi-file PACKAGE artifact" aria-label="package">📦</span>
      ) : null}
    </h1>
  );
}

// PermalinkChip — UUID link to /a/<uuid> (slug- and version-free) with a
// one-click copy button that grabs the absolute URL. Displays "a/UUID" as
// a placeholder instead of the full UUID for cleaner presentation.
function PermalinkChip({ href, copy }: { href: string; copy: string }) {
  const [copied, setCopied] = useState(false);
  const onCopy = async (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    try {
      await navigator.clipboard.writeText(copy);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1200);
    } catch {
      // ignore — clipboard requires a user gesture; this click qualifies.
    }
  };
  return (
    <span className="inline-flex items-center overflow-hidden rounded-md border border-neutral-200 bg-white font-mono text-[11px]">
      <a
        href={href}
        className="px-2 py-0.5 text-neutral-700 no-underline transition hover:bg-neutral-100"
        title="permalink (UUID, version-free)"
      >
        <span className="text-neutral-400">a/</span>
        UUID
      </a>
      <button
        type="button"
        onClick={onCopy}
        className="border-l border-neutral-200 px-1.5 py-0.5 text-neutral-500 transition hover:bg-neutral-100 hover:text-neutral-800"
        title={copied ? "copied!" : "copy permalink"}
        aria-label="copy permalink"
      >
        {copied ? (
          <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
            <polyline points="3 8 7 12 13 4" />
          </svg>
        ) : (
          <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
            <rect x="4.5" y="4.5" width="8" height="8" rx="1.5" />
            <path d="M3 11V3.5C3 2.94772 3.44772 2.5 4 2.5H11" strokeLinecap="round" />
          </svg>
        )}
      </button>
    </span>
  );
}

// SlugChip — the named-slug reference (s/<slug> · v<n>), styled like the
// UUID permalink chip so the two identifiers sit together on one row. Links
// to every version of the slug. When the viewed version is not the slug's
// latest (a pinned /s/<slug>/<ver> or /a/<uuid> view), a "(latest is vN)"
// link to the latest version is appended so readers notice the older pin.
// The latest-version probe is lazy and best-effort (see latestVersionForSlug).
function SlugChip({ slug, version }: { slug: string; version: number | null }) {
  // The fetched value is keyed to its slug: the chip stays mounted across
  // client-side navigations, so a bare number could briefly (or, if the next
  // probe fails, permanently) show the previous slug's latest against the new
  // slug. A key mismatch self-invalidates instead of needing a reset-in-effect.
  const [latest, setLatest] = useState<{ slug: string; version: number | null } | null>(null);
  useEffect(() => {
    if (version == null) return;
    let cancelled = false;
    latestVersionForSlug(slug)
      .then((v) => {
        if (!cancelled) setLatest({ slug, version: v });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [slug, version]);
  const latestV = latest !== null && latest.slug === slug ? latest.version : null;
  const stale = version != null && latestV != null && latestV !== version;
  return (
    <span className="inline-flex items-center overflow-hidden rounded-md border border-neutral-200 bg-white font-mono text-[11px]">
      <a
        href={`/?slug=${encodeURIComponent(slug)}&order_by=version&order_dir=desc`}
        className="px-2 py-0.5 text-neutral-700 no-underline transition hover:bg-neutral-100"
        title="show every version of this slug"
      >
        <span className="text-neutral-400">s/</span>
        {slug}
        {version != null ? <span className="text-neutral-400"> · v{version}</span> : null}
      </a>
      {stale ? (
        <a
          href={`/s/${slug}`}
          className="py-0.5 pr-2 text-amber-600 no-underline transition hover:text-amber-800 hover:underline"
          title="viewing an older version — open the latest"
        >
          (latest is v{latestV})
        </a>
      ) : null}
    </span>
  );
}

// AccessSummary returns a (color, label, tooltip) tuple describing the
// effective read-access for the artifact. Tier classification:
//   - public      array contains '*'  (anyone authenticated)
//   - domain      every pattern is a `*@something` glob — show the domain(s)
//   - restricted  specific emails (and/or empty array == creator-only)
function accessTier(patterns: string[]): {
  tier: "public" | "domain" | "restricted";
  label: string;
  tooltip: string;
} {
  if (patterns.length === 0) {
    return { tier: "restricted", label: "private", tooltip: "creator and admins only" };
  }
  if (patterns.includes("*")) {
    return { tier: "public", label: "public", tooltip: "any authenticated user" };
  }
  const allDomain = patterns.every((p) => /^\*@[^*?]+$/.test(p));
  if (allDomain) {
    const domains = patterns.map((p) => p.slice(2)).join(", ");
    return { tier: "domain", label: domains, tooltip: `restricted to: ${domains}` };
  }
  return {
    tier: "restricted",
    label: patterns.length === 1 ? patterns[0] : `${patterns.length} entries`,
    tooltip: `restricted to: ${patterns.join(", ")}`,
  };
}

// AccessButton renders the "Edit Access" trigger in the controls row.
// Clicking it opens the centered AccessModal. Readers see the same button
// labeled "Access" with a view-only modal.
function AccessButton({
  artifactID,
  access,
  write,
  hasOtherVersions,
  canEdit,
  canShare,
  onSaved,
}: {
  artifactID: string;
  access: string[];
  write?: string[] | null;
  hasOtherVersions: boolean;
  canEdit: boolean;
  // The server's `can_share`. Gates the live-link count, which is owner-only
  // information and is not worth a request otherwise.
  canShare: boolean;
  onSaved: () => void;
}) {
  const [open, setOpen] = useState(false);
  // Live external links on this document, counted only when the modal opens:
  // it is owner-only information and nobody pays for it on page load.
  const [liveShares, setLiveShares] = useState(0);

  // Reset on a document switch. Without this the count survives: the effect
  // below returns early when the new document is not shareable, so a previous
  // document's non-zero count would keep rendering AccessModal's amber "N live
  // external links" warning against a document that has none.
  const [countedFor, setCountedFor] = useState(artifactID);
  if (artifactID !== countedFor) {
    setCountedFor(artifactID);
    setLiveShares(0);
  }

  useEffect(() => {
    if (!open || !canShare) return;
    let cancelled = false;
    void listShares(artifactID)
      .then((rows) => {
        if (cancelled) return;
        const now = Date.now();
        setLiveShares(
          rows.filter((l) => !l.revoked_at && new Date(l.expires_at).getTime() > now).length,
        );
      })
      // A failed count must not break the access editor; the notice is an
      // extra warning, not a precondition for editing.
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [open, canShare, artifactID]);

  const tier = accessTier(access);
  const dot =
    tier.tier === "public"
      ? "bg-emerald-400"
      : tier.tier === "domain"
      ? "bg-amber-400"
      : "bg-rose-500";

  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-haspopup="dialog"
        title={tier.tooltip}
        className="inline-flex items-center gap-1.5 rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
      >
        <span className={`inline-block h-1.5 w-1.5 rounded-full ${dot}`} aria-hidden="true" />
        {canEdit ? "Edit Access" : "Access"}
      </button>
      {open ? (
        <AccessModal
          artifactID={artifactID}
          access={access}
          write={write}
          hasOtherVersions={hasOtherVersions}
          canEdit={canEdit}
          liveShareLinks={liveShares}
          onClose={() => setOpen(false)}
          onSaved={onSaved}
        />
      ) : null}
    </>
  );
}

// ThreeDotsMenu — contextual overflow menu combining Edit, Compare, Download,
// the comment switch and Archive into a single three-dot button to the right of
// Edit Access. (Raw Source is the RawToggle sitting just left of the access
// button in the same row.)
function ThreeDotsMenu({
  downloadHref,
  downloadName,
  downloadLabel,
  zipHref,
  zipName,
  canEdit,
  isArchived,
  archiving,
  onArchive,
  archiveTitle,
  onEdit,
  editTitle,
  onUploadVersion,
  onCompare,
  commentsOn,
  canManageComments,
  commentsBusy,
  onToggleComments,
  onShare,
}: {
  downloadHref?: string;
  downloadName?: string;
  downloadLabel?: string;
  zipHref?: string;
  zipName?: string;
  canEdit: boolean;
  isArchived: boolean;
  archiving: boolean;
  onArchive: () => void;
  archiveTitle: string;
  onEdit?: () => void;
  // editTitle overrides the Edit item tooltip — a diagram opens a canvas,
  // not the raw source, and the tooltip should not claim otherwise.
  editTitle?: string;
  // Upload a file as the next version of this document. Offered on the same
  // terms as the page-wide drop (a writable, live, slugged document) and is
  // the discoverable half of it: an HTML or APP body renders in a sandboxed
  // iframe, whose drag events never reach the parent window, so on those the
  // menu is the only way in.
  onUploadVersion?: () => void;
  onCompare?: () => void;
  // Comments row: shown to everyone who can see the menu (the dot is the
  // answer to "why is there nowhere to comment?"), but only the owner —
  // canManageComments, the server's own verdict — can click it. Omitted
  // entirely when onToggleComments is undefined, i.e. on artifacts that never
  // support comments (attachments, binaries) where a state dot would be noise.
  commentsOn?: boolean;
  canManageComments?: boolean;
  commentsBusy?: boolean;
  onToggleComments?: () => void;
  onShare: () => void;
}) {
  const [open, setOpen] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);
  const btnRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (menuRef.current?.contains(t)) return;
      if (btnRef.current?.contains(t)) return;
      setOpen(false);
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

  return (
    <div className="relative">
      <button
        ref={btnRef}
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-label="more actions"
        aria-expanded={open}
        className="inline-flex items-center rounded-md border border-neutral-200 bg-white px-2 py-1 text-neutral-500 transition hover:bg-neutral-50 hover:text-neutral-800"
      >
        <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
          <circle cx="8" cy="3" r="1.5" />
          <circle cx="8" cy="8" r="1.5" />
          <circle cx="8" cy="13" r="1.5" />
        </svg>
      </button>
      {open ? (
        <div
          ref={menuRef}
          className="absolute right-0 top-full z-30 mt-1 min-w-[160px] rounded-md border border-neutral-200 bg-white py-1 shadow-lg"
        >
          <button
            type="button"
            onClick={() => { onShare(); setOpen(false); }}
            className={`${MENU_ROW} hover:bg-neutral-50`}
            title="copy this page's link, or create a time-limited external link"
          >
            <MenuIcon>
              <svg {...MENU_SVG}>
                <path d="M4 12v7a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7" />
                <polyline points="16 6 12 2 8 6" />
                <line x1="12" y1="2" x2="12" y2="15" />
              </svg>
            </MenuIcon>
            Share
          </button>
          {onEdit ? (
            <button
              type="button"
              onClick={() => { onEdit(); setOpen(false); }}
              className={`${MENU_ROW} hover:bg-neutral-50`}
              title={editTitle ?? "edit the raw source — saving creates a new version"}
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <path d="M17 3a2.85 2.83 0 1 1 4 4L7.5 20.5 2 22l1.5-5.5Z" />
                </svg>
              </MenuIcon>
              Edit
            </button>
          ) : null}
          {onUploadVersion ? (
            <button
              type="button"
              onClick={() => { onUploadVersion(); setOpen(false); }}
              className={`${MENU_ROW} hover:bg-neutral-50`}
              title="upload a file as the next version of this document"
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                  <polyline points="7 9 12 4 17 9" />
                  <line x1="12" y1="4" x2="12" y2="16" />
                </svg>
              </MenuIcon>
              Upload new version…
            </button>
          ) : null}
          {onCompare ? (
            <button
              type="button"
              onClick={() => { onCompare(); setOpen(false); }}
              className={`${MENU_ROW} hover:bg-neutral-50`}
              title="compare two versions side by side"
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <rect x="3" y="4" width="18" height="16" rx="2" />
                  <line x1="12" y1="4" x2="12" y2="20" />
                </svg>
              </MenuIcon>
              Compare versions
            </button>
          ) : null}
          {downloadHref ? (
            <a
              href={downloadHref + (downloadHref.includes("?") ? "&" : "?") + "download=1"}
              download={downloadName || ""}
              className={`${MENU_ROW} hover:bg-neutral-50`}
              onClick={() => setOpen(false)}
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                  <polyline points="7 10 12 15 17 10" />
                  <line x1="12" y1="3" x2="12" y2="15" />
                </svg>
              </MenuIcon>
              {downloadLabel ?? "Download"}
            </a>
          ) : null}
          {zipHref ? (
            <a
              href={zipHref + (zipHref.includes("?") ? "&" : "?") + "download=1"}
              download={zipName || ""}
              className={`${MENU_ROW} hover:bg-neutral-50`}
              title="download the full package as a .zip"
              onClick={() => setOpen(false)}
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
                  <polyline points="7 10 12 15 17 10" />
                  <line x1="12" y1="3" x2="12" y2="15" />
                </svg>
              </MenuIcon>
              Download zip
            </a>
          ) : null}
          {onToggleComments ? (
            <CommentsMenuItem
              on={!!commentsOn}
              canManage={!!canManageComments}
              busy={commentsBusy}
              onToggle={() => onToggleComments()}
            />
          ) : null}
          {canEdit ? (
            <button
              type="button"
              onClick={() => { onArchive(); setOpen(false); }}
              disabled={archiving}
              className={`${MENU_ROW} hover:bg-neutral-50 hover:text-rose-700 disabled:opacity-50`}
              title={archiveTitle}
            >
              <MenuIcon>
                <svg {...MENU_SVG}>
                  <rect x="3" y="4" width="18" height="4" rx="1" />
                  <path d="M5 8v11a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8" />
                  <line x1="10" y1="13" x2="14" y2="13" />
                </svg>
              </MenuIcon>
              {archiving
                ? (isArchived ? "unarchiving\u2026" : "archiving\u2026")
                : (isArchived ? "Unarchive" : "Archive")}
            </button>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

// CollapseToggle — the triangle that collapses/expands the viewer header.
// 14px glyph in a p-1.5 button: at the previous 10px/p-0.5 it was a ~14px
// target, under the 24px minimum for a comfortable pointer hit and small
// enough to read as decoration rather than a control. -mr-1 pulls the extra
// padding back out of the row so the glyph keeps its old right edge.
export function CollapseToggle({
  collapsed,
  onClick,
  label,
}: {
  collapsed: boolean;
  onClick: () => void;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      aria-expanded={!collapsed}
      className="-mr-1 inline-flex items-center rounded p-1.5 text-neutral-400 transition hover:bg-neutral-100 hover:text-neutral-700"
    >
      <svg
        width="14"
        height="14"
        viewBox="0 0 10 10"
        fill="currentColor"
        aria-hidden="true"
        className={`transition-transform ${collapsed ? "" : "rotate-90"}`}
      >
        <polygon points="2,1 8,5 2,9" />
      </svg>
    </button>
  );
}

function RenderedBody({ body, ct, src, allowPopups, docKey }: { body: string; ct: string; src?: string; allowPopups?: boolean; docKey?: string }) {
  const iframeRef = useRef<HTMLIFrameElement>(null);
  useExternalLinkMessage(iframeRef);
  if (isMarkdown(ct)) return <MarkdownBody body={body} docKey={docKey} />;
  if (isHTML(ct)) {
    // When `src` is provided (a single HTML file inside a PACKAGE, or a
    // standalone HTML artifact), the iframe loads from arti's origin so
    // relative URLs resolve, scripts run, and arti-server can inject the
    // comments overlay into the served page. We intentionally
    // do NOT include allow-same-origin: that combination would grant the
    // uploaded HTML full access to arti's DOM, localStorage, and the
    // arti_session cookie — i.e. anyone could upload a malicious package
    // whose script exfiltrates the viewer's catalog with their identity.
    // Static asset loads (img/script/link with no `crossorigin` attr)
    // still work; only JS-driven fetch/XHR back to arti is CORS-blocked,
    // which arti's interactive reports don't need (data is inlined).
    const common = {
      className: "h-[calc(100vh-180px)] w-full rounded border border-neutral-200 bg-white",
      title: "artifact content",
    } as const;
    // APP files carry the injected bridge; if previewed in-viewer, the Runlayer
    // OAuth consent needs window.open, so the parent iframe must allow popups
    // (still NO allow-same-origin — the opaque-origin guarantee is unchanged).
    const sandbox = allowPopups
      ? "allow-scripts allow-popups allow-popups-to-escape-sandbox allow-downloads"
      : "allow-scripts allow-downloads";
    return src ? (
      <iframe {...common} ref={iframeRef} src={src} sandbox={sandbox} />
    ) : (
      <iframe {...common} ref={iframeRef} srcDoc={body} sandbox={sandbox} />
    );
  }
  if (isPlainCode(ct)) {
    // JSON renders pretty-printed (2-space indent) by default; "Raw Source"
    // (RawBody, below) still shows the artifact's stored body verbatim.
    const displayBody = isJSONContentType(ct) ? prettyPrintJSON(body) : body;
    return (
      <pre className="whitespace-pre-wrap break-words rounded-md border border-neutral-200 bg-neutral-50 px-6 py-5 font-mono text-[12px] leading-relaxed text-neutral-800">
        {displayBody}
      </pre>
    );
  }
  return (
    <pre className="whitespace-pre-wrap font-mono text-sm text-neutral-800">
      {body}
    </pre>
  );
}

function RawBody({ body }: { body: string }) {
  return (
    <pre className="whitespace-pre-wrap break-words rounded-md border border-neutral-200 bg-neutral-50 px-6 py-5 font-mono text-[11px] leading-relaxed text-neutral-700">
      {body}
    </pre>
  );
}

// AttachmentDownload renders a non-renderable attachment (pdf, zip, binary,
// …) as a download card rather than dumping raw bytes into a <pre>.
function AttachmentDownload({
  href,
  name,
  contentType,
  size,
}: {
  href: string;
  name: string;
  contentType: string;
  size: number | null;
}) {
  return (
    <div className="mx-auto flex max-w-md flex-col items-center gap-3 rounded-lg border border-neutral-200 bg-neutral-50 px-6 py-10 text-center">
      <div className="text-4xl">📎</div>
      <div className="break-all font-medium text-neutral-900">{name}</div>
      <div className="text-[12px] text-neutral-500">
        {contentType}
        {size != null ? ` · ${formatBytes(size)}` : ""}
      </div>
      <a
        href={href}
        download={name}
        className="mt-1 rounded-md border border-blue-300 bg-white px-4 py-1.5 text-[13px] font-medium text-blue-700 hover:bg-blue-50"
      >
        ↓ Download
      </a>
    </div>
  );
}

// NonTextBody renders content whose bytes aren't text: a PDF inline via the
// browser's native viewer (no JS lib — the catalog CSP already allows
// frame-src 'self', and arti serves the bytes inline), an image inline, and
// anything else (zip, binary, …) as a download card. `src` is the raw
// same-origin artifact URL. Used for ATTACHMENT uploads and for any
// (legacy) TEXT artifact that carries a non-textual content_type.
function NonTextBody({
  ct,
  src,
  name,
  size,
}: {
  ct: string;
  src: string;
  name: string;
  size: number | null;
}) {
  if (isPDF(ct)) {
    return (
      <iframe
        src={src}
        title={name}
        className="h-[calc(100vh-180px)] w-full rounded border border-neutral-200 bg-white"
      />
    );
  }
  if (isImage(ct)) {
    return (
      // eslint-disable-next-line @next/next/no-img-element
      <img
        src={src}
        alt={name}
        className="mx-auto max-h-[80vh] max-w-full rounded border border-neutral-200"
      />
    );
  }
  return <AttachmentDownload href={src} name={name} contentType={ct} size={size} />;
}

export default function ArtifactViewer({
  info,
  body,
  manifest,
  fullHref,
  me,
}: {
  info: ArtifactInfo;
  body?: string;
  manifest?: PackageManifest;
  fullHref?: string;
  me?: Me;
}) {
  const { width, setWidth, textSize, setTextSize, original, setOriginal } = useViewerPrefs();
  // Multiplier the rendered body's font-sizes are calc()'d against, published
  // to content as the inherited `--arti-text-scale` CSS var (see TEXT_SCALE).
  const textScale = TEXT_SCALE[textSize];
  const [editing, setEditing] = useState(false);
  const [comparing, setComparing] = useState(false);
  // Whether the open markdown editor is in Split. Per-view, like `editing`.
  const [editorSplit, setEditorSplit] = useState(false);
  // Compare mode carries its own width, defaulting to Wide (reset on every
  // open, below) so a two-pane diff always opens roomy instead of inheriting a
  // narrow single-doc width. The width control drives THIS while comparing, so
  // adjusting it never writes to the persisted per-doc width (useViewerPrefs);
  // closing the diff falls back to that remembered width.
  const [compareWidth, setCompareWidth] = useState<Width>("wide");
  // Raw Source, edit, and compare modes are per-view, not sticky: reset all
  // whenever we land on a different artifact, in case this component instance is
  // reused across a client-side navigation instead of remounting.
  // Adjusted during render rather than in an effect, so the reset is applied in
  // the same commit as the new artifact rather than one commit later.
  //
  // Caveat, measured rather than assumed: as of Next 16 this guard never
  // actually fires. Every artifact→artifact navigation tried in a dev server —
  // slug→slug and version→version, via both links and router.push — REMOUNTS
  // this component, so `useState(false)` has already reset these four. Deleting
  // it was tempting, but a dev-mode observation is not evidence about the
  // production router, and the invariant it protects (per-view chrome must not
  // survive a change of document) would fail silently and confusingly if the
  // instance ever were reused. Kept as a cheap guard. To retire it, first
  // confirm remount-on-nav against a production build.
  //
  // Keying the whole viewer on artifact_id would also work, but it would remount
  // the comments overlay and the width machinery — far more than this needs.
  const [sharing, setSharing] = useState(false);
  const [renderedArtifactId, setRenderedArtifactId] = useState(info.artifact_id);
  if (info.artifact_id !== renderedArtifactId) {
    setRenderedArtifactId(info.artifact_id);
    setOriginal(false);
    setEditing(false);
    setComparing(false);
    setEditorSplit(false);
    // Sharing too: a modal left open across an in-place document switch would
    // keep showing the previous document's minted URL and link list while
    // artifactID underneath it points at the new one.
    setSharing(false);
  }
  const mode = useRailMode();
  const router = useRouter();
  // Register this document as the drop target, so a file dropped anywhere on
  // the page publishes a new version of it instead of a new document. Null for
  // anything with no version lineage to extend — see targetFromInfo.
  const upload = useUpload();
  const uploadTarget = useMemo(() => targetFromInfo(info), [info]);
  const { setTarget, clearTarget } = upload;
  useEffect(() => {
    if (!uploadTarget) return;
    setTarget(uploadTarget);
    return () => clearTarget(uploadTarget);
  }, [uploadTarget, setTarget, clearTarget]);
  const canEdit = !!me && (hasPerm(me, "MANAGE_ARTIFACTS") || sameEmail(me.email, info.creator));
  const isArchived = !!info.deleted_at;
  // Comments only attach to documents you can annotate: TEXT with textual
  // content, or a PACKAGE (its HTML files get the in-page overlay). Never for
  // ATTACHMENT uploads or non-text content (pdf, image, binary) — there's no
  // prose to anchor to, so the controls are turned off entirely.
  const commentable =
    info.artifact_type === "PACKAGE" ||
    (info.artifact_type === "TEXT" && isTextualContentType(info.content_type));
  // The owner's per-doc switch, layered on top of that. Absent (an older or
  // cached payload) reads as ON — the pre-existing behavior — so a missing
  // field can never silently strip comments off every artifact.
  const commentsAllowed = info.comments_enabled !== false;
  const commentsEnabled =
    !isArchived && !editing && !comparing && commentable && commentsAllowed;

  // Edit publishes a new version, which the server gates on WRITE access
  // (checkWriteAccess), no longer read == write. Prefer the server's
  // authoritative per-caller `can_write` (set on viewer responses; accounts for
  // creator/admin/allowed_write/groups/idp). Fall back to a conservative
  // heuristic only if it's absent (non-viewer contexts): mirror mode
  // (allowed_write == null) keeps the old read==write behavior; a set
  // allowed_write restricts to creators/admins.
  const editable =
    isEditableArtifact(info) &&
    (info.can_write ?? (canEdit || info.allowed_write == null));
  // Compare — same eligibility as Edit; the compare view fetches the version
  // list itself and reports "only one version" on open (no pre-check here).
  const comparable = isComparableArtifact(info);
  const [headerCollapsed, setHeaderCollapsed] = useState(false);
  const [archiving, setArchiving] = useState(false);
  const [actionErr, setActionErr] = useState<string>("");
  const [commentsBusy, setCommentsBusy] = useState(false);
  // Flip the per-doc comment switch. router.refresh() re-fetches the server
  // payload, which is what actually mounts/unmounts the overlay — we don't
  // mirror the flag in local state, so the menu can never disagree with what
  // the server will enforce.
  const toggleComments = async () => {
    if (!info.can_manage_comments || commentsBusy) return;
    setCommentsBusy(true);
    setActionErr("");
    try {
      await updateArtifactCommentsEnabled(info.artifact_id, !commentsAllowed);
      router.refresh();
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : String(e));
    } finally {
      setCommentsBusy(false);
    }
  };
  const doArchive = async () => {
    if (!canEdit) return;
    const ok = window.confirm(
      isArchived
        ? `Unarchive "${info.title}"? It will reappear in the catalog.`
        : `Archive "${info.title}"? It will disappear from the catalog but can be restored from the Archived page.`,
    );
    if (!ok) return;
    setArchiving(true);
    setActionErr("");
    try {
      if (isArchived) {
        await unarchiveArtifact(info.artifact_id);
      } else {
        await archiveArtifact(info.artifact_id);
      }
      // Archive bounces back to the catalog root; unarchive stays on
      // the viewer so the user sees the restored state.
      if (!isArchived) router.push("/");
      setArchiving(false);
      router.refresh();
    } catch (e) {
      setActionErr(e instanceof Error ? e.message : String(e));
      setArchiving(false);
    }
  };

  // APP artifacts ARE packages (browsable file tree) — they just also get the
  // "Visit app" launcher below. So package treatment covers PACKAGE + APP.
  const isPackage = (info.artifact_type === "PACKAGE" || info.artifact_type === "APP") && manifest;
  // Diagrams are TEXT artifacts whose body happens to be a canvas document:
  // same versioning/edit/compare plumbing, different renderer.
  const isDiagram = info.artifact_type === "TEXT" && isDiagramContentType(info.content_type);
  // A diagram renders at full width wherever it appears — reading it in a narrow
  // column just scales the picture down. The markdown editor's Split layout wants
  // the same thing, for the same reason: two panes in a reading column are two
  // cramped panes. Both are forced transiently, the way compare mode does it, so
  // neither writes to the persisted per-doc width — a reader who prefers Narrow
  // prose still gets Narrow prose on the next markdown doc.
  const forcedWide = isDiagram || (editing && editorSplit);
  const effectiveWidth: Width = forcedWide ? "wide" : comparing ? compareWidth : width;
  const selectedInPkg = mode.kind === "package" ? mode.selected : null;

  // Permalink: stable, version-pinned, slug-free URL anyone can quote.
  const permalink = `/a/${info.artifact_id}`;
  const permalinkAbs =
    typeof window !== "undefined"
      ? new URL(permalink, window.location.origin).toString()
      : permalink;

  // APP artifacts get a "Visit app" launcher → the full-page running app at
  // /app/{slug}/{version} (or /app/{uuid} when slugless), pinned to this version.
  const visitHref =
    info.artifact_type !== "APP"
      ? undefined
      : info.named_slug
        ? info.version != null
          ? `/app/${info.named_slug}/${info.version}`
          : `/app/${info.named_slug}`
        : `/app/${info.artifact_id}`;

  // "Full Page" target — the chrome-less view of the selected payload. Unified
  // on a slug-relative `?v=full[&file=<path>]` URL that resolves against the page
  // we're already on (/s/<slug>/<v> or /a/<uuid>): single-file artifacts and
  // package files (any dir) share ONE shape, it stays slug/version-friendly (no
  // UUID), and pinned-version vs "latest" follows from the current URL.
  // FullPageView renders every kind correctly (HTML/markdown/code from the
  // viewer, image/pdf/binary from the per-file bytes URL), so we no longer fork
  // to a raw /api/ URL per content type.
  const isFullPageable = (ct: string) =>
    isHTML(ct) || isMarkdown(ct) || isPlainCode(ct) || isDiagramContentType(ct);
  // APP is the one type with no Full Page: "Visit app" already opens the
  // running app chrome-less at /app/{ident}, so a second button pointing at a
  // near-identical view is just two names for one thing. ViewerToolbar puts
  // "Visit app" in the slot Full Page would have occupied.
  let effectiveFullHref: string | undefined;
  if (info.artifact_type === "APP") {
    effectiveFullHref = undefined;
  } else if (isPackage && selectedInPkg) {
    effectiveFullHref = `?v=full&file=${encodeURIComponent(selectedInPkg)}`;
  } else if (!isPackage && (isFullPageable(info.content_type) || isPDF(info.content_type) || isImage(info.content_type))) {
    effectiveFullHref = `?v=full`;
  }

  // Download target: an individual file when one is selected inside a
  // PACKAGE, otherwise the artifact body (a zip for PACKAGE, raw bytes
  // for single-file artifacts). Same-origin so the `download` attr is
  // honored by the browser.
  let downloadHref: string;
  let downloadName: string;
  let downloadLabel: string | undefined;
  // For PACKAGE artifacts always offer the whole-zip download as a
  // separate button (zipHref/zipName), so previewing a single file
  // doesn't hide the "grab the bundle" affordance.
  let zipHref: string | undefined;
  let zipName: string | undefined;
  if (isPackage && selectedInPkg) {
    downloadHref = `/api/artifacts/${info.artifact_id}/files/${encodeFilePath(selectedInPkg)}`;
    downloadName = selectedInPkg.split("/").pop() || selectedInPkg;
    downloadLabel = "Download file";
    zipHref = `/api/artifacts/${info.artifact_id}`;
    zipName = `${safeBaseName(info)}.zip`;
  } else if (isPackage) {
    // Package overview (no file selected): the single button IS the zip.
    downloadHref = `/api/artifacts/${info.artifact_id}`;
    downloadName = `${safeBaseName(info)}.zip`;
    downloadLabel = "Download zip";
  } else {
    downloadHref = `/api/artifacts/${info.artifact_id}`;
    const base = safeBaseName(info);
    const ext = extFromContentType(info.content_type);
    downloadName = ext ? `${base}.${ext}` : base;
  }

  return (
    <>
      {commentsEnabled ? <CommentsLayer artifactId={info.artifact_id} me={me} contentType={info.content_type} artifactType={info.artifact_type} fullPage={false} fullPageHref={fullHref ?? "?v=full"} /> : null}
      <div data-arti-topbar className="sticky top-12 z-20 border-b border-neutral-200 bg-neutral-50/95 backdrop-blur supports-[backdrop-filter]:bg-neutral-50/80 md:top-0">
        <div className={`mx-auto ${WIDTH_CLASS.wide} px-6 pt-4 pb-2`}>
          {/* Row 1 — title with collapse toggle. */}
          <div className="flex flex-wrap items-center gap-2">
            <BackButton />
            <EditableTitle
              initial={info.title}
              canEdit={canEdit}
              isPackage={info.artifact_type === "PACKAGE"}
              onSave={async (next) => {
                const updated = await updateArtifactTitle(info.artifact_id, next);
                info.title = updated.title;
                router.refresh();
              }}
              onCollapse={() => setHeaderCollapsed((c) => !c)}
            />
            <span className="ml-auto">
              <CollapseToggle
                collapsed={headerCollapsed}
                onClick={() => setHeaderCollapsed((c) => !c)}
                label={headerCollapsed ? "expand header" : "collapse header"}
              />
            </span>
          </div>

          {!headerCollapsed ? (
            <>
              {/* Row 2 — type + content-type + permalink (left), creator + size + time (right). */}
              <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[11px] text-neutral-500">
                <span className="rounded bg-neutral-100 px-2 py-0.5 text-neutral-600">
                  {info.artifact_type}
                </span>
                <span>{info.content_type}</span>
                <PermalinkChip href={permalink} copy={permalinkAbs} />
                {info.named_slug ? <SlugChip slug={info.named_slug} version={info.version} /> : null}
                <span className="ml-auto flex items-center gap-1">
                  <CreatorName email={info.creator} />
                  {info.size_bytes != null ? (
                    <span className="text-neutral-400" title={`${info.size_bytes} bytes`}>
                      {" \u00b7 "}
                      {formatBytes(info.size_bytes)}
                    </span>
                  ) : null}
                  <span className="text-neutral-400" title={info.modified_at || info.created_at}>
                    {" \u00b7 "}
                    {relativeTime(info.modified_at || info.created_at)}
                  </span>
                </span>
              </div>

              {/* Row 3 — labels first, then scopes. Sans-serif on purpose:
                  these are human-facing tags, not identifiers like the
                  mono UUID/slug chips in row 2. */}
              {info.scopes.length > 0 || info.labels.length > 0 || canEdit ? (
                <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[11px]">
                  {info.labels.length > 0 || canEdit ? (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <LabelEditor
                        artifactID={info.artifact_id}
                        labels={info.labels}
                        canEdit={canEdit}
                        onSaved={() => router.refresh()}
                      />
                    </div>
                  ) : null}
                  {info.scopes.length > 0 || canEdit ? (
                    <div className="flex flex-wrap items-center gap-1.5">
                      <ScopeEditor
                        artifactID={info.artifact_id}
                        scopes={info.scopes}
                        canEdit={canEdit}
                        onSaved={() => router.refresh()}
                      />
                    </div>
                  ) : null}
                </div>
              ) : null}

              {/* Row 4 — view controls + access + three-dot menu. */}
              <div className="mt-2 flex flex-wrap items-center gap-3">
                <ViewerToolbar
                  width={effectiveWidth}
                  setWidth={comparing ? setCompareWidth : setWidth}
                  textSize={textSize}
                  setTextSize={setTextSize}
                  visitHref={visitHref}
                  fullHref={effectiveFullHref}
                  // Inert while something forces full width — hidden rather
                  // than left doing nothing.
                  showWidth={!forcedWide}
                />
                <span className="ml-auto" />
                {/* Raw is meaningless while the editor (which IS the raw
                    source) or the compare view has replaced the body — hide it
                    to avoid a dead toggle. */}
                {!editing && !comparing ? <RawToggle original={original} setOriginal={setOriginal} /> : null}
                <AccessButton
                  artifactID={info.artifact_id}
                  access={info.allowed_access}
                  write={info.allowed_write}
                  hasOtherVersions={!!info.named_slug}
                  canEdit={canEdit}
                  canShare={!!info.can_share}
                  onSaved={() => router.refresh()}
                />
                <ThreeDotsMenu
                  downloadHref={downloadHref}
                  downloadName={downloadName}
                  downloadLabel={downloadLabel}
                  zipHref={zipHref}
                  zipName={zipName}
                  canEdit={canEdit}
                  isArchived={isArchived}
                  archiving={archiving}
                  onArchive={doArchive}
                  archiveTitle={
                    hasPerm(me, "MANAGE_ARTIFACTS") && !sameEmail(me?.email, info.creator)
                      ? (isArchived ? "unarchive (admin override)" : "archive (admin override)")
                      : (isArchived ? "unarchive" : "archive")
                  }
                  onUploadVersion={uploadTarget ? () => upload.open() : undefined}
                  onEdit={editable ? () => { setComparing(false); setEditing(true); } : undefined}
                  editTitle={
                    isDiagram
                      ? "open the diagram canvas — saving creates a new version"
                      : undefined
                  }
                  onCompare={comparable ? () => { setEditing(false); setCompareWidth("wide"); setComparing(true); } : undefined}
                  // Only offered where comments are possible at all — an
                  // attachment or a binary has nothing to anchor a comment to,
                  // so a switch there would promise something that can't happen.
                  commentsOn={commentsAllowed}
                  canManageComments={info.can_manage_comments}
                  commentsBusy={commentsBusy}
                  onToggleComments={commentable ? toggleComments : undefined}
                  onShare={() => setSharing(true)}
                />
                {sharing ? (
                  <ShareModal
                    artifactID={info.artifact_id}
                    // The page's own URL, resolved in the browser so it carries
                    // the real host rather than a guess at it.
                    pageURL={typeof window === "undefined" ? "" : window.location.href}
                    version={info.version}
                    hasSlug={!!info.named_slug}
                    // The server's own verdict. canEdit is a client-side
                    // re-derivation off `creator`, which versioning reassigns —
                    // a delegated writer would see a control the server 403s.
                    canShare={!!info.can_share}
                    onClose={() => setSharing(false)}
                  />
                ) : null}
              </div>
            </>
          ) : null}
          {actionErr ? (
            <div className="mt-1 text-[11px] text-rose-600">error: {actionErr}</div>
          ) : null}
        </div>
      </div>

      {isPackage ? (
        <PackageBody
          info={info}
          manifest={manifest}
          width={width}
          textScale={textScale}
          original={original}
          selected={selectedInPkg}
        />
      ) : (
        <section
          data-arti-doc
          className={`mx-auto ${WIDTH_CLASS[effectiveWidth]} px-6 py-8`}
          style={{ "--arti-text-scale": textScale } as React.CSSProperties}
        >
          {!isTextualContentType(info.content_type) ? (
            // Non-text bytes (pdf, image, zip, binary): render inline (PDF via
            // the browser's native viewer, images via <img>) or offer a
            // download — never dump raw bytes into a <pre>. Covers ATTACHMENT
            // uploads and any legacy TEXT artifact mis-typed with a non-textual
            // content_type.
            <NonTextBody
              ct={info.content_type}
              src={downloadHref}
              name={downloadName}
              size={info.size_bytes}
            />
          ) : comparing ? (
            // Side-by-side version diff. Keyed by artifact so a client-side
            // navigation while comparing remounts it fresh against the new
            // artifact's versions (same reasoning as the editor below).
            <ArtifactCompare
              key={info.artifact_id}
              info={info}
              body={body ?? ""}
              onClose={() => setComparing(false)}
            />
          ) : editing ? (
            // Keyed by artifact so a client-side navigation while editing
            // remounts the editor fresh: the draft (`text` state) from one
            // artifact can never survive a render against another's props —
            // the effect-based setEditing(false) reset above runs a render
            // too late to guarantee that on its own.
            isDiagram ? (
              <DiagramArtifactEditor
                key={info.artifact_id}
                info={info}
                body={body ?? ""}
                onClose={() => setEditing(false)}
              />
            ) : (
              <ArtifactEditor
                key={info.artifact_id}
                info={info}
                body={body ?? ""}
                onClose={() => {
                  setEditing(false);
                  // Don't leave a stale Split flag behind. `editing &&` already
                  // keeps it from affecting the width once the editor is closed,
                  // but clearing it means reopening can't briefly force wide on
                  // the strength of a previous session's layout.
                  setEditorSplit(false);
                }}
                onSplitChange={setEditorSplit}
              />
            )
          ) : isDiagram && !original ? (
            // A diagram's stored body is JSON, but its rendered form is the
            // picture. "Raw Source" still shows the JSON, same as any other
            // text artifact.
            <DiagramView
              body={body ?? ""}
              title={info.title}
              fileName={info.named_slug || info.title || "diagram"}
            />
          ) : original ? (
            <RawBody body={body ?? ""} />
          ) : (
            <RenderedBody
              body={body ?? ""}
              ct={info.content_type}
              // Standalone HTML loads via `src` (arti-served) so the comments
              // overlay is injected in-page, same as a PACKAGE file. Non-HTML
              // ignores src and renders from `body`.
              src={isHTML(info.content_type) ? `/api/artifacts/${info.artifact_id}` : undefined}
              docKey={info.artifact_id}
            />
          )}
        </section>
      )}
    </>
  );
}

function PackageBody({
  info,
  manifest,
  width,
  textScale,
  original,
  selected,
}: {
  info: ArtifactInfo;
  manifest?: PackageManifest;
  width: ReturnType<typeof useViewerPrefs>["width"];
  textScale: number;
  original: boolean;
  selected: string | null;
}) {
  const [fileBody, setFileBody] = useState<string | null>(null);
  const [fileCT, setFileCT] = useState<string>("");

  // The manifest records each file's content_type at upload time — the same
  // value arti serves. Reading it here lets us decide how to render BEFORE
  // fetching: non-text bytes (image, pdf, binary) must not be pulled in as
  // text (that's the garbled "‰PNG…" dump). When the manifest doesn't know the
  // type (empty), fall back to the old fetch-as-text path.
  const entry = selected ? manifest?.entries.find((e) => e.path === selected) : undefined;
  const entryCT = entry?.content_type ?? "";
  const nonText = entryCT !== "" && !isTextualContentType(entryCT);
  const fileUrl = selected
    ? `/api/artifacts/${info.artifact_id}/files/${encodeFilePath(selected)}`
    : "";

  useEffect(() => {
    // Non-text files render straight from the per-file URL (an <img>/<iframe>);
    // no body fetch needed, and reading their bytes as text is meaningless.
    if (!selected || nonText) {
      setFileBody(null);
      return;
    }
    // Via fetchPackageFile rather than a bare fetch: this used to skip the
    // response.ok check entirely, so a 403/404 put the server's error envelope
    // into fileBody and RENDERED IT AS THE FILE'S CONTENTS. fetchPackageFile
    // throws an ArtiError carrying just the `detail`, which lands in the catch
    // below and is shown as an error instead of as content.
    //
    // `contentType || entryCT` (not `??`): the helper returns "" when the
    // response carries no content-type, and an empty string must fall back to
    // the manifest's type too.
    //
    // Cancellation guard so switching files quickly cannot let a slower earlier
    // response overwrite the newer file's body — same convention as
    // ArtifactCompare.
    let cancelled = false;
    fetchPackageFile(info.artifact_id, selected)
      .then(({ body, contentType }) => {
        if (cancelled) return;
        setFileCT(contentType || entryCT);
        setFileBody(body);
      })
      .catch((e) => {
        if (cancelled) return;
        setFileBody("(error: " + (e as Error).message + ")");
      });
    return () => {
      cancelled = true;
    };
  }, [selected, nonText, entryCT, info.artifact_id]);

  // For HTML files inside a PACKAGE we render the iframe via `src` (the
  // per-file API URL) instead of srcDoc, so the document loads with a
  // real origin — relative refs resolve and embedded scripts (slides
  // pagination, etc.) run.
  const fileSrc = selected && isHTML(entryCT || fileCT) ? fileUrl : undefined;

  return (
    <section
      className={`mx-auto ${WIDTH_CLASS[width]} px-6 py-8`}
      style={{ "--arti-text-scale": textScale } as React.CSSProperties}
    >
      {selected ? (
        // Thin header strip identifying the file being viewed inside the
        // package — full path (truncated, hover for the rest) + its type.
        <div className="mb-3 flex items-center gap-3 border-b border-neutral-100 pb-2 text-[11px] text-neutral-500">
          <span className="min-w-0 flex-1 truncate font-mono" title={selected}>
            {selected}
          </span>
          {entryCT || fileCT ? (
            <span className="shrink-0 rounded bg-neutral-100 px-1.5 py-0.5 text-neutral-600">
              {(entryCT || fileCT).split(";")[0]}
            </span>
          ) : null}
        </div>
      ) : null}
      {!selected ? (
        <div className="flex min-h-[50vh] items-center justify-center px-6 text-center">
          <p className="text-sm text-neutral-400">
            Select a file from the side panel to view it.
          </p>
        </div>
      ) : nonText ? (
        // Image/pdf/binary: render inline (image via <img>, pdf via the
        // browser's native viewer) or offer a download — never dump raw bytes
        // into a <pre>. Mirrors the standalone non-text path above; the
        // per-file URL serves the right Content-Type so <img>/<iframe> work.
        <NonTextBody
          ct={entryCT}
          src={fileUrl}
          name={selected.split("/").pop() || selected}
          size={entry?.size ?? null}
        />
      ) : fileBody === null ? (
        <div className="text-neutral-400">loading…</div>
      ) : original ? (
        <RawBody body={fileBody} />
      ) : isDiagramContentType(entryCT || fileCT) ? (
        <DiagramView
          body={fileBody}
          title={selected.split("/").pop() || selected}
          fileName={selected.split("/").pop() || "diagram"}
        />
      ) : (
        <RenderedBody body={fileBody} ct={fileCT} src={fileSrc} allowPopups={info.artifact_type === "APP"} docKey={`${info.artifact_id}:${selected}`} />
      )}
    </section>
  );
}
