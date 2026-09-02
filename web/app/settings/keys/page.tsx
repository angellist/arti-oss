"use client";

import { useEffect, useRef, useState } from "react";
import { getMe, listApiKeys, createApiKey, revokeApiKey, hasPerm } from "@/lib/arti";
import type { ApiKey, CreatedApiKey, Me } from "@/lib/types";

function keyStatus(k: ApiKey): "Revoked" | "Expired" | "Active" {
  if (k.revoked_at) return "Revoked";
  if (new Date(k.expires_at) < new Date()) return "Expired";
  return "Active";
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

function statusBadge(status: "Revoked" | "Expired" | "Active") {
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
  onRevoke,
}: {
  k: ApiKey;
  showOwner: boolean;
  onRevoke: (id: string) => Promise<void>;
}) {
  const status = keyStatus(k);
  const inactive = status !== "Active";
  const [busy, setBusy] = useState(false);

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

  return (
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
      <td className="py-2 px-3 text-[12px] text-neutral-600">{k.scopes.join(", ")}</td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.created_at} /></td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.expires_at} /></td>
      <td className="py-2 px-3 text-[12px] text-neutral-600"><DateCell s={k.last_used_at} /></td>
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
  );
}

// CreatedKeyDialog shows the one-time plaintext key with a copy button.
function CreatedKeyDialog({ created, onClose }: { created: CreatedApiKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(created.key);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // fallback: select the text
    }
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4">
      <div className="w-full max-w-lg rounded-lg border border-neutral-200 bg-white p-6 shadow-xl">
        <h3 className="text-[14px] font-semibold text-neutral-900">API key created — copy it now</h3>
        <p className="mt-1 text-[12px] text-neutral-500">
          This key will not be shown again. Store it somewhere safe, such as your CI secret store.
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
          <button
            type="button"
            onClick={onClose}
            className="rounded px-3 py-1.5 text-[12px] font-medium text-neutral-700 hover:bg-neutral-100"
          >
            Done
          </button>
        </div>
      </div>
    </div>
  );
}

