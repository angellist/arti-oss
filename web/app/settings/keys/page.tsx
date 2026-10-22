"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { getMe, listApiKeys, createApiKey, revokeApiKey, getCredentialUsage, hasPerm } from "@/lib/arti";
import {
  filterKeys,
  keyStatus,
  ownersOf,
  scopeLabel,
  sortKeys,
  type KeyStatus,
  type SortDir,
  type SortKey,
  type StatusFilter,
} from "@/lib/apiKeyTable";
import type { ApiKey, CreatedApiKey, CredentialSource, CredentialUsage, Me } from "@/lib/types";

// credRef is the reference a key's writes are stamped with
// (artifacts.written_via), and the key under which its usage is recorded.
function credRef(k: ApiKey): string {
  return `apikey:${k.id}`;
}

// newSourceDays is how long a source counts as new in the table. Slack only
// carries a key answering from a new network now, so this list is where the
// rest of the story lives and recent arrivals have to be visible at a glance.
const newSourceDays = 7;

function isNewSource(src: CredentialSource): boolean {
  if (!src.first_seen) return false;
  const age = Date.now() - new Date(src.first_seen).getTime();
  return age < newSourceDays * 24 * 60 * 60 * 1000;
}

// SourcesCell shows how many places a key has been used from. More than one is
// the signal worth reading: a key issued for one pipeline that answers from
// several addresses has been copied somewhere.
function SourcesCell({
  sources,
  expanded,
  onToggle,
}: {
  sources: CredentialSource[];
  expanded: boolean;
  onToggle: () => void;
}) {
  if (sources.length === 0) {
    return <span className="text-neutral-400">—</span>;
  }
  const fresh = sources.filter(isNewSource).length;
  return (
    <button
      type="button"
      onClick={onToggle}
      className="rounded px-1 text-[12px] text-blue-600 hover:bg-blue-50"
      aria-expanded={expanded}
    >
      {sources.length} {sources.length === 1 ? "source" : "sources"}
      {fresh > 0 && (
        <span className="ml-1 rounded bg-amber-100 px-1 py-0.5 text-[10px] font-medium text-amber-800">
          {fresh} new
        </span>
      )}
      <span className="ml-1 text-[10px] text-neutral-400">{expanded ? "▾" : "▸"}</span>
    </button>
  );
}

// SourceList is the expanded detail: every address and client a key has
// answered from in the window, oldest-first-seen shown so a newcomer stands
// out against the source it was issued for.
function SourceList({ sources, colSpan }: { sources: CredentialSource[]; colSpan: number }) {
  return (
    <tr className="border-t border-neutral-100 bg-neutral-50/60">
      <td colSpan={colSpan} className="py-2 pl-6 pr-6">
        <table className="w-full">
          <thead>
            <tr className="text-left text-[10px] uppercase tracking-wide text-neutral-400">
              <th className="pb-1 pr-3 font-medium">Address</th>
              <th className="pb-1 pr-3 font-medium">Client</th>
              <th className="pb-1 pr-3 font-medium">Reads</th>
              <th className="pb-1 pr-3 font-medium">Writes</th>
              <th className="pb-1 pr-3 font-medium">First seen</th>
              <th className="pb-1 font-medium">Last seen</th>
            </tr>
          </thead>
          <tbody>
            {sources.map((src) => (
              <tr key={`${src.ip}|${src.user_agent}`} className="text-[11px] text-neutral-600">
                <td className="py-0.5 pr-3 font-mono">
                  {src.ip || "—"}
                  {isNewSource(src) && (
                    <span className="ml-1.5 rounded bg-amber-100 px-1 py-0.5 text-[9px] font-medium text-amber-800">
                      new
                    </span>
                  )}
                </td>
                <td className="py-0.5 pr-3 font-mono" title={src.user_agent}>
                  {src.user_agent ? src.user_agent.slice(0, 40) : "—"}
                </td>
                <td className="py-0.5 pr-3">{src.reads}</td>
                <td className="py-0.5 pr-3">{src.writes}</td>
                <td className="py-0.5 pr-3"><DateCell s={src.first_seen} /></td>
                <td className="py-0.5"><DateCell s={src.last_seen} /></td>
              </tr>
            ))}
          </tbody>
        </table>
      </td>
    </tr>
  );
}

