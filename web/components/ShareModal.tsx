"use client";

import { useCallback, useEffect, useState } from "react";
import { createPortal } from "react-dom";

import { listShares, mintShare, revokeShare } from "@/lib/arti";
import type { ShareLink } from "@/lib/types";

// ShareModal — the ⋯ menu's Share item. Two halves that do very different
// things, which is why they are visually separated rather than merged:
//
//  1. Copy this page's link. Works only for people who can sign in to arti.
//     Grants nothing, so everyone who can see the document gets it.
//  2. Mint an external timed link. A capability URL that anyone holding it can
//     open with no account at all, until it expires or is revoked. Owner-only,
//     and only when the server has the feature switched on.

const TTLS: { value: string; label: string }[] = [
  { value: "1h", label: "1 hour" },
  { value: "8h", label: "8 hours" },
  { value: "24h", label: "1 day" },
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
];

function relativeExpiry(iso: string, now: number): string {
  const ms = new Date(iso).getTime() - now;
  if (Number.isNaN(ms)) return iso;
  if (ms <= 0) return "expired";
  const hours = Math.round(ms / 3_600_000);
  if (hours < 48) return `in ${hours}h`;
  return `in ${Math.round(hours / 24)}d`;
}

// Expired and revoked links stay listed for a week: an owner asking "did this
// leak" needs recent history, not an empty table.
const DEAD_ROW_GRACE_MS = 7 * 24 * 3_600_000;

export function isVisibleRow(l: ShareLink, now = Date.now()): boolean {
  const dead = l.revoked_at ? new Date(l.revoked_at).getTime() : new Date(l.expires_at).getTime();
  if (Number.isNaN(dead)) return true;
  if (!l.revoked_at && dead > now) return true; // still live
  return now - dead < DEAD_ROW_GRACE_MS;
}

