"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";

import type { RosterRole, RosterUser } from "@/lib/types";
import { addUser, listUsers, setGroupMember, setUserActive } from "@/lib/arti";
import AccessAuditModal from "./AccessAuditModal";

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
function GroupChips({
  user,
  manageable,
  busy,
  onChange,
}: {
  user: RosterUser;
  manageable: Record<string, string[]>;
  busy: boolean;
  onChange: (group: string, member: boolean) => void;
}) {
  const membersOf = (g: string) =>
    Object.prototype.hasOwnProperty.call(manageable, g) ? manageable[g] : undefined;
  const addable = Object.keys(manageable)
    .filter((g) => !user.groups.includes(g))
    .sort();
  const none = user.groups.length === 0 && user.idp_groups.length === 0;
  return (
    <div className="flex flex-wrap items-center gap-1">
      {none && addable.length === 0 ? <span className="text-neutral-300">—</span> : null}
      {user.groups.map((g) => (
        <span
          key={`g:${g}`}
          title={
            membersOf(g) && !membersOf(g)?.includes(user.email)
              ? "member through a pattern such as *@domain; edit it on the Groups page"
              : undefined
          }
          className="inline-flex items-center gap-0.5 rounded border border-sky-200 bg-sky-50 px-1.5 text-[11px] leading-[1.45] text-sky-700"
        >
          group:{g}
          {membersOf(g)?.includes(user.email) ? (
            <button
              type="button"
              aria-label={`remove ${user.email} from ${g}`}
              title={`remove from ${g}`}
              disabled={busy}
              onClick={() => onChange(g, false)}
              className="ml-0.5 text-sky-500 hover:text-rose-600 disabled:opacity-40"
            >
              ×
            </button>
          ) : null}
        </span>
      ))}
      {user.idp_groups.map((g) => (
        <span
          key={`i:${g}`}
          title={
            user.idp_stale
              ? "this SSO snapshot has aged out — it currently grants nothing; a fresh sign-in restores it"
              : "SSO group captured at sign-in; managed in the identity provider"
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
      {addable.length > 0 ? (
        <select
          aria-label={`add ${user.email} to a group`}
          value=""
          disabled={busy}
          onChange={(e) => {
            if (e.target.value) onChange(e.target.value, true);
          }}
          className="rounded border border-dashed border-neutral-300 bg-white px-1 text-[11px] leading-[1.45] text-neutral-500 disabled:opacity-40"
        >
          <option value="">+ group</option>
          {addable.map((g) => (
            <option key={g} value={g}>
              {g}
            </option>
          ))}
        </select>
      ) : null}
    </div>
  );
}

const PAGE_SIZE = 100;

type SortKey = "email" | "roles" | "groups" | "last_seen" | "status";

// Each column's first click sorts in the direction an admin scans it for:
// newest sign-in, most roles or groups, deactivated rows first.
const FIRST_DIR: Record<SortKey, 1 | -1> = { email: 1, roles: -1, groups: -1, last_seen: -1, status: 1 };

const sortValue: Record<SortKey, (u: RosterUser) => string | number> = {
  email: (u) => u.email,
  roles: (u) => u.roles.length,
  groups: (u) => u.groups.length + u.idp_groups.length,
  last_seen: (u) => (u.last_seen_at ? Date.parse(u.last_seen_at) : -Infinity),
  status: (u) => (u.deactivated_at === null ? 1 : 0),
};

export default function UsersManager({
  initialUsers,
  sources,
  currentEmail = "",
  manageableGroups = [],
  permissions = [],
}: {
  initialUsers: RosterUser[];
  sources: string[];
  currentEmail?: string;
  manageableGroups?: { name: string; members: string[] }[];
  permissions?: string[];
}) {
  const [users, setUsers] = useState<RosterUser[]>(initialUsers);
  const [q, setQ] = useState("");
  const [kindF, setKindF] = useState("");
  const [roleF, setRoleF] = useState("");
  const [groupF, setGroupF] = useState("");
  const [statusF, setStatusF] = useState<"" | "active" | "deactivated">("");
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>({ key: "email", dir: 1 });
  const [pageAt, setPageAt] = useState({ view: "", page: 0 });
  const [adding, setAdding] = useState(false);
  const [err, setErr] = useState("");
  const [flash, setFlash] = useState("");
  const [pending, setPending] = useState("");
  const [auditing, setAuditing] = useState("");
  const [groupMembers, setGroupMembers] = useState<Record<string, string[]>>(() =>
    Object.fromEntries(manageableGroups.map((g) => [g.name, g.members])),
  );

  const roleOptions = useMemo(
    () => [...new Set([...users.flatMap((u) => u.roles.map((r) => r.name)), ...(roleF ? [roleF] : [])])].sort(),
    [users, roleF],
  );
  const groupOptions = useMemo(
    () =>
      [
        ...new Set([
          ...users.flatMap((u) => [...u.groups.map((g) => `group:${g}`), ...u.idp_groups.map((g) => `idp:${g}`)]),
          ...(groupF ? [groupF] : []),
        ]),
      ].sort(),
    [users, groupF],
  );

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return users.filter((u) =>
      (!kindF || u.kind === kindF) &&
      (!roleF || u.roles.some((r) => r.name === roleF)) &&
      (!groupF || u.groups.some((g) => `group:${g}` === groupF) || u.idp_groups.some((g) => `idp:${g}` === groupF)) &&
      (!statusF || (statusF === "deactivated") === (u.deactivated_at !== null)) &&
      [u.email, u.kind, u.note, ...u.roles.map((r) => `${r.name} ${r.source}`), ...u.groups.map((g) => `group:${g}`), ...u.idp_groups.map((g) => `idp:${g}`)]
        .join(" ")
        .toLowerCase()
        .includes(needle),
    );
  }, [users, q, kindF, roleF, groupF, statusF]);

  const sorted = useMemo(() => {
    const v = sortValue[sort.key];
    return [...shown].sort((a, b) => {
      const x = v(a);
      const y = v(b);
      const c = x < y ? -1 : x > y ? 1 : 0;
      return c * sort.dir || a.email.localeCompare(b.email);
    });
  }, [shown, sort]);

  // A filter or sort change returns to page 1: the page is kept only while the
  // view it was chosen on is still the one shown.
  const view = JSON.stringify([q, kindF, roleF, groupF, statusF, sort]);
  const pages = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const page = Math.min(pageAt.view === view ? pageAt.page : 0, pages - 1);
  const pageRows = sorted.slice(page * PAGE_SIZE, (page + 1) * PAGE_SIZE);
  const goTo = (n: number) => setPageAt({ view, page: Math.max(0, Math.min(n, pages - 1)) });
  const deactivatedCount = users.filter((u) => u.deactivated_at !== null).length;
  const auditUser = users.find((u) => u.email === auditing);

  const refresh = async () => {
    try {
      setUsers((await listUsers()).users);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  const toggleActive = async (u: RosterUser) => {
    const activate = u.deactivated_at !== null;
    if (
      !activate &&
      !window.confirm(
        `Deactivate ${u.email}? Every sign-in, session, API key and token for this email will be refused until it is reactivated. Nothing is deleted.`,
      )
    ) {
      return;
    }
    setErr("");
    setFlash("");
    setPending(u.email);
    try {
      const updated = await setUserActive(u.email, activate);
      setUsers((prev) => prev.map((x) => (x.email === updated.email ? updated : x)));
      setFlash(activate ? `${u.email} reactivated.` : `${u.email} deactivated.`);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setPending("");
    }
  };

  const changeGroup = async (u: RosterUser, group: string, member: boolean) => {
    setErr("");
    setFlash("");
    setPending(u.email);
    try {
      const g = await setGroupMember(group, u.email, member);
      setGroupMembers((prev) => ({ ...prev, [g.name]: g.members }));
      await refresh();
      setFlash(member ? `${u.email} added to ${group}.` : `${u.email} removed from ${group}.`);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setPending("");
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

      <div className="mb-2.5 flex items-baseline justify-between gap-3">
        <h2 className="text-[14px] font-semibold text-neutral-900">Users</h2>
        <span className="text-[12px] text-neutral-500">
          {users.length} total · {deactivatedCount} deactivated
        </span>
      </div>

      <div className="mb-2.5 flex flex-wrap items-center gap-2">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="filter by email, role, or group…"
          aria-label="filter users"
          className="w-full max-w-[320px] rounded-md border border-neutral-200 px-2.5 py-1.5 text-[12px] text-neutral-800 placeholder:text-neutral-400"
        />
        {(
          [
            ["type", kindF, setKindF, ["human", "service"]],
            ["role", roleF, setRoleF, roleOptions],
            ["group", groupF, setGroupF, groupOptions],
          ] as const
        ).map(([label, value, set, options]) => (
          <select
            key={label}
            value={value}
            onChange={(e) => set(e.target.value)}
            aria-label={`filter by ${label}`}
            className="max-w-[14rem] rounded-md border border-neutral-200 bg-white px-2 py-1.5 text-[12px] text-neutral-800"
          >
            <option value="">any {label}</option>
            {options.map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
        ))}
        <select
          value={statusF}
          onChange={(e) => setStatusF(e.target.value as typeof statusF)}
          aria-label="filter by status"
          className="max-w-[14rem] rounded-md border border-neutral-200 bg-white px-2 py-1.5 text-[12px] text-neutral-800"
        >
          <option value="">any status</option>
          <option value="active">active</option>
          <option value="deactivated">deactivated</option>
        </select>
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
            <col className="w-[34%]" />
            <col className="w-[23%]" />
            <col className="w-[28%]" />
            <col className="w-[15%]" />
            <col className="w-[7rem]" />
          </colgroup>
          <thead>
            <tr className="bg-neutral-50">
              {(
                [
                  ["email", "Email"],
                  ["roles", "Roles"],
                  ["groups", "Groups"],
                  ["last_seen", "Last sign-in"],
                  ["status", "Status"],
                ] as const
              ).map(([key, h]) => (
                <th
                  key={key}
                  aria-sort={sort.key === key ? (sort.dir === 1 ? "ascending" : "descending") : undefined}
                  className="border-b border-neutral-200 px-3 py-1.5 text-left text-[10px] font-semibold uppercase tracking-wider text-neutral-500"
                >
                  <button
                    type="button"
                    onClick={() =>
                      setSort((s) => (s.key === key ? { key, dir: s.dir === 1 ? -1 : 1 } : { key, dir: FIRST_DIR[key] }))
                    }
                    className="uppercase tracking-wider hover:text-neutral-800"
                  >
                    {h}
                    <span className="ml-1 inline-block w-2">{sort.key === key ? (sort.dir === 1 ? "▲" : "▼") : ""}</span>
                  </button>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {pageRows.map((u) => {
              const seen = relative(u.last_seen_at);
              const inactive = u.deactivated_at !== null;
              return (
                <tr key={u.email} className="border-b border-neutral-200 last:border-b-0 hover:bg-neutral-50">
                  <td className="px-3 py-2 align-top text-[13px] text-neutral-800">
                    <button
                      type="button"
                      onClick={() => setAuditing(u.email)}
                      className={
                        "block max-w-full truncate text-left hover:underline" +
                        (inactive ? " text-neutral-400 line-through" : "")
                      }
                      title={`${u.email} — permissions and credentials`}
                    >
                      {u.email}
                    </button>
                    <span className="mt-0.5 flex flex-wrap items-center gap-1">
                      {inactive ? (
                        <span
                          className="rounded border border-rose-200 bg-rose-50 px-1 text-[9px] uppercase tracking-wide text-rose-700"
                          title={`deactivated${u.deactivated_by ? ` by ${u.deactivated_by}` : ""} ${relative(u.deactivated_at).title}`}
                        >
                          deactivated
                        </span>
                      ) : null}
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
                    <GroupChips
                      user={u}
                      manageable={groupMembers}
                      busy={pending !== ""}
                      onChange={(g, member) => changeGroup(u, g, member)}
                    />
                  </td>
                  <td className="px-3 py-2 align-top text-[13px] text-neutral-800" title={seen.title}>
                    {u.last_seen_at ? seen.label : <span className="italic text-neutral-400">{seen.label}</span>}
                  </td>
                  <td className="px-3 py-2 align-top">
                    {u.email === currentEmail.toLowerCase() ? null : (
                      <button
                        type="button"
                        onClick={() => toggleActive(u)}
                        disabled={pending !== ""}
                        className={
                          "whitespace-nowrap rounded-md border px-2 py-1 text-[11px] disabled:opacity-40 " +
                          (inactive
                            ? "border-neutral-300 bg-white text-neutral-700 hover:bg-neutral-50"
                            : "border-rose-200 bg-white text-rose-700 hover:bg-rose-50")
                        }
                      >
                        {pending === u.email ? "…" : inactive ? "Reactivate" : "Deactivate"}
                      </button>
                    )}
                  </td>
                </tr>
              );
            })}
            {shown.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-3 py-8 text-center text-[12px] text-neutral-400">
                  {users.length === 0 ? "no users yet" : "nothing matches these filters"}
                </td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>

      <div className="mt-2.5 flex items-center justify-between gap-3 text-[12px] text-neutral-500">
        <span>
          {sorted.length === 0
            ? "0 shown"
            : `Showing ${page * PAGE_SIZE + 1}–${page * PAGE_SIZE + pageRows.length} of ${sorted.length}${
                sorted.length === users.length ? "" : " matching"
              }`}
        </span>
        {pages > 1 ? (
          <span className="flex items-center gap-1">
            <button type="button" aria-label="previous page" disabled={page === 0} onClick={() => goTo(page - 1)} className="rounded-md border border-neutral-200 px-2 py-0.5 disabled:opacity-40">
              ‹
            </button>
            <span>
              page {page + 1} of {pages}
            </span>
            <button type="button" aria-label="next page" disabled={page === pages - 1} onClick={() => goTo(page + 1)} className="rounded-md border border-neutral-200 px-2 py-0.5 disabled:opacity-40">
              ›
            </button>
          </span>
        ) : null}
      </div>

      {sources.length > 0 ? (
        <p className="mt-2.5 text-[11px] leading-relaxed text-neutral-400">
          assembled from {sources.join(", ")}. a principal that appears in none of these is not
          listed.
        </p>
      ) : null}

      {auditUser ? (
        <AccessAuditModal
          key={auditUser.email}
          email={auditUser.email}
          subtitle={`${auditUser.kind} · ${
            auditUser.last_seen_at ? `last sign-in ${relative(auditUser.last_seen_at).label}` : "no sign-in recorded"
          } · ${auditUser.deactivated_at === null ? "active" : "deactivated"}`}
          permissions={permissions}
          onClose={() => setAuditing("")}
        />
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