export default function ApiKeysPage() {
  const [me, setMe] = useState<Me | null>(null);
  const [keys, setKeys] = useState<ApiKey[]>([]);
  const [allKeys, setAllKeys] = useState<ApiKey[]>([]);
  const [showAll, setShowAll] = useState(false);
  const [loading, setLoading] = useState(true);
  const [err, setErr] = useState("");
  const [revokeErr, setRevokeErr] = useState("");
  const [allErr, setAllErr] = useState("");

  // Create form state
  const [name, setName] = useState("");
  const [ttlDays, setTtlDays] = useState(90);
  const [creating, setCreating] = useState(false);
  const [createErr, setCreateErr] = useState("");
  const [justCreated, setJustCreated] = useState<CreatedApiKey | null>(null);

  // Guard against a stale load()/loadAll() overwriting state from a newer one.
  const loadSeq = useRef(0);
  const loadAllSeq = useRef(0);

  const load = async () => {
    // Independent settling: getMe is optional (drives perm-gated UI), so a
    // keys-load failure must NOT discard a successful getMe. Seq-guarded so a
    // slow older load() can't clobber a newer one's result (e.g. null out `me`).
    const seq = ++loadSeq.current;
    const [meRes, keysRes] = await Promise.allSettled([getMe(), listApiKeys()]);
    if (seq !== loadSeq.current) return; // superseded by a newer load()
    setMe(meRes.status === "fulfilled" ? meRes.value : null);
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
      const all = await listApiKeys(true);
      if (seq !== loadAllSeq.current) return; // a newer loadAll won — ignore stale success
      setAllKeys(all);
    } catch {
      if (seq !== loadAllSeq.current) return; // superseded — don't surface a stale error
      // Scope to the admin panel — must NOT set the shared `err`, which the
      // own-keys section treats as fatal and would hide the (already-loaded) list.
      setAllErr("Failed to load all keys.");
    }
  };

  const handleShowAll = async (on: boolean) => {
    setShowAll(on);
    if (on) await loadAll();
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setCreating(true);
    setCreateErr("");
    try {
      const created = await createApiKey(name.trim(), ["upload"], ttlDays);
      setJustCreated(created);
      setName("");
      setTtlDays(90);
      await load(); // refresh the list
      if (showAll) await loadAll(); // keep the admin all-keys panel in sync
    } catch (ex) {
      setCreateErr(ex instanceof Error ? ex.message : String(ex));
    } finally {
      setCreating(false);
    }
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

  const tableHeaders = (showOwner: boolean) => (
    <thead>
      <tr className="text-left text-[11px] uppercase tracking-wide text-neutral-400">
        <th className="py-2 pl-6 pr-3 font-medium">Name</th>
        {showOwner && <th className="py-2 px-3 font-medium">Owner</th>}
        <th className="py-2 px-3 font-medium">Key</th>
        <th className="py-2 px-3 font-medium">Scope</th>
        <th className="py-2 px-3 font-medium">Created</th>
        <th className="py-2 px-3 font-medium">Expires</th>
        <th className="py-2 px-3 font-medium">Last used</th>
        <th className="py-2 px-3 font-medium">Status</th>
        <th className="py-2 pl-3 pr-6 font-medium" />
      </tr>
    </thead>
  );

  return (
    <div>
      {justCreated && (
        <CreatedKeyDialog
          created={justCreated}
          onClose={() => setJustCreated(null)}
        />
      )}

      <div className="px-6 pt-4">
        <h2 className="text-[14px] font-semibold text-neutral-900">API Keys</h2>
        <p className="mt-0.5 text-[12px] text-neutral-500">
          API keys let a tool or agent call arti non-interactively{" "}
          <strong>as you</strong>. Today you can only create keys for{" "}
          <strong>uploading</strong> (create / append / read, ≤ 25 MiB). Keys are shown
          once — treat them like a password.
        </p>
      </div>

      {/* Create form */}
      <div className="px-6 py-4">
        <h3 className="mb-2 text-[12px] font-semibold text-neutral-700">Create a new key</h3>
        <form onSubmit={handleCreate} className="flex flex-wrap items-end gap-3">
          <div className="flex flex-col gap-1">
            <label htmlFor="key-name" className="text-[11px] text-neutral-500">
              Name
            </label>
            <input
              id="key-name"
              type="text"
              required
              placeholder="e.g. ci-upload"
              value={name}
              onChange={(e) => setName(e.target.value)}
              className="w-64 rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
            />
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="key-scope" className="text-[11px] text-neutral-500">
              Scope
            </label>
            <select
              id="key-scope"
              className="rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
              defaultValue="upload"
              disabled
            >
              <option value="upload">Upload</option>
            </select>
          </div>
          <div className="flex flex-col gap-1">
            <label htmlFor="key-ttl" className="text-[11px] text-neutral-500">
              Expires after
            </label>
            <select
              id="key-ttl"
              value={ttlDays}
              onChange={(e) => setTtlDays(Number(e.target.value))}
              className="rounded border border-neutral-300 px-2 py-1 text-[12px] focus:border-blue-400 focus:outline-none"
            >
              <option value={30}>30 days</option>
              <option value={90}>90 days</option>
              <option value={180}>180 days</option>
              <option value={365}>365 days</option>
            </select>
          </div>
          <button
            type="submit"
            disabled={creating || !name.trim()}
            className="rounded bg-neutral-900 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-neutral-700 disabled:opacity-40"
          >
            {creating ? "Creating…" : "Create key"}
          </button>
        </form>
        {createErr && <p className="mt-2 text-[12px] text-red-600">{createErr}</p>}
      </div>

      {revokeErr && (
        <p className="px-6 pb-2 text-[12px] text-red-600">Revoke failed: {revokeErr}</p>
      )}

      {/* Own keys table */}
      <div className="px-0">
        {loading ? (
          <p className="px-6 py-4 text-[12px] text-neutral-400">Loading…</p>
        ) : err ? (
          <p className="px-6 py-4 text-[12px] text-red-600">{err}</p>
        ) : keys.length === 0 ? (
          <p className="px-6 py-2 text-[12px] text-neutral-400">No keys yet.</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full min-w-[700px] border-t border-neutral-100">
              {tableHeaders(false)}
              <tbody>
                {keys.map((k) => (
                  <KeyRow key={k.id} k={k} showOwner={false} onRevoke={handleRevoke} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Admin section: all keys */}
      {isAdmin && (
        <div className="mt-8 border-t border-neutral-200 px-6 pt-4">
          <div className="flex items-center gap-3">
            <h3 className="text-[12px] font-semibold text-neutral-700">All keys (admin)</h3>
            <button
              type="button"
              onClick={() => handleShowAll(!showAll)}
              className="rounded border border-neutral-200 px-2 py-0.5 text-[11px] text-neutral-600 hover:bg-neutral-50"
            >
              {showAll ? "Hide" : "Show all keys"}
            </button>
          </div>
          {allErr && <p className="mt-2 text-[12px] text-red-600">{allErr}</p>}
          {showAll && !allErr && (
            allKeys.length === 0 ? (
              <p className="mt-2 text-[12px] text-neutral-400">No keys found.</p>
            ) : (
              <div className="mt-2 overflow-x-auto">
                <table className="w-full min-w-[800px] border-t border-neutral-100">
                  {tableHeaders(true)}
                  <tbody>
                    {allKeys.map((k) => (
                      <KeyRow key={k.id} k={k} showOwner={true} onRevoke={handleRevoke} />
                    ))}
                  </tbody>
                </table>
              </div>
            )
          )}
        </div>
      )}
    </div>
  );
}