// sourcesFor picks the usage rows belonging to one key, newest activity first.
function sourcesFor(usage: CredentialUsage | null, k: ApiKey): CredentialSource[] {
  if (!usage) return [];
  return usage.sources.filter((s) => s.cred === credRef(k));
}

// DateCell renders a timestamp as the date with the hh:mm on a second line.
function DateCell({ s }: { s: string | null }) {
  if (!s) return <span className="text-neutral-400">—</span>;
  const d = new Date(s);
  return (
    <div className="leading-tight">
      <div>{d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })}</div>
      <div className="text-[10px] text-neutral-400">
        {d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}
      </div>
    </div>
  );
}

function statusBadge(status: KeyStatus) {
  if (status === "Active")
    return <span className="rounded bg-green-50 px-1.5 py-0.5 text-[11px] font-medium text-green-700">Active</span>;
  if (status === "Expired")
    return <span className="rounded bg-yellow-50 px-1.5 py-0.5 text-[11px] font-medium text-yellow-700">Expired</span>;
  return <span className="rounded bg-neutral-100 px-1.5 py-0.5 text-[11px] font-medium text-neutral-500">Revoked</span>;
}

// KeyRow renders one row in the keys table.
function KeyRow({
  k,
  showOwner,
  sources,
  docs,
  onRevoke,
}: {
  k: ApiKey;
  showOwner: boolean;
  sources: CredentialSource[];
  docs: number;
  onRevoke: (id: string) => Promise<void>;
}) {
  const status = keyStatus(k);
  const inactive = status !== "Active";
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState(false);

  const revoke = async () => {
    if (!window.confirm(`Revoke key "${k.name}"? This cannot be undone.`)) return;
    setBusy(true);
    try {
      await onRevoke(k.id);
    } finally {
      setBusy(false);
    }
  };

  const rowClass = "border-t border-neutral-100 " + (inactive ? "opacity-50" : "");
  const columns = showOwner ? 11 : 10;

  return (
    <>
    <tr className={rowClass}>
      <td className={"py-2 pl-6 pr-3 text-[12px] " + (inactive ? "text-neutral-400" : "text-neutral-900")}>
        {k.name}
      </td>
      {showOwner && (
        <td className="py-2 px-3 text-[12px] text-neutral-600" title={k.owner_email}>
          {k.owner_email.split("@")[0]}
        </td>
      )}
      <td className="py-2 px-3 font-mono text-[11px] text-neutral-500">{k.key_prefix}…</td>
      <td className="whitespace-nowrap py-2 px-3 text-[12px] text-neutral-600" title={k.scopes.join(", ")}>
        {k.scopes.map(scopeLabel).join(", ")}
      </td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.created_at} /></td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.expires_at} /></td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.last_used_at} /></td>
      <td className="py-2 px-3 text-[12px]">
        <SourcesCell sources={sources} expanded={expanded} onToggle={() => setExpanded(!expanded)} />
      </td>
      <td className="py-2 px-3 text-[12px] text-neutral-600">
        {docs > 0 ? (
          // Every document this key wrote, which is the question asked once a
          // key turns out to have been shared.
          <a
            className="text-blue-600 hover:underline"
            href={`/?q=${encodeURIComponent(`via:${credRef(k)}`)}`}
          >
            {docs}
          </a>
        ) : (
          <span className="text-neutral-400">0</span>
        )}
      </td>
      <td className="py-2 px-3">{statusBadge(status)}</td>
      <td className="py-2 pl-3 pr-6">
        {status === "Active" && (
          <button
            type="button"
            disabled={busy}
            onClick={revoke}
            className="rounded px-2 py-0.5 text-[11px] text-red-600 hover:bg-red-50 disabled:opacity-40"
          >
            {busy ? "…" : "Revoke"}
          </button>
        )}
      </td>
    </tr>
    {expanded && <SourceList sources={sources} colSpan={columns} />}
    </>
  );
}

