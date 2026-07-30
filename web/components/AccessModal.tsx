"use client";

import { useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";

import { listGroups, listIdpGroups, updateArtifactAccess } from "@/lib/arti";
import type { Group, IdpGroup } from "@/lib/types";

type Level = "read" | "write";

// A principal row: one access token plus its resolved level. `write` level
// means the token is in allowed_write (may push versions / append / edit);
// `read` means read-only.
interface Row {
  token: string;
  level: Level;
}

// levelsFor derives the initial per-principal levels from the artifact's
// (access, write) pair. When `write` is null/undefined the artifact is in the
// back-compat "write follows read" state, so every reader is also a writer.
// Otherwise a token is a writer iff it appears in `write` — crucially, an empty
// (but non-null) `write` means creator-only, so every listed reader is Read.
// Exported for direct unit testing of exactly that null-vs-[] distinction.
export function levelsFor(access: string[], write: string[] | null | undefined): Row[] {
  const writers = new Set((write ?? access).map((t) => t));
  return access.map((token) => ({ token, level: writers.has(token) ? "write" : "read" }));
}

// principalLabel resolves a token to human copy. `*` is rendered from a
// config-neutral phrase (never a hardcoded tenant name); groups/idp get a
// badge; emails and globs render as-is.
function principalKind(token: string): "everyone" | "domain" | "group" | "idp" | "email" {
  if (token === "*") return "everyone";
  if (token.toLowerCase().startsWith("group:")) return "group";
  if (token.toLowerCase().startsWith("idp:")) return "idp";
  if (/^\*@[^*?]+$/.test(token)) return "domain";
  return "email";
}

// AccessModal is the centered access editor: one row per principal with a
// Read | Read & write control, plus an add-row typeahead over emails, manual
// groups, and captured IdP (SSO) groups. Read-only for non-editors. Replaces
// the old anchored AccessPopover.
// It runs in two modes. Against an existing artifact it PATCHes on every edit
// (the default). On a "New artifact" page there is no artifact to PATCH yet, so
// the caller passes `draft` + `onCommit` and edits land in the caller's draft
// state instead — the access is then sent as part of the single create POST.
// In draft mode the copy says so plainly, because a dialog that looks like it
// saved but didn't is the one failure mode worth extra words.
export default function AccessModal({
  artifactID,
  access,
  write,
  hasOtherVersions,
  canEdit,
  onClose,
  onSaved,
  draft: draftMode = false,
  onCommit,
}: {
  // Required unless draft mode, where nothing is PATCHed.
  artifactID?: string;
  access: string[];
  write?: string[] | null;
  hasOtherVersions: boolean;
  canEdit: boolean;
  onClose: () => void;
  onSaved: () => void;
  // Draft mode: no artifact exists yet; lift edits instead of persisting them.
  draft?: boolean;
  // `write` is null when the draft is still in mirror mode (write follows read),
  // which is NOT the same as an explicit list of every reader — see persist().
  onCommit?: (access: string[], write: string[] | null) => void;
}) {
  // Local, optimistic copy of the principal rows. Seeded from props and
  // re-synced whenever the server props change (after a save + router.refresh).
  // Editing local state — not props directly — means several quick edits made
  // before the refresh round-trips compose on the latest local rows instead of
  // each recomputing from the same stale prop snapshot (which would drop all
  // but the last change).
  const [rows, setRows] = useState<Row[]>(() => levelsFor(access, write));
  // Re-sync to authoritative props when they change (after a save +
  // router.refresh) using React's "adjust state during render" pattern —
  // synchronous, and avoids a set-state-in-effect round-trip.
  const propKey = `${access.join(",")}|${(write ?? []).join(",")}|${write == null ? "mirror" : "explicit"}`;
  const [syncedKey, setSyncedKey] = useState(propKey);
  if (propKey !== syncedKey) {
    setSyncedKey(propKey);
    setRows(levelsFor(access, write));
  }
  const [draft, setDraft] = useState("");
  const [addLevel, setAddLevel] = useState<Level>("read");
  const [focused, setFocused] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const [groups, setGroups] = useState<Group[]>([]);
  const [idpGroups, setIdpGroups] = useState<IdpGroup[]>([]);

  useEffect(() => {
    listGroups().then(setGroups).catch(() => setGroups([]));
    listIdpGroups().then(setIdpGroups).catch(() => setIdpGroups([]));
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [busy, onClose]);

  const groupByToken = useMemo(() => {
    const m = new Map<string, Group>();
    for (const g of groups) m.set(g.token.toLowerCase(), g);
    return m;
  }, [groups]);
  const idpByToken = useMemo(() => {
    const m = new Map<string, IdpGroup>();
    for (const g of idpGroups) m.set(g.token.toLowerCase(), g);
    return m;
  }, [idpGroups]);

  // persist recomputes (access, write) from the given rows and PATCHes both.
  // access = every row's token; write = tokens whose level is write. The
  // server unions write into access (⊆ invariant), so a write-only row still
  // gets read.
  const persist = async (next: Row[]) => {
    setRows(next); // optimistic — later edits compose on this, not stale props
    const nextAccess = next.map((r) => r.token);
    const nextWrite = next.filter((r) => r.level === "write").map((r) => r.token);
    // Draft mode: there is nothing to PATCH. Lift the edit to the caller's
    // draft state and return — no network, so no failure to roll back from.
    if (draftMode) {
      // Report mirror mode as null rather than "every reader, listed". They look
      // equivalent on the version being created, but an EXPLICIT allowed_write is
      // sticky: later versions inherit it, and because the server unions write
      // into read (the ⊆ invariant), a stored write of ['*'] would silently
      // re-widen read access the first time someone narrows it to a domain.
      // Mirror mode has no such tail — it just tracks whatever read becomes.
      const mirror = next.length > 0 && next.every((r) => r.level === "write");
      onCommit?.(nextAccess, mirror ? null : nextWrite);
      return;
    }
    setBusy(true);
    setErr("");
    try {
      await updateArtifactAccess(artifactID!, nextAccess, nextWrite);
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setRows(levelsFor(access, write)); // roll back to the last authoritative state
    } finally {
      setBusy(false);
    }
  };

  const removeRow = (token: string) => persist(rows.filter((r) => r.token !== token));
  const setRowLevel = (token: string, level: Level) =>
    persist(rows.map((r) => (r.token === token ? { ...r, level } : r)));
  const addRow = (explicit?: string) => {
    const token = (explicit ?? draft).trim();
    if (!token || rows.some((r) => r.token === token)) {
      setDraft("");
      return;
    }
    void persist([...rows, { token, level: addLevel }]).then(() => setDraft(""));
  };

  // Typeahead suggestions across manual + idp groups, once the user types.
  const suggestions = useMemo(() => {
    const q = draft.trim().toLowerCase();
    if (!canEdit || q === "") return [] as { token: string; label: string; sub: string; sso: boolean }[];
    const have = new Set(rows.map((r) => r.token.toLowerCase()));
    const out: { token: string; label: string; sub: string; sso: boolean }[] = [];
    for (const g of groups) {
      if (have.has(g.token.toLowerCase())) continue;
      if (g.token.toLowerCase().includes(q) || g.display_name.toLowerCase().includes(q)) {
        out.push({
          token: g.token,
          label: g.display_name || g.name,
          sub: `${g.token} · ${g.member_count} member${g.member_count === 1 ? "" : "s"}`,
          sso: false,
        });
      }
    }
    for (const g of idpGroups) {
      if (have.has(g.token.toLowerCase())) continue;
      if (g.token.toLowerCase().includes(q) || g.name.toLowerCase().includes(q)) {
        out.push({
          token: g.token,
          label: g.name,
          sub: `${g.token} · ${g.member_count} member${g.member_count === 1 ? "" : "s"}`,
          sso: true,
        });
      }
    }
    return out.slice(0, 8);
  }, [draft, canEdit, rows, groups, idpGroups]);

  const body = (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center overflow-y-auto bg-black/40 p-4 backdrop-blur-sm"
      onMouseDown={() => {
        if (!busy) onClose();
      }}
    >
      <div
        role="dialog"
        aria-modal="true"
        aria-label="Edit access"
        className="w-full max-w-md rounded-xl border border-neutral-200 bg-white shadow-2xl"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="flex items-center justify-between border-b border-neutral-100 px-5 py-3">
          <h2 className="text-sm font-semibold text-neutral-800">{canEdit ? "Edit access" : "Access"}</h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="close"
            className="rounded p-1 text-neutral-400 hover:bg-neutral-100 hover:text-neutral-600"
          >
            ✕
          </button>
        </div>

        <div className="px-5 py-3">
          {draftMode ? (
            // Nothing here is written yet. Said plainly, because every other
            // edit surface in arti DOES save on the spot.
            <p className="mb-2 rounded-md bg-blue-50 px-2.5 py-1.5 text-[11px] leading-snug text-blue-800">
              <strong>Not saved yet.</strong> This access applies when you create the artifact.
            </p>
          ) : hasOtherVersions ? (
            <p className="mb-2 rounded-md bg-amber-50 px-2.5 py-1.5 text-[11px] leading-snug text-amber-800">
              Changes apply to <strong>this version only</strong>. Other versions of this slug keep their
              existing access.
            </p>
          ) : null}

          <ul className="max-h-72 divide-y divide-neutral-100 overflow-auto">
            {rows.length === 0 ? (
              <li className="py-3 text-[12px] text-neutral-500">
                <span className="font-medium text-rose-700">Private</span> — only the creator and admins can
                read.
              </li>
            ) : (
              rows.map((r) => (
                <li key={r.token} className="flex items-center justify-between gap-2 py-2 text-[12px]">
                  <PrincipalLabel token={r.token} group={groupByToken.get(r.token.toLowerCase())} idp={idpByToken.get(r.token.toLowerCase())} />
                  <div className="flex shrink-0 items-center gap-1.5">
                    {canEdit ? (
                      <select
                        value={r.level}
                        disabled={busy}
                        onChange={(e) => setRowLevel(r.token, e.target.value as Level)}
                        aria-label={`access level for ${r.token}`}
                        className="rounded-md border border-neutral-200 bg-white px-1.5 py-1 text-[11px] text-neutral-700 disabled:opacity-50"
                      >
                        <option value="read">Read</option>
                        <option value="write">Read &amp; write</option>
                      </select>
                    ) : (
                      <span className="text-[11px] text-neutral-500">{r.level === "write" ? "Read & write" : "Read"}</span>
                    )}
                    {canEdit ? (
                      <button
                        type="button"
                        onClick={() => removeRow(r.token)}
                        disabled={busy}
                        aria-label={`remove ${r.token}`}
                        title="remove"
                        className="rounded p-1 text-neutral-400 hover:bg-rose-50 hover:text-rose-600 disabled:opacity-50"
                      >
                        ×
                      </button>
                    ) : null}
                  </div>
                </li>
              ))
            )}
          </ul>

          {canEdit ? (
            <div className="mt-3 border-t border-neutral-100 pt-3">
              <div className="flex items-center gap-2">
                <input
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  onFocus={() => setFocused(true)}
                  onBlur={() => setTimeout(() => setFocused(false), 120)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault();
                      addRow();
                    }
                  }}
                  placeholder="email, *@domain, or a group"
                  disabled={busy}
                  className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-white px-2 py-1 font-mono text-[12px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
                />
                <select
                  value={addLevel}
                  disabled={busy}
                  onChange={(e) => setAddLevel(e.target.value as Level)}
                  aria-label="level for new entry"
                  className="rounded-md border border-neutral-200 bg-white px-1.5 py-1 text-[11px] text-neutral-700 disabled:opacity-50"
                >
                  <option value="read">Read</option>
                  <option value="write">Read &amp; write</option>
                </select>
                <button
                  type="button"
                  onClick={() => addRow()}
                  disabled={busy || !draft.trim()}
                  className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                >
                  Add
                </button>
              </div>
              {focused && suggestions.length > 0 ? (
                <ul className="mt-2 max-h-40 overflow-auto rounded-md border border-neutral-200 bg-white py-1">
                  {suggestions.map((s) => (
                    <li key={s.token}>
                      <button
                        type="button"
                        onMouseDown={(e) => {
                          e.preventDefault();
                          addRow(s.token);
                        }}
                        className="flex w-full items-center gap-2 px-2.5 py-1 text-left hover:bg-neutral-50"
                      >
                        <span className="min-w-0 flex-1">
                          <span className="flex items-center gap-1.5">
                            <span className="truncate text-[12px] text-neutral-800">{s.label}</span>
                            {s.sso ? <SsoBadge /> : null}
                          </span>
                          <span className="block truncate font-mono text-[10px] text-neutral-400">{s.sub}</span>
                        </span>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : null}
              <p className="mt-1.5 text-[10px] leading-snug text-neutral-500">
                <code className="bg-neutral-100 px-1">*</code> = anyone who can sign in,{" "}
                <code className="bg-neutral-100 px-1">*@example.com</code> = a domain, an email, or start typing
                to find a <span className="font-medium">group</span>. Write access is unioned into read.
              </p>
              {err ? <p className="mt-1 text-[11px] text-rose-600">error: {err}</p> : null}
            </div>
          ) : null}
        </div>

        {/* Draft mode gets an explicit dismissal. It is only a close button —
            the rows above already live in the draft — so it says Done, not
            Save: there is exactly one commit on the page, and it's Create. */}
        {draftMode ? (
          <div className="flex items-center justify-end gap-2 border-t border-neutral-100 px-5 py-3">
            <button
              type="button"
              onClick={onClose}
              className="rounded-md bg-neutral-800 px-3 py-1 text-xs font-medium text-white transition hover:bg-neutral-900"
            >
              Done
            </button>
          </div>
        ) : null}
      </div>
    </div>
  );

  // Guard for non-DOM render contexts (SSR / the node-env test runner), where
  // there's no document to portal into — render the tree inline instead.
  if (typeof document === "undefined") return body;
  return createPortal(body, document.body);
}

// PrincipalLabel resolves one token to display copy: `*` → config-neutral
// phrase, group:/idp: → badge + name, everything else raw.
function PrincipalLabel({ token, group, idp }: { token: string; group?: Group; idp?: IdpGroup }) {
  const kind = principalKind(token);
  if (kind === "everyone") {
    return <span className="truncate text-neutral-800">Anyone who can sign in</span>;
  }
  if (kind === "group") {
    const name = token.slice("group:".length);
    return (
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate text-neutral-800">{group?.display_name || name}</span>
        {group ? (
          <span className="shrink-0 text-[10px] text-neutral-400">
            {group.member_count} member{group.member_count === 1 ? "" : "s"}
          </span>
        ) : (
          <span className="shrink-0 text-[10px] text-amber-600">unknown group</span>
        )}
      </span>
    );
  }
  if (kind === "idp") {
    const name = token.slice("idp:".length);
    return (
      <span className="flex min-w-0 items-center gap-1.5">
        <span className="truncate text-neutral-800">{name}</span>
        <SsoBadge />
        {idp ? (
          <span className="shrink-0 text-[10px] text-neutral-400">
            {idp.member_count} member{idp.member_count === 1 ? "" : "s"}
          </span>
        ) : null}
      </span>
    );
  }
  return <span className="truncate font-mono text-neutral-800">{token}</span>;
}

function SsoBadge() {
  return (
    <span className="shrink-0 rounded bg-indigo-50 px-1 py-0.5 text-[9px] font-medium uppercase tracking-wide text-indigo-600">
      SSO
    </span>
  );
}
