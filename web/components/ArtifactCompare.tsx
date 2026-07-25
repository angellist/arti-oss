"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import type { ArtifactInfo } from "@/lib/types";
import { fetchContent, listVersions } from "@/lib/arti";
import { computeLineDiff, rowKinds, type DiffCell, type DiffResult, type SideKind } from "@/lib/diff";
import { relativeTime } from "@/lib/time";
import { formatBytes } from "@/lib/format";

// ArtifactCompare — the read-only side-by-side version diff entered via the
// ⋯ menu's "Compare versions" item (TEXT artifacts with a slug; see
// isComparableArtifact). Replaces the body area with two version pickers over a
// GitHub-split-style line diff. Fetches the slug's version list on open (so the
// menu item needs no pre-check) and each side's raw body on demand (cached; the
// currently-viewed version's body is seeded in so that side never re-fetches).
// Purely for viewing — nothing here writes a new version.
export default function ArtifactCompare({
  info,
  body,
  onClose,
}: {
  info: ArtifactInfo;
  body: string;
  onClose: () => void;
}) {
  const slug = info.named_slug ?? "";
  const [versions, setVersions] = useState<ArtifactInfo[] | null>(null);
  const [loadErr, setLoadErr] = useState<string>("");
  // Raw bodies keyed by artifact_id, seeded with the version we already have.
  const [bodies, setBodies] = useState<Record<string, string>>({ [info.artifact_id]: body });
  const [leftId, setLeftId] = useState<string>("");
  const [rightId, setRightId] = useState<string>("");
  // Guards for the on-demand body loader: which ids have a fetch in flight
  // (dedupe) and whether we're still mounted (no setState after Close).
  const inFlight = useRef<Set<string>>(new Set());
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Fetch the slug's version list on open. Sorted newest-first for the
  // dropdowns; defaults put the older version on the left and the version
  // you're viewing on the right (old → new), so removals read on the left and
  // additions on the right. If the viewed version is the oldest, compare it
  // against the next-newer instead so the default pair is never identical.
  useEffect(() => {
    let cancelled = false;
    listVersions(slug)
      .then((res) => {
        if (cancelled) return;
        const sorted = [...res.versions].sort((a, b) => (b.version ?? 0) - (a.version ?? 0));
        setVersions(sorted);
        if (sorted.length === 0) return;
        if (sorted.length === 1) {
          setLeftId(sorted[0].artifact_id);
          setRightId(sorted[0].artifact_id);
          return;
        }
        const idx = sorted.findIndex((v) => v.artifact_id === info.artifact_id);
        if (idx === -1) {
          // Viewed version not in the readable list (unexpected) — top two.
          setLeftId(sorted[1].artifact_id);
          setRightId(sorted[0].artifact_id);
        } else if (idx < sorted.length - 1) {
          // An older version exists (higher index, since sorted desc).
          setLeftId(sorted[idx + 1].artifact_id);
          setRightId(sorted[idx].artifact_id);
        } else {
          // Viewed version is the oldest — compare against the next-newer.
          setLeftId(sorted[idx].artifact_id);
          setRightId(sorted[idx - 1].artifact_id);
        }
      })
      .catch((e) => {
        if (!cancelled) setLoadErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [slug, info.artifact_id]);

  // Load each selected version's raw body on demand (cached; deduped via the
  // in-flight ref so re-runs never double-fetch). Errors are stored as the body
  // so a failed side shows the message inline rather than hanging. Reads the
  // current `bodies` at run time to skip already-cached ids without needing it
  // as a dependency (which would churn the effect on every cache fill).
  useEffect(() => {
    for (const id of [leftId, rightId]) {
      if (!id || bodies[id] !== undefined || inFlight.current.has(id)) continue;
      inFlight.current.add(id);
      fetchContent(id)
        .then((r) => {
          if (mounted.current) setBodies((m) => ({ ...m, [id]: r.body }));
        })
        .catch((e) => {
          if (mounted.current)
            setBodies((m) => ({ ...m, [id]: `(error loading: ${e instanceof Error ? e.message : String(e)})` }));
        })
        .finally(() => inFlight.current.delete(id));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [leftId, rightId]);

  const leftBody = bodies[leftId];
  const rightBody = bodies[rightId];
  const diff: DiffResult | null = useMemo(() => {
    if (leftBody === undefined || rightBody === undefined) return null;
    return computeLineDiff(leftBody, rightBody);
  }, [leftBody, rightBody]);

  // ---- render states -------------------------------------------------------

  if (loadErr) {
    return (
      <Notice>
        <span className="text-rose-700">Couldn&apos;t load versions:</span> {loadErr}
        <CloseLink onClose={onClose} />
      </Notice>
    );
  }
  if (versions === null) {
    return <Notice>Loading versions…</Notice>;
  }
  if (versions.length <= 1) {
    return (
      <Notice>
        This artifact has only one version — there&apos;s nothing to compare.
        <CloseLink onClose={onClose} />
      </Notice>
    );
  }

  return (
    <div>
      {/* Control bar — the two version pickers centered so the arrow sits over
          the pane divider (equal 1fr columns flank the auto-width arrow), the
          +/- summary trailing the right picker, and Close pushed to the edge. */}
      <div className="mb-3 grid grid-cols-[1fr_auto_1fr] items-center gap-2 rounded-md border border-blue-200 bg-blue-50/60 px-3 py-2 text-[12px]">
        <div className="flex items-center justify-end">
          <VersionSelect value={leftId} onChange={setLeftId} versions={versions} side="left" />
        </div>
        <span className="px-1 text-neutral-400" aria-hidden="true">
          →
        </span>
        <div className="flex items-center gap-2">
          <VersionSelect value={rightId} onChange={setRightId} versions={versions} side="right" />
          {diff ? (
            diff.truncated ? (
              <span className="font-medium text-amber-700">too large to align — showing raw</span>
            ) : diff.adds === 0 && diff.dels === 0 ? (
              <span className="text-neutral-500">(identical)</span>
            ) : (
              <span className="tabular-nums">
                <span className="text-emerald-700">+{diff.adds}</span>{" "}
                <span className="text-rose-700">−{diff.dels}</span>
              </span>
            )
          ) : null}
          <button
            type="button"
            onClick={onClose}
            className="ml-auto rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
          >
            Close
          </button>
        </div>
      </div>

      {diff === null ? (
        <div className="text-neutral-400">loading…</div>
      ) : (
        <DiffTable diff={diff} />
      )}
    </div>
  );
}

// VersionSelect — a native <select> over every readable version, newest first.
function VersionSelect({
  value,
  onChange,
  versions,
  side,
}: {
  value: string;
  onChange: (id: string) => void;
  versions: ArtifactInfo[];
  side: "left" | "right";
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      aria-label={`${side} version`}
      className="rounded-md border border-neutral-200 bg-white px-2 py-1 text-[12px] text-neutral-800 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
    >
      {versions.map((v) => (
        <option key={v.artifact_id} value={v.artifact_id}>
          {v.version != null ? `v${v.version}` : "—"}
          {" · "}
          {relativeTime(v.modified_at || v.created_at)}
          {v.size_bytes != null ? ` · ${formatBytes(v.size_bytes)}` : ""}
        </option>
      ))}
    </select>
  );
}

// DiffTable renders aligned rows as an HTML table so both panes' rows stay
// row-locked (a table shares column tracks across all rows). table-fixed with a
// colgroup pins the layout to the container: two fixed-width line gutters flank
// two equal-width (auto → 50/50) text columns, so a long line on one side can't
// blow that pane out and shove the other off-screen. Text cells wrap
// (whitespace-pre-wrap keeps indentation; break-words snaps unbreakable tokens)
// so a logical line reads as several visual lines instead of forcing a
// horizontal scroll. Font-size is calc()'d against the viewer's inherited
// `--arti-text-scale` (same var the width/size controls publish), so the
// text-size control drives the diff too; the gutter is pinned to 12px so line
// numbers stay compact regardless of scale. Rose = removed (left), emerald =
// added (right), neutral filler on the empty side. Each side's kind comes from
// rowKinds so a both-present-but-differing row (only the truncated fallback
// produces these) tints as a change rather than showing neutral.
function DiffTable({ diff }: { diff: DiffResult }) {
  return (
    <div className="overflow-x-auto rounded-md border border-neutral-200 bg-white">
      <table
        className="w-full table-fixed border-collapse font-mono leading-relaxed"
        style={{ fontSize: "calc(12px * var(--arti-text-scale, 1))" }}
      >
        <colgroup>
          <col className="w-14" />
          <col />
          <col className="w-14" />
          <col />
        </colgroup>
        <tbody>
          {diff.rows.map((r, i) => {
            const k = rowKinds(r);
            return (
              <tr key={i}>
                <DiffPane cell={r.left} kind={k.left} side="left" />
                <DiffPane cell={r.right} kind={k.right} side="right" />
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// DiffPane renders one side (number gutter + text) of a diff row as two <td>s,
// tinted per its precomputed kind (see rowKinds).
function DiffPane({
  cell,
  kind,
  side,
}: {
  cell: DiffCell | null;
  kind: SideKind;
  side: "left" | "right";
}) {
  const numCls =
    kind === "del"
      ? "bg-rose-100 text-rose-400"
      : kind === "add"
        ? "bg-emerald-100 text-emerald-400"
        : kind === "filler"
          ? "bg-neutral-50"
          : "text-neutral-300";
  const textCls =
    kind === "del"
      ? "bg-rose-50 text-rose-900"
      : kind === "add"
        ? "bg-emerald-50 text-emerald-900"
        : kind === "filler"
          ? "bg-neutral-50"
          : "text-neutral-800";
  // A right border after the left text cell splits the two panes.
  const divider = side === "left" ? " border-r border-neutral-200" : "";
  return (
    <>
      <td className={`select-none px-2 text-right align-top text-[12px] tabular-nums ${numCls}`}>
        {cell ? cell.num : ""}
      </td>
      <td className={`whitespace-pre-wrap break-words px-3 align-top${divider} ${textCls}`}>
        {cell ? <CellText cell={cell} kind={kind} /> : ""}
      </td>
    </>
  );
}

// CellText renders a diff cell's text. On a paired changed row the cell carries
// inline word-level segments (see computeInlineSegments), so only the words that
// actually differ get a deeper tint — rose on the removed side, emerald on the
// added side — while the shared words stay flat. Rows without segmentation
// (equal lines, lone insertions/deletions, the truncated fallback) render the
// plain line, with a single space standing in for a blank line so the row keeps
// its height.
function CellText({ cell, kind }: { cell: DiffCell; kind: SideKind }) {
  if (!cell.segments || cell.segments.length === 0) {
    return <>{cell.text === "" ? " " : cell.text}</>;
  }
  const hl = kind === "del" ? "rounded-[2px] bg-rose-200" : "rounded-[2px] bg-emerald-200";
  return (
    <>
      {cell.segments.map((s, i) =>
        s.changed ? (
          <span key={i} className={hl}>
            {s.text}
          </span>
        ) : (
          <span key={i}>{s.text}</span>
        ),
      )}
    </>
  );
}

// Notice is the small centered card used for the loading / error / single-
// version states (no diff to show).
function Notice({ children }: { children: React.ReactNode }) {
  return (
    <div className="mx-auto max-w-lg rounded-md border border-neutral-200 bg-neutral-50 px-6 py-8 text-center text-[13px] text-neutral-600">
      {children}
    </div>
  );
}

function CloseLink({ onClose }: { onClose: () => void }) {
  return (
    <div className="mt-3">
      <button
        type="button"
        onClick={onClose}
        className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
      >
        Close
      </button>
    </div>
  );
}
