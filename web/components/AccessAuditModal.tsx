"use client";

import { useCallback, useEffect, useRef, useState } from "react";

import type { AuditCredentialKind, CredentialAudit, UserAccess } from "@/lib/types";
import { getCredentialAudit, lookupUserAccess, revokeUserCredential } from "@/lib/arti";
import { AccessMatrix } from "./PermissionMatrix";
import { relativeTime } from "@/lib/time";

type Row = {
  kind: AuditCredentialKind;
  id: string;
  kindLabel: string;
  name: string;
  detail: string;
  created: string;
  expires: string;
  lastUsed: string | null;
  status: "Active" | "Expired" | "Revoked";
  revokeLabel: string;
};

function status(revoked: boolean, expiresAt: string): Row["status"] {
  if (revoked) return "Revoked";
  return Date.parse(expiresAt) < Date.now() ? "Expired" : "Active";
}

function latest(...isos: (string | null | undefined)[]): string | null {
  const xs = isos.filter((x): x is string => !!x).sort();
  return xs.length ? xs[xs.length - 1] : null;
}

function hostOf(u: string): string {
  try {
    return new URL(u).host;
  } catch {
    return u;
  }
}

export function auditRows(a: CredentialAudit): Row[] {
  const lastSeen = (cred: string) =>
    latest(...a.usage.filter((u) => u.cred === cred).map((u) => u.last_seen));
  return [
    ...a.api_keys.map((k) => ({
      kind: "apikey" as const,
      id: k.id,
      kindLabel: "API key",
      name: k.name,
      detail: `${k.key_prefix}… · ${k.scopes.join(", ")}`,
      created: k.created_at,
      expires: k.expires_at,
      lastUsed: latest(k.last_used_at, lastSeen(`apikey:${k.id}`)),
      status: status(k.revoked_at !== null, k.expires_at),
      revokeLabel: "Revoke",
    })),
    ...a.device_tokens.map((d) => ({
      kind: "device" as const,
      id: d.family_id,
      kindLabel: "Upload token",
      name: `device login ${d.family_id.slice(0, 8)}`,
      detail: `scope ${d.scope} · refreshed ${relativeTime(d.rotated_at)}`,
      created: d.created_at,
      expires: d.expires_at,
      lastUsed: lastSeen(`device:${d.family_id}`),
      status: status(d.revoked, d.expires_at),
      revokeLabel: "Revoke",
    })),
    // The stored access token expires and refreshes on its own, so a
    // connection stays Active until it is disconnected.
    ...a.connections.map((c) => ({
      kind: "connection" as const,
      id: c.resource,
      kindLabel: "App connection",
      name: hostOf(c.resource),
      detail: c.resource,
      created: c.updated_at,
      expires: "",
      lastUsed: null,
      status: "Active" as const,
      revokeLabel: "Disconnect",
    })),
    ...a.share_links.map((l) => ({
      kind: "share" as const,
      id: l.id,
      kindLabel: "Share link",
      name: l.target,
      detail: `${l.token_prefix}… · opened ${l.open_count}×${l.note ? ` · ${l.note}` : ""}`,
      created: l.created_at,
      expires: l.expires_at,
      lastUsed: l.last_opened_at,
      status: status(l.revoked_at !== null, l.expires_at),
      revokeLabel: "Revoke",
    })),
  ];
}

export function usageCredLabel(cred: string, a: CredentialAudit): string {
  const i = cred.indexOf(":");
  const kind = i < 0 ? cred : cred.slice(0, i);
  const id = i < 0 ? "" : cred.slice(i + 1);
  switch (kind) {
    case "session":
      return "browser session";
    case "token":
      return "CLI / MCP token";
    case "service":
      return "service credential";
    case "apikey":
      return `API key ${a.api_keys.find((k) => k.id === id)?.name ?? id.slice(0, 8)}`;
    case "device":
      return `upload token ${id.slice(0, 8)}`;
    default:
      return cred;
  }
}

// relativeTime only reads backwards, so a future instant (an expiry) is a date.
function When({
  iso,
  empty = "—",
  future = false,
}: {
  iso: string | null;
  empty?: string;
  future?: boolean;
}) {
  if (!iso) return <span className="text-neutral-400">{empty}</span>;
  const d = new Date(iso);
  return (
    <span title={d.toLocaleString()}>
      {future ? d.toLocaleDateString() : relativeTime(iso)}
    </span>
  );
}

