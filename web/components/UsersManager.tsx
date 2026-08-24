"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { RosterRole, RosterUser } from "@/lib/types";
import { addUser, listUsers } from "@/lib/arti";

// UsersManager renders the Users roster and the one write it supports.
//
// Most of a row is derived from activity, so the page is mostly a read. The
// single write — recording a principal — exists because nothing else can express
// it: assigning a role can't, since the baseline USER role is the one role the
// store refuses to assign (everyone holds it implicitly), and in a deployment
// whose only other role is ADMIN, "add a user" through role assignment would
// mean "make them an administrator".

// relative renders a coarse age. Exact timestamps are in the title attribute —
// a roster is scanned, and "3 days ago" is what the scan is looking for.
function relative(iso: string | null): { label: string; title: string } {
  if (!iso) {
    // Not "never signed in": user_idp_groups only starts at migration 0020, so
    // an older login left no row and we genuinely do not know.
    return { label: "no sign-in recorded", title: "arti has no login on record for this principal" };
  }
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return { label: iso, title: iso };
  const mins = Math.max(0, Math.round((Date.now() - then) / 60000));
  const label =
    mins < 2 ? "just now"
    : mins < 60 ? `${mins} minutes ago`
    : mins < 120 ? "1 hour ago"
    : mins < 48 * 60 ? `${Math.round(mins / 60)} hours ago`
    : mins < 60 * 24 * 60 ? `${Math.round(mins / (60 * 24))} days ago`
    : `${Math.round(mins / (60 * 24 * 30))} months ago`;
  return { label, title: new Date(iso).toLocaleString() };
}

function RoleChips({ roles }: { roles: RosterRole[] }) {
  if (roles.length === 0) return <span className="text-neutral-300">—</span>;
  return (
    <div className="flex flex-col items-start gap-[3px]">
      {roles.map((r) => (
        <div key={`${r.name}/${r.source}`} className="flex items-baseline gap-1.5">
          <span
            className={
              "rounded border px-1.5 text-[11px] leading-[1.45] " +
              (r.name === "ADMIN"
                ? "border-amber-200 bg-amber-50 text-amber-700"
                : "border-neutral-200 bg-neutral-100 text-neutral-700")
            }
          >
            {r.name}
          </span>
          <span className="whitespace-nowrap text-[10px] text-neutral-400">
            {r.source === "baseline" || r.source === "direct" ? r.source : `via ${r.source}`}
          </span>
        </div>
      ))}
    </div>
  );
}

// GroupChips keeps the two namespaces visually distinct, because they are
// distinct: `group:` is an arti group somebody here manages, `idp:` is an SSO
// group captured at login. A stale idp chip is struck through — past
// ARTI_IDP_GROUPS_MAX_AGE it grants nothing, and drawing it like a live grant
// would overstate the person's access.
function GroupChips({ user }: { user: RosterUser }) {
  const none = user.groups.length === 0 && user.idp_groups.length === 0;
  if (none) return <span className="text-neutral-300">—</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {user.groups.map((g) => (
        <span
          key={`g:${g}`}
          className="rounded border border-sky-200 bg-sky-50 px-1.5 text-[11px] leading-[1.45] text-sky-700"
        >
          group:{g}
        </span>
      ))}
      {user.idp_groups.map((g) => (
        <span
          key={`i:${g}`}
          title={
            user.idp_stale
              ? "this SSO snapshot has aged out — it currently grants nothing; a fresh sign-in restores it"
              : "SSO group captured at sign-in"
          }
          className={
            "rounded border border-dashed px-1.5 text-[11px] leading-[1.45] " +
            (user.idp_stale
              ? "border-neutral-300 text-neutral-400 line-through"
              : "border-neutral-300 text-neutral-600")
          }
        >
          idp:{g}
        </span>
      ))}
    </div>
  );
}