const keyColumns: { key: SortKey; label: string; ownerOnly?: boolean }[] = [
  { key: "name", label: "Note" },
  { key: "owner", label: "Owner", ownerOnly: true },
  { key: "key", label: "Key" },
  { key: "scope", label: "Access" },
  { key: "created", label: "Created" },
  { key: "expires", label: "Expires" },
  { key: "last_used", label: "Last used" },
  { key: "sources", label: "Used from" },
  { key: "docs", label: "Docs" },
  { key: "status", label: "Status" },
];

// KeysTable renders keys with every column header sortable.
function KeysTable({
  keys,
  showOwner,
  usage,
  onRevoke,
}: {
  keys: ApiKey[];
  showOwner: boolean;
  usage: CredentialUsage | null;
  onRevoke: (id: string) => Promise<void>;
}) {
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: "created", dir: "desc" });
  const sorted = useMemo(
    () =>
      sortKeys(keys, sort.key, sort.dir, (k) => ({
        sources: sourcesFor(usage, k).length,
        docs: usage?.docs[credRef(k)] ?? 0,
      })),
    [keys, sort, usage],
  );
  const toggle = (key: SortKey) =>
    setSort((s) => (s.key === key ? { key, dir: s.dir === "asc" ? "desc" : "asc" } : { key, dir: "asc" }));
  const visible = keyColumns.filter((c) => showOwner || !c.ownerOnly);

  return (
    <div className="overflow-x-auto">
      <table className={"w-full border-t border-neutral-100 " + (showOwner ? "min-w-[800px]" : "min-w-[700px]")}>
        <thead>
          <tr className="text-left text-[11px] uppercase tracking-wide text-neutral-400">
            {visible.map((c, i) => {
              const active = sort.key === c.key;
              return (
                <th
                  key={c.key}
                  className={"py-2 font-medium " + (i === 0 ? "pl-6 pr-3" : "px-3")}
                  aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}
                >
                  <button
                    type="button"
                    onClick={() => toggle(c.key)}
                    className={"uppercase tracking-wide hover:text-neutral-700 " + (active ? "text-neutral-700" : "")}
                  >
                    {c.label}
                    <span className="ml-0.5 text-[9px]">{active ? (sort.dir === "asc" ? "▲" : "▼") : ""}</span>
                  </button>
                </th>
              );
            })}
            <th className="py-2 pl-3 pr-6 font-medium" />
          </tr>
        </thead>
        <tbody>
          {sorted.map((k) => (
            <KeyRow
              key={k.id}
              k={k}
              showOwner={showOwner}
              sources={sourcesFor(usage, k)}
              docs={usage?.docs[credRef(k)] ?? 0}
              onRevoke={onRevoke}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

const statusFilters: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "expired", label: "Expired" },
  { value: "revoked", label: "Revoked" },
];

// KeyFilters is the search, creator and status bar above a keys table. The
// creator picker only renders when owners are passed.
function KeyFilters({
  query,
  onQuery,
  placeholder,
  status,
  onStatus,
  count,
  owners,
  owner,
  onOwner,
}: {
  query: string;
  onQuery: (q: string) => void;
  placeholder: string;
  status: StatusFilter;
  onStatus: (f: StatusFilter) => void;
  count?: (f: StatusFilter) => number;
  owners?: string[];
  owner?: string;
  onOwner?: (o: string) => void;
}) {
  return (
    <div className="flex flex-wrap items-end gap-4 px-6 pb-3">
      <div className="flex flex-col gap-1">
        <label htmlFor="key-search" className="text-[11px] text-neutral-500">
          Search
        </label>
        <input
          id="key-search"
          type="search"
          placeholder={placeholder}
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          className="w-56 rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
        />
      </div>
      {owners && onOwner && (
        <div className="flex flex-col gap-1">
          <label htmlFor="owner-filter" className="text-[11px] text-neutral-500">
            Creator
          </label>
          <select
            id="owner-filter"
            value={owner ?? ""}
            onChange={(e) => onOwner(e.target.value)}
            className="rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
          >
            <option value="">All creators</option>
            {owners.map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
        </div>
      )}
      <div className="flex flex-col gap-1">
        <span className="text-[11px] text-neutral-500">Status</span>
        <div className="flex overflow-hidden rounded border border-neutral-300">
          {statusFilters.map((f) => (
            <button
              key={f.value}
              type="button"
              aria-pressed={status === f.value}
              onClick={() => onStatus(f.value)}
              className={
                "border-l border-neutral-300 px-2.5 py-1 text-[12px] first:border-l-0 " +
                (status === f.value ? "bg-neutral-900 text-white" : "text-neutral-600 hover:bg-neutral-50")
              }
            >
              {f.label} <span className="opacity-60">{count ? count(f.value) : ""}</span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}

const confirmDelayMs = 600;

// NewKeyDialog collects the key settings, generates the key, then shows the
// one-time plaintext. Closing after generation needs a second click to confirm.
function NewKeyDialog({
  onGenerate,
  onClose,
}: {
  onGenerate: (name: string, ttlDays: number) => Promise<CreatedApiKey>;
  onClose: () => void;
}) {
  const [name, setName] = useState("");
  const [ttlDays, setTtlDays] = useState(90);
  const [generating, setGenerating] = useState(false);
  const [err, setErr] = useState("");
  const [created, setCreated] = useState<CreatedApiKey | null>(null);
  const [copied, setCopied] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const panel = useRef<HTMLDivElement>(null);
  const armedAt = useRef(0);

  // Focus moves into the dialog on open and again when the secret appears.
  useEffect(() => {
    panel.current?.querySelector<HTMLElement>("input:not(:disabled), button:not(:disabled)")?.focus();
  }, [created]);

  // Tab and Shift+Tab cycle inside the dialog, never onto the page behind it.
  const trapTab = (e: React.KeyboardEvent) => {
    if (e.key !== "Tab" || !panel.current) return;
    const items = Array.from(
      panel.current.querySelectorAll<HTMLElement>("input:not(:disabled), select:not(:disabled), button:not(:disabled)"),
    );
    if (items.length === 0) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  const generate = async () => {
    setGenerating(true);
    setErr("");
    try {
      setCreated(await onGenerate(name.trim(), ttlDays));
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setGenerating(false);
    }
  };

  const copy = async () => {
    if (!created) return;
    try {
      await navigator.clipboard.writeText(created.key);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // fallback: select the text
    }
  };

  const secondaryButton =
    "rounded px-3 py-1.5 text-[12px] font-medium text-neutral-700 hover:bg-neutral-100 disabled:opacity-40";
  const primaryButton =
    "rounded bg-neutral-900 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-neutral-700 disabled:opacity-40";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div
        ref={panel}
        role="dialog"
        aria-modal="true"
        aria-labelledby="new-key-title"
        onKeyDown={trapTab}
        className="w-full max-w-lg rounded-lg border border-neutral-200 bg-white p-6 shadow-xl"
      >
        {!created ? (
          <>
            <h3 id="new-key-title" className="text-[14px] font-semibold text-neutral-900">
              Create a new key
            </h3>
            <div className="mt-3 flex flex-col gap-1">
              <label htmlFor="key-name" className="text-[11px] text-neutral-500">
                Note
              </label>
              <input
                id="key-name"
                type="text"
                placeholder="e.g. devin-helios-tracking"
                value={name}
                onChange={(e) => setName(e.target.value)}
                disabled={generating}
                aria-describedby="key-name-help"
                className="rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
              />
              <p id="key-name-help" className="text-[11px] text-neutral-500">
                Only for you, to remember where this key is used, so be specific (e.g.{" "}
                <code className="whitespace-nowrap rounded bg-neutral-100 px-1">devin-helios-tracking</code>). This is
                not the environment variable name: where you use the key, such as Runlayer or Devin,
                name that variable{" "}
                <code className="whitespace-nowrap rounded bg-neutral-100 px-1">ARTI_API_KEY</code>.
              </p>
            </div>
            <div className="mt-3 flex flex-col gap-1">
              <label htmlFor="key-scope" className="text-[11px] text-neutral-500">
                Access
              </label>
              <select
                id="key-scope"
                className="w-40 rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
                defaultValue="upload"
                disabled
              >
                <option value="upload">Read + Upload</option>
              </select>
            </div>
            <div className="mt-3 flex flex-col gap-1">
              <label htmlFor="key-ttl" className="text-[11px] text-neutral-500">
                Expires after
              </label>
              <select
                id="key-ttl"
                value={ttlDays}
                onChange={(e) => setTtlDays(Number(e.target.value))}
                disabled={generating}
                className="w-40 rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
              >
                <option value={30}>30 days</option>
                <option value={90}>90 days</option>
                <option value={180}>180 days</option>
                <option value={365}>365 days</option>
              </select>
            </div>
            {err && <p className="mt-2 text-[12px] text-red-600">{err}</p>}
            <div className="mt-4 flex justify-end gap-2">
              <button type="button" onClick={onClose} disabled={generating} className={secondaryButton}>
                Cancel
              </button>
              <button
                type="button"
                onClick={generate}
                disabled={generating || !name.trim()}
                className={primaryButton}
              >
                {generating ? "Generating…" : "Generate"}
              </button>
            </div>
          </>
        ) : (
          <>
            <h3 id="new-key-title" className="text-[14px] font-semibold text-neutral-900">
              API key created — copy it now
            </h3>
            <p className="mt-1 text-[12px] font-semibold text-neutral-900">
              <span aria-hidden="true">⚠️</span> This key will not be shown again. Store it somewhere
              safe, such as your CI secret store.
            </p>
            <p className="mt-2 text-[12px] text-neutral-500">
              To set it as an environment variable, use{" "}
              <code className="rounded bg-neutral-100 px-1 text-[11px]">ARTI_API_KEY</code> as the
              variable <em>name</em>, and the string below as the variable <em>value</em>.
            </p>
            <div className="mt-3 flex items-center gap-2 rounded border border-neutral-200 bg-neutral-50 px-3 py-2">
              <code className="flex-1 break-all text-[11px] text-neutral-800">{created.key}</code>
              <button
                type="button"
                onClick={copy}
                className="shrink-0 rounded px-2 py-1 text-[11px] font-medium text-blue-600 hover:bg-blue-50"
              >
                {copied ? "Copied!" : "Copy"}
              </button>
            </div>
            <div className="mt-4 flex justify-end">
              {/* One element for both steps keeps focus. The confirm ignores clicks and
                  Enters that land within confirmDelayMs of Done (double-click, key-repeat). */}
              <button
                type="button"
                onClick={() => {
                  if (!confirming) {
                    armedAt.current = Date.now();
                    setConfirming(true);
                  } else if (Date.now() - armedAt.current >= confirmDelayMs) {
                    onClose();
                  }
                }}
                className={primaryButton}
              >
                {confirming ? "Yes, I have copied this key and know I won\u2019t see it again" : "Done"}
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}

export default function ApiKeysPage() {
  const [me, setMe] = useState<Me | null>(null);
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [allKeys, setAllKeys] = useState<ApiKey[] | null>(null);
  const [tab, setTab] = useState<"mine" | "all">("mine");
  const [ownerFilter, setOwnerFilter] = useState("");
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("active");
  const [myQuery, setMyQuery] = useState("");
  const [myStatus, setMyStatus] = useState<StatusFilter>("active");
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [revokeErr, setRevokeErr] = useState("");
  const [allErr, setAllErr] = useState("");
  const [usage, setUsage] = useState<CredentialUsage | null>(null);
  const [allUsage, setAllUsage] = useState<CredentialUsage | null>(null);

  const [creating, setCreating] = useState(false);
  const createButton = useRef<HTMLButtonElement>(null);

  // Guard against a stale load()/loadAll() overwriting state from a newer one.
  const loadSeq = useRef(0);
  const loadAllSeq = useRef(0);

  const load = async () => {
    // Independent settling: getMe is optional (drives perm-gated UI), so a
    // keys-load failure must NOT discard a successful getMe. Seq-guarded so a
    // slow older load() can't clobber a newer one's result (e.g. null out `me`).
    const seq = ++loadSeq.current;
    const [meRes, keysRes, usageRes] = await Promise.allSettled([
      getMe(),
      listApiKeys(),
      // Usage is additive detail: a failure here must leave the key list
      // intact, so it settles independently and simply shows no sources.
      getCredentialUsage(),
    ]);
    if (seq !== loadSeq.current) return; // superseded by a newer load()
    setMe(meRes.status === "fulfilled" ? meRes.value : null);
    setUsage(usageRes.status === "fulfilled" ? usageRes.value : null);
    if (keysRes.status === "fulfilled") {
      setKeys(keysRes.value);
      setErr("");
    } else {
      const ex = keysRes.reason;
      setErr(ex instanceof Error ? ex.message : "Failed to load keys.");
    }
    setLoading(false);
  };

  // eslint-disable-next-line react-hooks/set-state-in-effect
  useEffect(() => { void load(); }, []);

  const loadAll = async () => {
    const seq = ++loadAllSeq.current;
    setAllErr(""); // clear at start so an in-flight retry doesn't keep the table hidden
    try {
      const [all, allUse] = await Promise.all([
        listApiKeys(true),
        getCredentialUsage(true).catch(() => null),
      ]);
      if (seq !== loadAllSeq.current) return; // a newer loadAll won — ignore stale success
      setAllKeys(all);
      setAllUsage(allUse);
    } catch {
      if (seq !== loadAllSeq.current) return; // superseded — don't surface a stale error
      // Scope to the admin panel — must NOT set the shared `err`, which the
      // own-keys section treats as fatal and would hide the (already-loaded) list.
      setAllErr("Failed to load all keys.");
    }
  };

  const handleTab = async (t: "mine" | "all") => {
    setTab(t);
    if (t === "all") await loadAll();
  };

  const generateKey = async (name: string, ttlDays: number) => {
    const created = await createApiKey(name, ["upload"], ttlDays);
    void load(); // refresh the list
    if (showAll) void loadAll(); // keep the admin all-keys panel in sync
    return created;
  };

  const handleRevoke = async (id: string) => {
    setRevokeErr("");
    try {
      await revokeApiKey(id);
    } catch (ex) {
      // Surface the failure instead of swallowing it — the key is still active.
      setRevokeErr(ex instanceof Error ? ex.message : String(ex));
      return;
    }
    await load();
    if (showAll) await loadAll();
  };

  const isAdmin = hasPerm(me, "MANAGE_API_KEYS");
  const showAll = isAdmin && tab === "all";

  const owners = useMemo(() => ownersOf(allKeys ?? []), [allKeys]);
  const ownerKeys = useMemo(
    () => filterKeys(allKeys ?? [], { owner: ownerFilter, status: "all", query }),
    [allKeys, ownerFilter, query],
  );
  const shownAllKeys = useMemo(
    () => filterKeys(ownerKeys, { owner: "", status: statusFilter }),
    [ownerKeys, statusFilter],
  );
  const statusCount = (f: StatusFilter) => filterKeys(ownerKeys, { owner: "", status: f }).length;

  const myQueried = useMemo(
    () => filterKeys(keys, { owner: "", status: "all", query: myQuery }),
    [keys, myQuery],
  );
  const shownMyKeys = useMemo(
    () => filterKeys(myQueried, { owner: "", status: myStatus }),
    [myQueried, myStatus],
  );
  const myStatusCount = (f: StatusFilter) => filterKeys(myQueried, { owner: "", status: f }).length;

  return (
    <div>
      {creating && (
        <NewKeyDialog
          onGenerate={generateKey}
          onClose={() => {
            setCreating(false);
            requestAnimationFrame(() => createButton.current?.focus());
          }}
        />
      )}

      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">API Keys</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          API keys let a tool or agent call arti non-interactively{" "}
          <strong>as you</strong>. Every key has <strong>Read + Upload</strong> access: it can
          read any document you can read, create documents, and append new versions (≤ 25 MiB).
          It cannot edit metadata, archive, or change access. Keys are shown once — treat them
          like a password.
        </p>
      </div>

      {isAdmin && (
        <div role="tablist" className="mt-3 flex gap-4 border-b border-neutral-200 px-6">
          {([["mine", "My keys"], ["all", "All keys"]] as const).map(([t, label]) => (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={tab === t}
              onClick={() => void handleTab(t)}
              className={
                "-mb-px border-b-2 pb-2 text-[12px] font-medium " +
                (tab === t
                  ? "border-neutral-900 text-neutral-900"
                  : "border-transparent text-neutral-500 hover:text-neutral-800")
              }
            >
              {label}
            </button>
          ))}
        </div>
      )}

      {revokeErr && (
        <p className="px-6 pt-3 text-[12px] text-red-600">Revoke failed: {revokeErr}</p>
      )}

      {!showAll && (
      <>
      <div className="px-6 py-4">
        <button
          ref={createButton}
          type="button"
          onClick={() => setCreating(true)}
          className="rounded bg-neutral-900 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-neutral-700"
        >
          Create key
        </button>
      </div>

      {/* Own keys table */}
      <div className="px-0">
        {loading ? (
          <p className="px-6 py-4 text-[12px] text-neutral-400">Loading…</p>
        ) : err ? (
          <p className="px-6 py-4 text-[12px] text-red-600">{err}</p>
        ) : keys.length === 0 ? (
          <p className="px-6 py-2 text-[12px] text-neutral-400">No keys yet.</p>
        ) : (
          <>
            <KeyFilters
              query={myQuery}
              onQuery={setMyQuery}
              placeholder="Key note"
              status={myStatus}
              onStatus={setMyStatus}
              count={myStatusCount}
            />
            {shownMyKeys.length === 0 ? (
              <p className="px-6 text-[12px] text-neutral-400">No keys match these filters.</p>
            ) : (
              <KeysTable keys={shownMyKeys} showOwner={false} usage={usage} onRevoke={handleRevoke} />
            )}
          </>
        )}
      </div>

      </>
      )}

      {showAll && (
        <div className="pt-4">
          <KeyFilters
            query={query}
            onQuery={setQuery}
            placeholder="Key note or creator"
            status={statusFilter}
            onStatus={setStatusFilter}
            count={allKeys ? statusCount : undefined}
            owners={owners}
            owner={ownerFilter}
            onOwner={setOwnerFilter}
          />
          {allErr ? (
            <p className="px-6 text-[12px] text-red-600">{allErr}</p>
          ) : allKeys === null ? (
            <p className="px-6 text-[12px] text-neutral-400">Loading…</p>
          ) : shownAllKeys.length === 0 ? (
            <p className="px-6 text-[12px] text-neutral-400">No keys match these filters.</p>
          ) : (
            <KeysTable keys={shownAllKeys} showOwner usage={allUsage} onRevoke={handleRevoke} />
          )}
        </div>
      )}
    </div>
  );
}