const th =
  "border-b border-neutral-200 px-2.5 py-1.5 text-left text-[10px] font-semibold uppercase tracking-wider text-neutral-500";
const td = "px-2.5 py-1.5 align-top";

export default function AccessAuditModal({
  email,
  subtitle = "",
  permissions = [],
  onClose,
}: {
  email: string;
  subtitle?: string;
  permissions?: string[];
  onClose: () => void;
}) {
  const [access, setAccess] = useState<UserAccess | null>(null);
  const [accessErr, setAccessErr] = useState("");
  const [audit, setAudit] = useState<CredentialAudit | null>(null);
  const [err, setErr] = useState("");
  const [pending, setPending] = useState("");

  const load = useCallback(
    () =>
      getCredentialAudit(email).then(setAudit, (e) =>
        setErr(e instanceof Error ? e.message : String(e)),
      ),
    [email],
  );

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    lookupUserAccess(email).then(setAccess, (e) =>
      setAccessErr(e instanceof Error ? e.message : String(e)),
    );
  }, [email]);

  // showModal moves focus in and makes the page inert, so Tab stays inside.
  // The browser's own focus restore never runs: React detaches the element first.
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    const d = ref.current;
    d?.showModal();
    return () => {
      d?.close();
      opener?.focus();
    };
  }, []);

  const revoke = async (r: Row) => {
    if (
      !window.confirm(
        `${r.revokeLabel} ${r.kindLabel.toLowerCase()} “${r.name}” for ${email}? This cannot be undone.`,
      )
    ) {
      return;
    }
    setErr("");
    setPending(`${r.kind}:${r.id}`);
    try {
      await revokeUserCredential(email, r.kind, r.id);
      await load();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setPending("");
    }
  };

  const rows = audit ? auditRows(audit) : [];

  return (
    // aria-modal is load-bearing: global keyboard shortcuts bail on it.
    <dialog
      ref={ref}
      aria-modal="true"
      aria-labelledby="access-audit-title"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      className="m-auto max-h-[90vh] w-[calc(100%-2rem)] max-w-[980px] flex-col overflow-hidden rounded-lg border border-neutral-200 bg-white p-0 shadow-xl backdrop:bg-neutral-900/35 open:flex"
    >
      <div className="flex items-start gap-3 border-b border-neutral-200 px-4 py-3">
        <div className="min-w-0 flex-1">
          <h3
            id="access-audit-title"
            className="text-[14px] font-semibold text-neutral-900"
          >
            {email}
          </h3>
          {subtitle ? <p className="truncate text-[12px] text-neutral-500">{subtitle}</p> : null}
        </div>
        <button
          type="button"
          aria-label="close"
          onClick={onClose}
          className="rounded-md px-2 text-[18px] leading-none text-neutral-400 hover:text-neutral-700"
        >
          ×
        </button>
      </div>

      <div className="overflow-y-auto px-4 py-3">
        {err ? (
          <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
            {err}
          </div>
        ) : null}
        <h4 className="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          Permissions
        </h4>
        {access ? (
          <AccessMatrix access={access} permissions={permissions} />
        ) : accessErr ? (
          <div className="rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
            {accessErr}
          </div>
        ) : (
          <div className="py-3 text-[12px] text-neutral-400">loading…</div>
        )}
        {!audit && !err ? (
          <div className="py-8 text-center text-[12px] text-neutral-400">
            loading…
          </div>
        ) : null}

        {audit ? (
          <>
            <h4 className="mb-1.5 mt-4 text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
              Stored credentials
            </h4>
            <div className="overflow-x-auto rounded-md border border-neutral-200">
              <table className="w-full border-collapse text-[12px] text-neutral-800">
                <thead className="bg-neutral-50">
                  <tr>
                    {[
                      "Kind",
                      "Credential",
                      "Created",
                      "Expires",
                      "Last used",
                      "Status",
                      "",
                    ].map((h, i) => (
                      <th key={h || i} className={th}>
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((r) => (
                    <tr
                      key={`${r.kind}:${r.id}`}
                      className="border-b border-neutral-100 last:border-b-0"
                    >
                      <td
                        className={`${td} whitespace-nowrap text-neutral-500`}
                      >
                        {r.kindLabel}
                      </td>
                      <td className={`${td} max-w-[320px]`}>
                        <div className="truncate" title={r.name}>
                          {r.name}
                        </div>
                        <div
                          className="truncate font-mono text-[10px] text-neutral-400"
                          title={r.detail}
                        >
                          {r.detail}
                        </div>
                      </td>
                      <td className={`${td} whitespace-nowrap`}>
                        <When iso={r.created} />
                      </td>
                      <td className={`${td} whitespace-nowrap`}>
                        <When iso={r.expires || null} future />
                      </td>
                      <td className={`${td} whitespace-nowrap`}>
                        <When iso={r.lastUsed} empty="never seen" />
                      </td>
                      <td className={`${td} whitespace-nowrap`}>
                        <span
                          className={
                            "rounded border px-1.5 text-[11px] " +
                            (r.status === "Active"
                              ? "border-emerald-200 bg-emerald-50 text-emerald-700"
                              : "border-neutral-200 bg-neutral-100 text-neutral-500")
                          }
                        >
                          {r.status}
                        </span>
                      </td>
                      <td className={`${td} text-right`}>
                        {r.status === "Active" ? (
                          <button
                            type="button"
                            onClick={() => revoke(r)}
                            disabled={pending !== ""}
                            className="rounded-md border border-rose-200 bg-white px-2 py-0.5 text-[11px] text-rose-700 hover:bg-rose-50 disabled:opacity-40"
                          >
                            {pending === `${r.kind}:${r.id}`
                              ? "…"
                              : r.revokeLabel}
                          </button>
                        ) : null}
                      </td>
                    </tr>
                  ))}
                  {rows.length === 0 ? (
                    <tr>
                      <td
                        colSpan={7}
                        className="px-3 py-5 text-center text-[12px] text-neutral-400"
                      >
                        no stored credentials
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
            <p className="mt-1.5 text-[11px] leading-relaxed text-neutral-400">
              Browser sessions and CLI / MCP tokens are signed and never stored,
              so they cannot be listed or revoked one by one — they show up only
              in the usage below. Deactivate refuses all of them at once.
            </p>

            <h4 className="mb-1.5 mt-4 text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
              Where credentials were used · last {audit.usage_days} days
            </h4>
            <div className="overflow-x-auto rounded-md border border-neutral-200">
              <table className="w-full border-collapse text-[12px] text-neutral-800">
                <thead className="bg-neutral-50">
                  <tr>
                    {[
                      "Credential",
                      "Client",
                      "IP",
                      "Reads",
                      "Writes",
                      "First seen",
                      "Last seen",
                    ].map((h) => (
                      <th key={h} className={th}>
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {audit.usage.map((u) => (
                    <tr
                      key={`${u.cred}|${u.ip}|${u.user_agent}`}
                      className="border-b border-neutral-100 last:border-b-0"
                    >
                      <td className={`${td} whitespace-nowrap`}>
                        {usageCredLabel(u.cred, audit)}
                      </td>
                      <td
                        className={`${td} max-w-[260px] truncate font-mono text-[11px]`}
                        title={u.user_agent}
                      >
                        {u.user_agent || "—"}
                      </td>
                      <td className={`${td} font-mono text-[11px]`}>{u.ip}</td>
                      <td className={`${td} tabular-nums`}>{u.reads}</td>
                      <td className={`${td} tabular-nums`}>{u.writes}</td>
                      <td className={`${td} whitespace-nowrap`}>
                        <When iso={u.first_seen} />
                      </td>
                      <td className={`${td} whitespace-nowrap`}>
                        <When iso={u.last_seen} />
                      </td>
                    </tr>
                  ))}
                  {audit.usage.length === 0 ? (
                    <tr>
                      <td
                        colSpan={7}
                        className="px-3 py-5 text-center text-[12px] text-neutral-400"
                      >
                        no recorded use in this window
                      </td>
                    </tr>
                  ) : null}
                </tbody>
              </table>
            </div>
          </>
        ) : null}
      </div>
    </dialog>
  );
}
