"use client";

import { useState } from "react";
import type { McpServer } from "@/lib/types";
import {
  createMcpServer,
  deleteMcpServer,
  listMcpServerApps,
  listMcpServers,
  updateMcpServer,
  type McpServerInput,
} from "@/lib/arti";
import { relativeTime } from "@/lib/time";

const inputCls =
  "min-w-0 rounded-md border border-neutral-200 px-2.5 py-1.5 text-[13px] text-neutral-900 placeholder:text-neutral-400";
const buttonCls =
  "shrink-0 rounded-md border border-neutral-200 bg-white px-2 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50";

type Draft = { name: string; resource_url: string; auth: "none" | "oauth"; scope: string; tools: string; notes: string };

const emptyDraft: Draft = { name: "", resource_url: "", auth: "oauth", scope: "", tools: "", notes: "" };

function toInput(d: Draft): McpServerInput {
  return {
    resource_url: d.resource_url,
    auth: d.auth,
    scope: d.scope,
    notes: d.notes,
    tool_allowlist: d.tools.split(/[\s,]+/).filter(Boolean),
  };
}

function toDraft(s: McpServer): Draft {
  return { name: s.name, resource_url: s.resource_url, auth: s.auth, scope: s.scope, tools: s.tool_allowlist.join(", "), notes: s.notes };
}