export default function UsersManager({
  initialUsers,
  sources,
}: {
  initialUsers: RosterUser[];
  sources: string[];
}) {
  const [users, setUsers] = useState<RosterUser[]>(initialUsers);
  const [q, setQ] = useState("");
  const [adding, setAdding] = useState(false);
  const [err, setErr] = useState("");
  const [flash, setFlash] = useState("");

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return users;
    return users.filter((u) =>
      [u.email, u.kind, u.note, ...u.roles.map((r) => `${r.name} ${r.source}`), ...u.groups.map((g) => `group:${g}`), ...u.idp_groups.map((g) => `idp:${g}`)]
        .join(" ")
        .toLowerCase()
        .includes(needle),
    );
  }, [users, q]);

  const refresh = async () => {
    try {
      setUsers((await listUsers()).users);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="px-6 py-4">
      {err ? (
        <div className="mb-3 max-w-3xl rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
          {err}
        </div>
      ) : null}
      {flash ? (
        <div className="mb-3 max-w-3xl rounded-md border border-emerald-200 bg-emerald-50 px-3 py-2 text-[12px] text-emerald-700">
          {flash}
        </div>
      ) : null}

      <div className="mb-2.5 flex items-center gap-2">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="filter by email, role, or group…"
          aria-label="filter users"
          className="w-full max-w-[320px] rounded-md border border-neutral-200 px-2.5 py-1.5 text-[12px] text-neutral-800 placeholder:text-neutral-400"
        />
        <span className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          {shown.length === users.length
            ? `${users.length} user${users.length === 1 ? "" : "s"}`
            : `${shown.length} of ${users.length}`}
        </span>
        <span className="flex-1" />
        <button
          type="button"
          onClick={() => {
            setErr("");
            setFlash("");
            setAdding(true);
          }}
          className="rounded-md border border-neutral-900 bg-neutral-900 px-2.5 py-1.5 text-[12px] text-white hover:bg-neutral-800"
        >
          + Add user
        </button>
      </div>

      <div className="overflow-x-auto rounded-md border border-neutral-200">
        <table className="w-full table-fixed border-collapse">
          <colgroup>
            <col className="w-[30%]" />
            <col className="w-[22%]" />
            <col className="w-[31%]" />
            <col className="w-[17%]" />
          </colgroup>
          <thead>
            <tr className="bg-neutral-50">
              {["Email", "Roles", "Groups", "Last sign-in"].map((h) => (
                <th
                  key={h}
                  className="border-b border-neutral-200 px-3 py-1.5 text-left text-[10px] font-semibold uppercase tracking-wider text-neutral-500"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.map((u) => {
              const seen = relative(u.last_seen_at);
              return (
                <tr key={u.email} className="border-b border-neutral-200 last:border-b-0 hover:bg-neutral-50">
                  <td className="px-3 py-2 align-top text-[13px] text-neutral-800">
                    <span className="block truncate" title={u.email}>
                      {u.email}
                    </span>
                    <span className="mt-0.5 flex flex-wrap items-center gap-1">
                      {u.kind === "service" ? (
                        <span className="rounded border border-neutral-200 bg-neutral-100 px-1 text-[9px] uppercase tracking-wide text-neutral-500">
                          service
                        </span>
                      ) : null}
                      {u.registered && u.added_by ? (
                        <span
                          className="text-[11px] text-neutral-400"
                          title={`recorded by ${u.added_by}`}
                        >
                          added by {u.added_by}
                        </span>
                      ) : null}
                      {u.note ? <span className="text-[11px] text-neutral-400">· {u.note}</span> : null}
                    </span>
                  </td>
                  <td className="px-3 py-2 align-top">
                    <RoleChips roles={u.roles} />
                  </td>
                  <td className="px-3 py-2 align-top">
                    <GroupChips user={u} />
                  </td>
                  <td className="px-3 py-2 align-top text-[13px] text-neutral-800" title={seen.title}>
                    {u.last_seen_at ? seen.label : <span className="italic text-neutral-400">{seen.label}</span>}
                  </td>
                </tr>
              );
            })}
            {shown.length === 0 ? (
              <tr>
                <td colSpan={4} className="px-3 py-8 text-center text-[12px] text-neutral-400">
                  {users.length === 0 ? "no users yet" : `nothing matches “${q}”`}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      {sources.length > 0 ? (
        <p className="mt-2.5 text-[11px] leading-relaxed text-neutral-400">
          assembled from {sources.join(", ")}. a principal that appears in none of these is not
          listed.
        </p>
      ) : null}

      {adding ? (
        <AddUserModal
          onCancel={() => setAdding(false)}
          onAdded={async (email) => {
            setAdding(false);
            setFlash(`${email} recorded. it holds the baseline USER role only — assign anything more from Roles and Permissions.`);
            await refresh();
          }}
        />
      ) : null}
    </div>
  );
}

function AddUserModal({
  onCancel,
  onAdded,
}: {
  onCancel: () => void;
  onAdded: (email: string) => void | Promise<void>;
}) {
  const [email, setEmail] = useState("");
  const [kind, setKind] = useState<"human" | "service">("service");
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCancel();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);

  const submit = async () => {
    const addr = email.trim();
    if (!addr || busy) return;
    setBusy(true);
    setErr("");
    try {
      const u = await addUser({ email: addr, kind, note: note.trim() });
      await onAdded(u.email);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setBusy(false);
    }
  };

  const body = (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-neutral-900/35 p-4">
      {/* role/aria-modal are load-bearing, not decoration: the global keyboard
          shortcuts (CatalogTable, DiagramEditor) bail on
          `[aria-modal="true"]`, so without it typing here — with focus on the
          Kind select rather than a text input — still triggers them. */}
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="add-user-title"
        className="w-full max-w-[420px] overflow-hidden rounded-lg border border-neutral-200 bg-white shadow-xl"
      >
        <div className="px-4 pt-3.5">
          <h3 id="add-user-title" className="text-[14px] font-semibold text-neutral-900">
            Add user
          </h3>
          <p className="mt-0.5 text-[12px] leading-relaxed text-neutral-500">
            records an email so it appears here before it has signed in or created anything.
            use it for service accounts.
          </p>
        </div>
        <div className="px-4 py-3.5">
          {err ? (
            <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-2.5 py-1.5 text-[11px] text-rose-700">
              {err}
            </div>
          ) : null}
          <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
            Email
          </label>
          <input
            ref={inputRef}
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") submit();
            }}
            placeholder="service-account@example.com"
            className="w-full rounded-md border border-neutral-300 px-2.5 py-1.5 text-[13px] text-neutral-900 placeholder:text-neutral-400"
          />
          <p className="mb-3.5 mt-1 text-[11px] leading-relaxed text-neutral-400">
            lowercased on save. one address — a glob like <code>*@example.com</code> is a
            pattern, not a person, and is refused.
          </p>

          <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
            Kind
          </label>
          <select
            value={kind}
            onChange={(e) => setKind(e.target.value as "human" | "service")}
            className="w-full rounded-md border border-neutral-300 bg-white px-2.5 py-1.5 text-[13px] text-neutral-900"
          >
            <option value="service">service — an agent, bot, or automation</option>
            <option value="human">human — a person</option>
          </select>

          <label className="mb-1 mt-3.5 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
            Note <span className="font-normal normal-case tracking-normal text-neutral-400">(optional)</span>
          </label>
          <input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="owning team, or why this exists"
            className="w-full rounded-md border border-neutral-300 px-2.5 py-1.5 text-[13px] text-neutral-900 placeholder:text-neutral-400"
          />

          <div className="mt-3.5 rounded-md border border-sky-200 bg-sky-50 px-2.5 py-2 text-[11px] leading-relaxed text-sky-800">
            This grants nothing. The principal holds the baseline USER role, like every
            authenticated caller; assign more from Roles and Permissions. It also does not
            allow sign-in — that stays gated by the domain allowlist and the required SSO
            groups.
          </div>
        </div>
        <div className="flex justify-end gap-2 border-t border-neutral-200 bg-neutral-50 px-4 py-2.5">
          <button
            type="button"
            onClick={onCancel}
            className="rounded-md border border-neutral-200 bg-white px-2.5 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={busy || email.trim() === ""}
            className="rounded-md border border-neutral-900 bg-neutral-900 px-2.5 py-1.5 text-[12px] text-white hover:bg-neutral-800 disabled:opacity-40"
          >
            {busy ? "Adding…" : "Add user"}
          </button>
        </div>
      </div>
    </div>
  );

  return typeof document === "undefined" ? body : createPortal(body, document.body);
}
