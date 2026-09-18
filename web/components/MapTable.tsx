"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { browseMap, getMapEntry, type MapBrowseResponse, type MapSortKey } from "@/lib/arti";

const PAGE_SIZE = 50;

const COLUMNS: { key: MapSortKey; label: string; className: string }[] = [
  { key: "key", label: "Key", className: "w-[26%] py-2 pl-6 pr-4 font-bold" },
  { key: "size", label: "Value", className: "py-2 pr-4 font-bold" },
  { key: "rev", label: "Rev", className: "w-14 py-2 pr-4 text-right font-bold" },
  { key: "updated", label: "Updated", className: "w-40 py-2 pr-6 font-bold" },
];

function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

// MapTable shows a MAP's LIVE HEAD, not the artifact body. /s/<slug> serves
// the latest frozen snapshot; head is what a reader means by "what is in this
// map", so the header says which one this is rather than leaving it implicit.
export default function MapTable({ slug, snapshotBytes }: { slug: string; snapshotBytes: number | null }) {
  const [q, setQ] = useState("");
  const [debouncedQ, setDebouncedQ] = useState("");
  const [sort, setSort] = useState<MapSortKey>("key");
  const [dir, setDir] = useState<"asc" | "desc">("asc");
  const [offset, setOffset] = useState(0);
  const [data, setData] = useState<MapBrowseResponse | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ key: string; text: string } | null>(null);

  // Debounce the search so a typed word is one request, not one per keystroke.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedQ(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);

  // A new search or sort must start at page 1, or the reader lands on an
  // offset that no longer exists and sees an empty table.
  const [lastQuery, setLastQuery] = useState({ q: "", sort, dir });
  if (lastQuery.q !== debouncedQ || lastQuery.sort !== sort || lastQuery.dir !== dir) {
    setLastQuery({ q: debouncedQ, sort, dir });
    setOffset(0);
  }

  // This subtree stays mounted across a client-side navigation, so without
  // this a second map opens carrying the first one's search, sort, page and
  // expanded rows — and reads as "this map is nearly empty" when it is really
  // a stale filter. Adjusted during render rather than in an effect so the
  // reset lands in the same commit as the new slug, and kept next to the state
  // it protects rather than as a `key` on the caller, which a later edit to
  // the viewer could drop silently. MapTable.test.tsx fails if it goes away.
  const [slugOfState, setSlugOfState] = useState(slug);
  if (slug !== slugOfState) {
    setSlugOfState(slug);
    setQ("");
    setDebouncedQ("");
    setSort("key");
    setDir("asc");
    setOffset(0);
    setDetail(null);
    setData(null);
    setError(null);
    setLastQuery({ q: "", sort: "key", dir: "asc" });
  }

  // "Loading" is the gap between the page the reader asked for and the page
  // that came back, not a flag an effect sets — setting one synchronously in
  // the effect body is what react-hooks/set-state-in-effect forbids.
  const reqKey = `${slug}\u0000${debouncedQ}\u0000${sort}\u0000${dir}\u0000${offset}`;
  const [loadedKey, setLoadedKey] = useState<string | null>(null);
  const loading = loadedKey !== reqKey;

  const load = useCallback(() => {
    let cancelled = false;
    browseMap(slug, { q: debouncedQ, sort, dir, limit: PAGE_SIZE, offset })
      .then((d) => {
        if (cancelled) return;
        setData(d);
        setError(null);
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message);
      })
      .finally(() => {
        if (!cancelled) setLoadedKey(reqKey);
      });
    return () => {
      cancelled = true;
    };
  }, [slug, debouncedQ, sort, dir, offset, reqKey]);

  useEffect(() => load(), [load]);

  const toggleSort = (k: MapSortKey) => {
    if (k === sort) {
      setDir(dir === "asc" ? "desc" : "asc");
    } else {
      setSort(k);
      setDir(k === "key" ? "asc" : "desc");
    }
  };

  // The table only ever holds a preview, so the detail view re-fetches the
  // entry whole rather than formatting the clipped string it was given. Two
  // quick clicks are two in-flight reads, and the slower one must not land on
  // top of the row the reader is actually looking at.
  const detailReq = useRef(0);
  const showValue = async (key: string) => {
    const req = ++detailReq.current;
    setDetail({ key, text: "loading…" });
    try {
      const e = await getMapEntry(slug, key);
      if (detailReq.current === req) setDetail({ key, text: JSON.stringify(e.value, null, 2) });
    } catch (err) {
      if (detailReq.current === req) setDetail({ key, text: `could not load: ${(err as Error).message}` });
    }
  };

  // Escape closes the value dialog — a modal that only a mouse can dismiss is
  // a trap for anyone on a keyboard.
  useEffect(() => {
    if (!detail) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setDetail(null);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [detail]);

  const rows = data?.rows ?? [];
  const total = data?.total ?? 0;
  const stats = data?.stats;

  return (
    <div className="text-[13px]">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-neutral-200 bg-white px-6 py-3">
        <div className="text-[12px] text-neutral-500">
          <span className="font-medium text-neutral-700">live head</span>
          {stats ? (
            <>
              {" · "}
              <span className="font-medium text-neutral-700">{stats.keys.toLocaleString()}</span>
              {" of "}
              {stats.max_keys.toLocaleString()} keys{" · "}
              <span className="font-medium text-neutral-700">{humanBytes(stats.bytes)}</span>
              {" of "}
              {humanBytes(stats.max_bytes)}
            </>
          ) : null}
          {" · "}
          {snapshotBytes ? (
            "Raw Source shows the frozen snapshot"
          ) : (
            // Head and the artifact body are different things, and until the
            // first snapshot the body is empty. Without this the reader hits
            // Raw Source, sees nothing, and reasonably concludes the data is
            // gone rather than unsnapshotted.
            <span className="text-amber-700">not snapshotted yet, so Raw Source is empty</span>
          )}
        </div>
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="Search keys and values…"
          aria-label="Search map entries"
          className="w-full max-w-xs rounded-md border border-neutral-200 bg-neutral-50 px-3 py-1.5 text-[12px] text-neutral-900 placeholder:text-neutral-400 focus:border-blue-400 focus:bg-white focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
      </div>

      {error ? (
        <div className="px-6 py-12 text-center text-[12px] text-neutral-400">{error}</div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full table-fixed bg-white text-[13px] leading-snug">
            <thead className="border-b border-neutral-200 bg-neutral-50 text-left text-[12px] text-neutral-700">
              <tr>
                {COLUMNS.map((c) => (
                  <th key={c.key} className={c.className}>
                    <button
                      type="button"
                      onClick={() => toggleSort(c.key)}
                      className="cursor-pointer select-none transition hover:text-neutral-900"
                    >
                      {c.label}
                      {sort === c.key ? (dir === "asc" ? " ▲" : " ▼") : ""}
                    </button>
                  </th>
                ))}
                <th className="w-40 py-2 pr-6 font-bold">By</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key} className="border-b border-neutral-100 align-top transition hover:bg-neutral-50/70">
                  <td className="max-w-0 truncate py-1.5 pl-6 pr-4 font-mono text-[12px] text-neutral-900" title={r.key}>{r.key}</td>
                  <td className="py-1.5 pr-4 font-mono text-[12px] text-neutral-600">
                    {/* Clamped rather than clipped to one line: a JSON object
                        cut at the column edge shows only its first field. Three
                        lines keep the table scannable; the whole value is one
                        click away. */}
                    <button
                      type="button"
                      onClick={() => showValue(r.key)}
                      title="Show formatted JSON"
                      className="line-clamp-3 w-full cursor-pointer break-all text-left hover:text-neutral-900"
                    >
                      {r.value}
                    </button>
                    {r.truncated ? (
                      <span className="text-[11px] text-neutral-400">{humanBytes(r.size_bytes)} · show all</span>
                    ) : null}
                  </td>
                  <td className="py-1.5 pr-4 text-right tabular-nums text-neutral-500">{r.rev}</td>
                  <td className="py-1.5 pr-6 text-[12px] text-neutral-500">{r.updated_at.replace("T", " ").replace(".000Z", "")}</td>
                  <td className="max-w-0 truncate py-1.5 pr-6 text-[12px] text-neutral-500" title={r.updated_by}>{r.updated_by}</td>
                </tr>
              ))}
              {rows.length === 0 && !loading ? (
                <tr>
                  <td colSpan={5} className="px-6 py-12 text-center text-neutral-400">
                    {debouncedQ ? `No entry matches “${debouncedQ}”.` : "This map has no entries yet."}
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </div>
      )}

      <nav className="flex items-center justify-between border-t border-neutral-200 bg-white px-6 py-2 text-[11px] text-neutral-500">
        <span>
          {total === 0 ? "0" : `${offset + 1}–${Math.min(offset + rows.length, total)}`} of{" "}
          <span className="font-medium text-neutral-700">{total.toLocaleString()}</span>
          {debouncedQ ? " matching" : ""}
          {loading ? " · loading…" : ""}
        </span>
        <span className="flex gap-3">
          <button
            type="button"
            disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            className="disabled:text-neutral-300 hover:text-neutral-900 disabled:hover:text-neutral-300"
          >
            ‹ prev
          </button>
          <button
            type="button"
            disabled={offset + rows.length >= total}
            onClick={() => setOffset(offset + PAGE_SIZE)}
            className="disabled:text-neutral-300 hover:text-neutral-900 disabled:hover:text-neutral-300"
          >
            next ›
          </button>
        </span>
      </nav>

      {detail ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label={`Value of ${detail.key}`}
          onClick={() => setDetail(null)}
          className="fixed inset-0 z-50 flex items-center justify-center bg-neutral-900/40 p-6"
        >
          <div
            onClick={(e) => e.stopPropagation()}
            className="flex max-h-[80vh] w-full max-w-4xl flex-col overflow-hidden rounded-lg border border-neutral-200 bg-white shadow-xl"
          >
            <div className="flex items-center justify-between gap-4 border-b border-neutral-200 px-4 py-2">
              <span className="truncate font-mono text-[12px] text-neutral-900">{detail.key}</span>
              <button
                type="button"
                onClick={() => setDetail(null)}
                className="shrink-0 text-[12px] text-neutral-500 hover:text-neutral-900"
              >
                close
              </button>
            </div>
            <pre className="overflow-auto whitespace-pre-wrap break-all p-4 font-mono text-[12px] text-neutral-800">
              {detail.text}
            </pre>
          </div>
        </div>
      ) : null}
    </div>
  );
}