export function ShareModal({
  artifactID,
  pageURL,
  version,
  hasSlug,
  canShare,
  onClose,
}: {
  artifactID: string;
  pageURL: string;
  version?: number | null;
  hasSlug: boolean;
  // The server's `can_share`: the caller owns the document AND the feature is
  // on AND the document is shareable. One flag, computed once server-side —
  // splitting it into separate client-side conditions is how the two drift.
  canShare: boolean;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const [scope, setScope] = useState<"version" | "slug">("version");
  const [ttl, setTTL] = useState("24h");
  const [note, setNote] = useState("");
  const [minting, setMinting] = useState(false);
  const [mintedURL, setMintedURL] = useState<string | null>(null);
  const [mintedCopied, setMintedCopied] = useState(false);
  // The link list and the clock it was read against, captured together. One
  // timestamp for the whole render keeps "expired" consistent down the table,
  // and keeps Date.now() out of render, where it is not a pure call.
  const [loaded, setLoaded] = useState<{ links: ShareLink[]; at: number }>({ links: [], at: 0 });
  const [reloads, setReloads] = useState(0);
  const [err, setErr] = useState<string | null>(null);

  const external = canShare;
  const refresh = useCallback(() => setReloads((n) => n + 1), []);

  useEffect(() => {
    if (!external) return;
    let cancelled = false;
    listShares(artifactID)
      .then((rows) => {
        if (!cancelled) setLoaded({ links: rows, at: Date.now() });
      })
      .catch((e: unknown) => {
        if (!cancelled) setErr(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [artifactID, external, reloads]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  async function copy(text: string, mark: (v: boolean) => void) {
    try {
      await navigator.clipboard.writeText(text);
      mark(true);
      setTimeout(() => mark(false), 1500);
    } catch {
      setErr("could not write to the clipboard");
    }
  }

  async function doMint() {
    setMinting(true);
    setErr(null);
    try {
      const res = await mintShare(artifactID, scope, ttl, note.trim());
      setMintedURL(res.url);
      setNote("");
      refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setMinting(false);
    }
  }

  async function doRevoke(id: string) {
    setErr(null);
    try {
      await revokeShare(id);
      refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  const now = loaded.at;
  const visible = loaded.links.filter((l) => isVisibleRow(l, now));

  const body = (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/30 p-6"
      role="dialog"
      aria-modal="true"
      aria-label="Share"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="mt-16 w-full max-w-xl rounded-lg border border-neutral-200 bg-white p-5 shadow-xl">
        <h2 className="text-sm font-semibold text-neutral-800">Share</h2>

        {/* ── 1. the page URL ── */}
        <section className="mt-4">
          <h3 className="text-xs font-medium text-neutral-700">Copy this page&apos;s link</h3>
          <p className="mt-1 text-[11px] text-neutral-500">
            Opens for anyone who can sign in to arti and already has access to this document.
          </p>
          <div className="mt-2 flex items-center gap-2">
            <input
              readOnly
              value={pageURL}
              aria-label="page link"
              className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-neutral-50 px-2 py-1 font-mono text-[11px] text-neutral-700"
            />
            <button
              type="button"
              onClick={() => void copy(pageURL, setCopied)}
              className="shrink-0 rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
            >
              {copied ? "Copied" : "Copy link"}
            </button>
          </div>
        </section>

        {/* ── 2. external timed link ── */}
        {external ? (
          <section className="mt-6 border-t border-neutral-200 pt-4">
            <h3 className="text-xs font-medium text-neutral-700">External link</h3>
            <p className="mt-1 text-[11px] text-neutral-500">
              Opens for anyone holding the URL, with no AngelList account, until it expires or you
              revoke it.
            </p>

            <fieldset className="mt-3">
              <legend className="text-[11px] font-medium text-neutral-600">What it shows</legend>
              <label className="mt-1 flex items-start gap-2 text-xs text-neutral-700">
                <input
                  type="radio"
                  name="share-scope"
                  className="mt-0.5"
                  checked={scope === "version"}
                  onChange={() => setScope("version")}
                />
                <span>This version{version ? ` (v${version})` : ""}</span>
              </label>
              {hasSlug ? (
                <label className="mt-1.5 flex items-start gap-2 text-xs text-neutral-700">
                  <input
                    type="radio"
                    name="share-scope"
                    className="mt-0.5"
                    checked={scope === "slug"}
                    onChange={() => setScope("slug")}
                  />
                  <span>
                    Latest version, always
                    {/* Stated in body text, not a tooltip: a hazard that only
                        appears on hover is a hazard nobody reads. Writers are
                        named because publishing a version needs write access,
                        not ownership — so more people can change what an
                        external reader sees than can mint or revoke. */}
                    <span className="mt-0.5 block text-[11px] text-amber-700">
                      This link will also show versions published later, by anyone who can write to
                      this document — not only by you.
                    </span>
                  </span>
                </label>
              ) : null}
            </fieldset>

            <div className="mt-3 flex flex-wrap items-center gap-2">
              <span className="text-[11px] font-medium text-neutral-600">Expires</span>
              {TTLS.map((t) => (
                <button
                  key={t.value}
                  type="button"
                  onClick={() => setTTL(t.value)}
                  aria-pressed={ttl === t.value}
                  className={`rounded-md border px-2 py-1 text-xs transition ${
                    ttl === t.value
                      ? "border-neutral-800 bg-neutral-800 text-white"
                      : "border-neutral-200 bg-white text-neutral-700 hover:bg-neutral-50"
                  }`}
                >
                  {t.label}
                </button>
              ))}
            </div>

            <input
              value={note}
              onChange={(e) => setNote(e.target.value)}
              placeholder="note (optional) — e.g. Q3 audit"
              aria-label="note"
              className="mt-3 w-full rounded-md border border-neutral-200 px-2 py-1 text-xs text-neutral-700"
            />

            <button
              type="button"
              disabled={minting}
              onClick={() => void doMint()}
              className="mt-3 rounded-md bg-neutral-800 px-3 py-1.5 text-xs text-white transition hover:bg-neutral-700 disabled:opacity-50"
            >
              {minting ? "Creating…" : "Create external link"}
            </button>

            {mintedURL ? (
              <div className="mt-3 rounded-md border border-emerald-200 bg-emerald-50 p-3">
                <p className="text-[11px] font-medium text-emerald-900">
                  Copy this now — it will not be shown again.
                </p>
                <div className="mt-2 flex items-center gap-2">
                  <input
                    readOnly
                    value={mintedURL}
                    aria-label="external link"
                    className="min-w-0 flex-1 rounded-md border border-emerald-200 bg-white px-2 py-1 font-mono text-[11px] text-neutral-800"
                  />
                  <button
                    type="button"
                    onClick={() => void copy(mintedURL, setMintedCopied)}
                    className="shrink-0 rounded-md border border-emerald-300 bg-white px-3 py-1 text-xs text-emerald-900 transition hover:bg-emerald-100"
                  >
                    {mintedCopied ? "Copied" : "Copy"}
                  </button>
                </div>
              </div>
            ) : null}

            {visible.length ? (
              <table className="mt-4 w-full text-left text-[11px]">
                <thead className="text-neutral-500">
                  <tr>
                    <th className="py-1 font-medium">Link</th>
                    <th className="py-1 font-medium">Shows</th>
                    <th className="py-1 font-medium">Expires</th>
                    <th className="py-1 font-medium">Opens</th>
                    <th className="py-1" />
                  </tr>
                </thead>
                <tbody>
                  {visible.map((l) => {
                    const dead = !!l.revoked_at || new Date(l.expires_at).getTime() <= now;
                    return (
                      <tr key={l.id} className={dead ? "text-neutral-400" : "text-neutral-700"}>
                        <td className="py-1 font-mono">
                          {l.token_prefix}…
                          {l.note ? <span className="ml-1 font-sans italic">{l.note}</span> : null}
                        </td>
                        <td className="py-1">{l.scope === "slug" ? "latest" : "one version"}</td>
                        <td className="py-1">
                          {l.revoked_at ? "revoked" : relativeExpiry(l.expires_at, now)}
                        </td>
                        <td className="py-1">{l.open_count}</td>
                        <td className="py-1 text-right">
                          {dead ? null : (
                            <button
                              type="button"
                              onClick={() => void doRevoke(l.id)}
                              className="rounded border border-neutral-200 px-2 py-0.5 text-[11px] text-rose-700 transition hover:bg-rose-50"
                            >
                              Revoke
                            </button>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            ) : null}
          </section>
        ) : null}

        {err ? <p className="mt-3 text-[11px] text-rose-600">error: {err}</p> : null}

        <div className="mt-5 flex justify-end">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
          >
            Close
          </button>
        </div>
      </div>
    </div>
  );

  // Guard for non-DOM render contexts (SSR / the node-env test runner), where
  // there is no document to portal into — render the tree inline instead.
  // Same shape as AccessModal.
  if (typeof document === "undefined") return body;
  return createPortal(body, document.body);
}