// ConnectorsManager is the admin surface for the APP MCP connector list.
// Disabling and deleting both ask first and name the apps that declare the
// connector, because either one breaks those apps for every viewer.
export default function ConnectorsManager({ initialServers }: { initialServers: McpServer[] }) {
  const [servers, setServers] = useState<McpServer[]>(initialServers);
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [editing, setEditing] = useState<Draft | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const run = async (fn: () => Promise<void>) => {
    setBusy(true);
    setErr("");
    try {
      await fn();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const refresh = async () => setServers(await listMcpServers());

  // confirmBreakage names the apps that declare this connector and asks.
  const confirmBreakage = async (name: string, verb: string, extra: string) => {
    const { apps, scanned, unreadable } = await listMcpServerApps(name);
    const list = apps.length
      ? apps.map((a) => `  • ${a.title || a.named_slug || a.artifact_id} (${a.named_slug ?? a.artifact_id})`).join("\n")
      : "  none of them";
    const skipped = unreadable ? `\n(${unreadable} apps could not be read and are not checked.)` : "";
    return window.confirm(
      `${verb} "${name}"? ${extra}\n\nOf ${scanned} live apps, these declare it and will stop working:\n${list}${skipped}`,
    );
  };

  const create = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      await createMcpServer({ name: draft.name.trim(), ...toInput(draft) });
      setDraft(emptyDraft);
      await refresh();
    });
  };

  const toggle = (s: McpServer) =>
    run(async () => {
      if (s.enabled && !(await confirmBreakage(s.name, "Disable", "Viewers keep their sign-ins."))) return;
      await updateMcpServer(s.name, { enabled: !s.enabled });
      await refresh();
    });

  const remove = (s: McpServer) =>
    run(async () => {
      if (!(await confirmBreakage(s.name, "Delete", "Every viewer is signed out of it."))) return;
      await deleteMcpServer(s.name);
      await refresh();
    });

  const save = () =>
    run(async () => {
      if (!editing) return;
      await updateMcpServer(editing.name, toInput(editing));
      setEditing(null);
      await refresh();
    });

  return (
    <div className="px-6 py-4">
      {err ? (
        <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">{err}</div>
      ) : null}

      <form onSubmit={create} className="mb-6 rounded-lg border border-neutral-200 bg-neutral-50 p-3">
        <h3 className="mb-2 text-[12px] font-semibold text-neutral-700">Add connector</h3>
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-6">
        <input
          value={draft.name}
          onChange={(e) => setDraft({ ...draft, name: e.target.value })}
          placeholder="name (notion)"
          aria-label="connector name"
          className={inputCls}
        />
        <input
          value={draft.resource_url}
          onChange={(e) => setDraft({ ...draft, resource_url: e.target.value })}
          placeholder="https://… MCP resource URL"
          aria-label="resource URL"
          className={inputCls + " sm:col-span-3"}
        />
        <select
          value={draft.auth}
          onChange={(e) => setDraft({ ...draft, auth: e.target.value as Draft["auth"] })}
          aria-label="auth"
          className={inputCls}
        >
          <option value="oauth">oauth</option>
          <option value="none">none</option>
        </select>
        <input
          value={draft.scope}
          onChange={(e) => setDraft({ ...draft, scope: e.target.value })}
          placeholder="scope (oauth)"
          aria-label="scope"
          className={inputCls}
        />
        <input
          value={draft.tools}
          onChange={(e) => setDraft({ ...draft, tools: e.target.value })}
          placeholder="tool allowlist, comma separated (empty = all)"
          aria-label="tool allowlist"
          className={inputCls + " sm:col-span-3"}
        />
        <input
          value={draft.notes}
          onChange={(e) => setDraft({ ...draft, notes: e.target.value })}
          placeholder="notes (optional)"
          aria-label="notes"
          className={inputCls + " sm:col-span-2"}
        />
        <button
          type="submit"
          disabled={busy || !draft.name.trim() || !draft.resource_url.trim()}
          className="rounded-md border border-neutral-200 bg-white px-3 py-1.5 text-[13px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
        >
          Add
        </button>
        </div>
      </form>

      {servers.length === 0 ? (
        <p className="text-[12px] text-neutral-400">No connectors. Apps can reach only arti and the built-in llm.</p>
      ) : (
        <table className="w-full table-fixed">
          <thead>
            <tr className="border-b border-neutral-200 text-left text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
              <th className="w-[15%] py-1.5">Name</th>
              <th className="w-[33%] py-1.5">Resource URL</th>
              <th className="w-[7%] py-1.5">Auth</th>
              <th className="w-[16%] py-1.5">Tool allowlist</th>
              <th className="w-[10%] py-1.5">Last edited</th>
              <th className="w-[19%] py-1.5" />
            </tr>
          </thead>
          <tbody>
            {servers.map((s) =>
              editing?.name === s.name ? (
                <tr key={s.name} className="border-b border-neutral-100 align-top">
                  <td className="break-words py-2 pr-2 text-[13px] font-semibold text-neutral-900">{s.name}</td>
                  <td className="space-y-1 py-2 pr-2">
                    <input
                      value={editing.resource_url}
                      onChange={(e) => setEditing({ ...editing, resource_url: e.target.value })}
                      aria-label="resource URL"
                      className={inputCls + " w-full"}
                    />
                    <input
                      value={editing.notes}
                      onChange={(e) => setEditing({ ...editing, notes: e.target.value })}
                      placeholder="notes"
                      aria-label="notes"
                      className={inputCls + " w-full"}
                    />
                  </td>
                  <td className="space-y-1 py-2 pr-2">
                    <select
                      value={editing.auth}
                      onChange={(e) => setEditing({ ...editing, auth: e.target.value as Draft["auth"] })}
                      aria-label="auth"
                      className={inputCls + " w-full"}
                    >
                      <option value="oauth">oauth</option>
                      <option value="none">none</option>
                    </select>
                    <input
                      value={editing.scope}
                      onChange={(e) => setEditing({ ...editing, scope: e.target.value })}
                      placeholder="scope"
                      aria-label="scope"
                      className={inputCls + " w-full"}
                    />
                  </td>
                  <td className="py-2 pr-2">
                    <textarea
                      value={editing.tools}
                      onChange={(e) => setEditing({ ...editing, tools: e.target.value })}
                      placeholder="empty = every tool"
                      aria-label="tool allowlist"
                      rows={3}
                      className={inputCls + " w-full text-[12px]"}
                    />
                  </td>
                  <td />
                  <td className="space-x-1 whitespace-nowrap py-2 text-right">
                    <button type="button" disabled={busy} onClick={() => void save()} className={buttonCls}>
                      Save
                    </button>
                    <button type="button" disabled={busy} onClick={() => setEditing(null)} className={buttonCls}>
                      Cancel
                    </button>
                  </td>
                </tr>
              ) : (
                <tr key={s.name} className="border-b border-neutral-100 align-top">
                  <td className="py-2 pr-2">
                    <div className="break-words text-[13px] font-semibold text-neutral-900">{s.name}</div>
                    {s.enabled ? null : <div className="text-[11px] font-medium text-amber-700">disabled</div>}
                    {s.notes ? <div className="text-[11px] text-neutral-500">{s.notes}</div> : null}
                  </td>
                  <td className="break-all py-2 pr-2 text-[11px] leading-snug text-neutral-500">
                    {s.resource_url}
                  </td>
                  <td className="py-2 pr-2 text-[12px] text-neutral-600">{s.auth}</td>
                  <td className="break-words py-2 pr-2 text-[12px] text-neutral-600">
                    {s.tool_allowlist.length ? s.tool_allowlist.join(", ") : <span className="text-neutral-400">all tools</span>}
                  </td>
                  <td className="py-2 pr-2 text-[12px] text-neutral-500" title={`${s.updated_by} · ${s.updated_at}`}>
                    <div className="truncate text-[11px]">{s.updated_by}</div>
                    <div>{relativeTime(s.updated_at)}</div>
                  </td>
                  <td className="space-x-1 whitespace-nowrap py-2 text-right">
                    <button type="button" disabled={busy} onClick={() => void toggle(s)} className={buttonCls}>
                      {s.enabled ? "Disable" : "Enable"}
                    </button>
                    <button type="button" disabled={busy} onClick={() => setEditing(toDraft(s))} className={buttonCls}>
                      Edit
                    </button>
                    <button type="button" disabled={busy} onClick={() => void remove(s)} className={buttonCls}>
                      Delete
                    </button>
                  </td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      )}
    </div>
  );
}
