"use client";

import { useEffect, useState } from "react";
import type { Group, Role, RoleAssignment, UserAccess } from "@/lib/types";
import {
  assignRole,
  createRole,
  deleteRole,
  listAssignments,
  listGroups,
  listRoles,
  lookupUserAccess,
  unassignRole,
  updateRole,
} from "@/lib/arti";

// Human labels for the opaque permission keys. Unknown keys fall back to the
// raw string so a newly-added permission still renders.
const PERM_LABEL: Record<string, string> = {
  MANAGE_ROLES: "Manage roles",
  MANAGE_USER_GROUPS: "Manage user groups",
  MANAGE_ARTIFACTS: "Manage all artifacts",
  MANAGE_SKILLS: "Manage skills",
  USE_ARTIFACTS: "Use artifacts",
};
const permLabel = (k: string) => PERM_LABEL[k] ?? k;

export default function RolesManager({
  initialRoles,
  permissions,
}: {
  initialRoles: Role[];
  permissions: string[];
}) {
  const [roles, setRoles] = useState<Role[]>(initialRoles);
  const [selected, setSelected] = useState<string | null>(initialRoles[0]?.name ?? null);
  const [creating, setCreating] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const current = roles.find((r) => r.name === selected) ?? null;

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

  const refresh = async (keep?: string | null) => {
    const rs = await listRoles();
    setRoles(rs);
    if (keep !== undefined) setSelected(keep);
  };

  return (
    <div className="px-6 py-4">
      {err ? (
        <div className="mb-3 max-w-3xl rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
          {err}
        </div>
      ) : null}

      <UserLookup permissions={permissions} />

      <h2 className="mb-2 mt-8 text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Roles</h2>
      <div className="flex flex-col gap-0 rounded-md border border-neutral-200 md:flex-row">
        {/* left: role list */}
        <div className="w-full shrink-0 border-b border-neutral-200 md:w-56 md:border-b-0 md:border-r">
          <div className="flex items-center justify-between px-3 py-2">
            <span className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
              {roles.length} role{roles.length === 1 ? "" : "s"}
            </span>
            <button
              type="button"
              onClick={() => {
                setCreating(true);
                setSelected(null);
                setErr("");
              }}
              className="rounded-md border border-neutral-200 bg-white px-2 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50"
            >
              + New
            </button>
          </div>
          <ul>
            {roles.map((r) => (
              <li key={r.name}>
                <button
                  type="button"
                  onClick={() => {
                    setSelected(r.name);
                    setCreating(false);
                    setErr("");
                  }}
                  className={
                    "block w-full px-3 py-2 text-left hover:bg-neutral-50 " +
                    (selected === r.name && !creating ? "bg-neutral-100" : "")
                  }
                >
                  <div className="flex items-center gap-1.5 text-[13px] text-neutral-800">
                    {r.name}
                    {r.builtin ? (
                      <span className="rounded bg-neutral-100 px-1 text-[9px] uppercase text-neutral-400">built-in</span>
                    ) : null}
                  </div>
                  <div className="text-[11px] text-neutral-400">
                    {r.permissions.length} permission{r.permissions.length === 1 ? "" : "s"}
                  </div>
                </button>
              </li>
            ))}
          </ul>
        </div>

        {/* right: editor / create */}
        <div className="min-w-0 flex-1 p-4">
          {creating ? (
            <CreateRoleForm
              permissions={permissions}
              busy={busy}
              onCancel={() => setCreating(false)}
              onCreate={(name, description, perms) =>
                run(async () => {
                  await createRole({ name, description, permissions: perms });
                  setCreating(false);
                  await refresh(name);
                })
              }
            />
          ) : current ? (
            <RoleEditor
              key={current.name}
              role={current}
              permissions={permissions}
              busy={busy}
              onSavePerms={(perms) => run(async () => { await updateRole(current.name, { permissions: perms }); await refresh(current.name); })}
              onSaveDescription={(d) => run(async () => { await updateRole(current.name, { description: d }); await refresh(current.name); })}
              onDelete={() =>
                run(async () => {
                  if (!window.confirm(`Delete role "${current.name}"? Its assignments are removed.`)) return;
                  await deleteRole(current.name);
                  await refresh(null);
                })
              }
            />
          ) : (
            <div className="text-[13px] text-neutral-400">Select a role, or create one.</div>
          )}
        </div>
      </div>
    </div>
  );
}

// ─── per-user lookup matrix ──────────────────────────────────────────

function UserLookup({ permissions }: { permissions: string[] }) {
  const [email, setEmail] = useState("");
  const [access, setAccess] = useState<UserAccess | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const lookup = async () => {
    const e = email.trim();
    if (!e) return;
    setBusy(true);
    setErr("");
    try {
      setAccess(await lookupUserAccess(e));
    } catch (ex) {
      setErr(ex instanceof Error ? ex.message : String(ex));
      setAccess(null);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-3xl rounded-md border border-neutral-200 p-4">
      <h2 className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Look up a person</h2>
      <div className="mt-2 flex items-center gap-2">
        <input
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              lookup();
            }
          }}
          placeholder="email@example.com"
          disabled={busy}
          className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-white px-2 py-1 text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
        <button
          type="button"
          onClick={lookup}
          disabled={busy || !email.trim()}
          className="rounded-md bg-blue-600 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          Look up
        </button>
      </div>
      {err ? <p className="mt-2 text-[11px] text-rose-600">{err}</p> : null}
      {access ? <AccessMatrix access={access} permissions={permissions} /> : null}
    </div>
  );
}

// AccessMatrix renders one column per role the person holds and one row per
// permission, a dot where the role grants it — so the union across columns is
// the person's effective access.
function AccessMatrix({ access, permissions }: { access: UserAccess; permissions: string[] }) {
  if (access.roles.length === 0) {
    return <p className="mt-3 text-[12px] text-neutral-500">{access.email} holds no roles.</p>;
  }
  const effective = new Set(access.effective_permissions);
  return (
    <div className="mt-3 overflow-x-auto">
      <table className="border-collapse text-[12px]">
        <thead>
          <tr>
            <th className="border-b border-neutral-200 px-2 py-1 text-left font-medium text-neutral-500">Permission</th>
            {access.roles.map((role, i) => (
              <th key={role.name + ":" + role.source + ":" + i} className="border-b border-neutral-200 px-3 py-1 text-center">
                <div className="font-medium text-neutral-800">{role.name}</div>
                <div className="text-[10px] font-normal text-neutral-400">
                  {role.source.startsWith("group:") ? "from " + role.source : role.source}
                </div>
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {permissions.map((perm) => (
            <tr key={perm} className={effective.has(perm) ? "" : "opacity-60"}>
              <td className="border-b border-neutral-100 px-2 py-1 text-neutral-700">
                {permLabel(perm)}
                <span className="ml-1 font-mono text-[10px] text-neutral-400">{perm}</span>
              </td>
              {access.roles.map((role, i) => (
                <td key={role.name + ":" + role.source + ":" + i} className="border-b border-neutral-100 px-3 py-1 text-center">
                  {role.permissions.includes(perm) ? (
                    <span className="inline-block h-2 w-2 rounded-full bg-emerald-500" aria-label="granted" />
                  ) : (
                    <span className="text-neutral-300">·</span>
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ─── role editor ─────────────────────────────────────────────────────

function RoleEditor({
  role,
  permissions,
  busy,
  onSavePerms,
  onSaveDescription,
  onDelete,
}: {
  role: Role;
  permissions: string[];
  busy: boolean;
  onSavePerms: (perms: string[]) => void;
  onSaveDescription: (d: string) => void;
  onDelete: () => void;
}) {
  const [desc, setDesc] = useState(role.description);
  const has = (p: string) => role.permissions.includes(p);
  const toggle = (p: string) => {
    const next = has(p) ? role.permissions.filter((x) => x !== p) : [...role.permissions, p];
    onSavePerms(next);
  };

  // Built-in roles are fixed (permissions + description not editable). USER is
  // also not assignable (everyone has it); ADMIN can be assigned.
  const isUser = role.name === "USER";
  const permsLocked = role.builtin;
  const descLocked = role.builtin;

  return (
    <div className="max-w-xl">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="flex items-center gap-1.5 text-[15px] font-semibold text-neutral-900">
            {role.name}
            {role.builtin ? (
              <span className="rounded bg-neutral-100 px-1 text-[9px] uppercase text-neutral-400">built-in</span>
            ) : null}
          </h3>
        </div>
        <button
          type="button"
          onClick={onDelete}
          disabled={busy || role.builtin}
          title={role.builtin ? "built-in roles can't be deleted" : "delete role"}
          className="shrink-0 rounded-md border border-neutral-200 px-2.5 py-1 text-[11px] text-rose-600 hover:bg-rose-50 disabled:cursor-not-allowed disabled:opacity-40"
        >
          Delete role
        </button>
      </div>

      {isUser ? (
        <p className="mt-2 rounded-md border border-neutral-200 bg-neutral-50 px-3 py-2 text-[12px] text-neutral-500">
          The default role for every authenticated user. It&apos;s built-in — its permissions are fixed and it
          can&apos;t be assigned (everyone has it automatically).
        </p>
      ) : role.builtin ? (
        <p className="mt-2 rounded-md border border-neutral-200 bg-neutral-50 px-3 py-2 text-[12px] text-neutral-500">
          Built-in role with full access. It&apos;s fixed — its permissions and description can&apos;t be changed —
          but you can assign it to people and groups below.
        </p>
      ) : null}

      <div className="mt-3">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Description</label>
        <div className="flex items-center gap-2">
          <input
            value={desc}
            onChange={(e) => setDesc(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && desc !== role.description) onSaveDescription(desc);
            }}
            disabled={busy || descLocked}
            className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-white px-2 py-1 text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200 disabled:bg-neutral-50 disabled:text-neutral-400"
          />
          {!descLocked && desc !== role.description ? (
            <button
              type="button"
              onClick={() => onSaveDescription(desc)}
              disabled={busy}
              className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
            >
              Save
            </button>
          ) : null}
        </div>
      </div>

      <div className="mt-4">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Permissions</label>
        <ul className="rounded-md border border-neutral-200">
          {permissions.map((p) => (
            <li key={p} className="flex items-center gap-2 border-b border-neutral-100 px-3 py-1.5 last:border-b-0">
              <input
                type="checkbox"
                checked={has(p)}
                disabled={busy || permsLocked}
                onChange={() => toggle(p)}
                aria-label={p}
              />
              <span className={"text-[13px] " + (permsLocked ? "text-neutral-400" : "text-neutral-800")}>{permLabel(p)}</span>
              <span className="font-mono text-[10px] text-neutral-400">{p}</span>
            </li>
          ))}
        </ul>
        {!permsLocked ? <p className="mt-1 text-[10px] text-neutral-500">Changes save immediately.</p> : null}
      </div>

      {isUser ? null : <AssignmentsEditor role={role.name} busy={busy} />}
    </div>
  );
}

// ─── assignments ─────────────────────────────────────────────────────

function AssignmentsEditor({ role, busy }: { role: string; busy: boolean }) {
  const [assignments, setAssignments] = useState<RoleAssignment[]>([]);
  const [ptype, setPtype] = useState<"user" | "group">("user");
  const [pid, setPid] = useState("");
  const [focused, setFocused] = useState(false);
  const [groups, setGroups] = useState<Group[]>([]);
  const [err, setErr] = useState("");

  const reload = () => listAssignments(role).then(setAssignments).catch(() => setAssignments([]));
  useEffect(() => {
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [role]);
  useEffect(() => {
    listGroups().then(setGroups).catch(() => setGroups([]));
  }, []);

  // Group typeahead: groups matching the typed text that aren't already
  // assigned this role.
  const assignedGroups = new Set(
    assignments.filter((a) => a.principal_type === "group").map((a) => a.principal_id),
  );
  const q = pid.trim().toLowerCase();
  const groupMatches =
    ptype === "group"
      ? groups
          .filter((g) => !assignedGroups.has(g.name))
          .filter((g) => q === "" || g.name.toLowerCase().includes(q) || g.display_name.toLowerCase().includes(q))
          .slice(0, 6)
      : [];

  const add = async (explicitId?: string) => {
    const id = (explicitId ?? pid).trim();
    if (!id) return;
    setErr("");
    try {
      await assignRole({ principal_type: ptype, principal_id: id, role_name: role });
      setPid("");
      await reload();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };
  const remove = async (a: RoleAssignment) => {
    setErr("");
    try {
      await unassignRole({ principal_type: a.principal_type, principal_id: a.principal_id, role_name: role });
      await reload();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div className="mt-5">
      <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
        Assigned to ({assignments.length})
      </label>
      <div className="rounded-md border border-neutral-200">
        <div className="border-b border-neutral-100 p-2">
          <div className="flex items-center gap-2">
            <select
              value={ptype}
              onChange={(e) => {
                setPtype(e.target.value as "user" | "group");
                setPid("");
              }}
              disabled={busy}
              className="rounded-md border border-neutral-200 bg-white px-1.5 py-1 text-[12px]"
            >
              <option value="user">user</option>
              <option value="group">group</option>
            </select>
            <input
              value={pid}
              onChange={(e) => setPid(e.target.value)}
              onFocus={() => setFocused(true)}
              onBlur={() => setTimeout(() => setFocused(false), 120)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  add();
                }
              }}
              placeholder={ptype === "user" ? "email@example.com" : "search your groups"}
              disabled={busy}
              className="min-w-0 flex-1 rounded-md border border-neutral-200 bg-white px-2 py-1 text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
            />
            <button
              type="button"
              onClick={() => add()}
              disabled={busy || !pid.trim()}
              className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
            >
              Add
            </button>
          </div>
          {ptype === "group" && focused && groupMatches.length > 0 ? (
            <ul className="mt-1 max-h-40 overflow-auto rounded-md border border-neutral-200 bg-white py-1">
              {groupMatches.map((g) => (
                <li key={g.name}>
                  <button
                    type="button"
                    onMouseDown={(e) => {
                      e.preventDefault();
                      add(g.name);
                    }}
                    className="block w-full px-2.5 py-1 text-left hover:bg-neutral-50"
                  >
                    <span className="text-[13px] text-neutral-800">{g.display_name || g.name}</span>
                    <span className="ml-1.5 rounded bg-neutral-100 px-1 font-mono text-[10px] text-neutral-500">{g.name}</span>
                  </button>
                </li>
              ))}
            </ul>
          ) : ptype === "group" && focused && q !== "" ? (
            <p className="mt-1 px-1 text-[11px] text-neutral-400">No matching group.</p>
          ) : null}
        </div>
        {assignments.length === 0 ? (
          <div className="px-3 py-2 text-[12px] text-neutral-400">No one assigned yet.</div>
        ) : (
          <ul className="divide-y divide-neutral-100">
            {assignments.map((a) => (
              <li key={a.principal_type + ":" + a.principal_id} className="flex items-center justify-between gap-2 px-3 py-1.5 text-[13px]">
                <span className="truncate">
                  <span className="mr-1 rounded bg-neutral-100 px-1 text-[10px] uppercase text-neutral-400">{a.principal_type}</span>
                  <span className="font-mono text-neutral-800">{a.principal_id}</span>
                </span>
                <button
                  type="button"
                  onClick={() => remove(a)}
                  disabled={busy}
                  aria-label={`remove ${a.principal_id}`}
                  title="remove"
                  className="shrink-0 rounded p-1 text-neutral-400 hover:bg-rose-50 hover:text-rose-600 disabled:opacity-50"
                >
                  ×
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      {err ? <p className="mt-1 text-[11px] text-rose-600">{err}</p> : null}
    </div>
  );
}

function CreateRoleForm({
  permissions,
  busy,
  onCancel,
  onCreate,
}: {
  permissions: string[];
  busy: boolean;
  onCancel: () => void;
  onCreate: (name: string, description: string, perms: string[]) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [perms, setPerms] = useState<string[]>([]);
  const toggle = (p: string) => setPerms((ps) => (ps.includes(p) ? ps.filter((x) => x !== p) : [...ps, p]));

  return (
    <div className="max-w-xl">
      <h3 className="text-[15px] font-semibold text-neutral-900">New role</h3>
      <div className="mt-3">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Name</label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="EDITOR"
          disabled={busy}
          className="w-full rounded-md border border-neutral-200 bg-white px-2 py-1 font-mono text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
      </div>
      <div className="mt-3">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Description</label>
        <input
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          placeholder="(optional)"
          disabled={busy}
          className="w-full rounded-md border border-neutral-200 bg-white px-2 py-1 text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
      </div>
      <div className="mt-3">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">Permissions</label>
        <ul className="rounded-md border border-neutral-200">
          {permissions.map((p) => (
            <li key={p} className="flex items-center gap-2 border-b border-neutral-100 px-3 py-1.5 last:border-b-0">
              <input type="checkbox" checked={perms.includes(p)} disabled={busy} onChange={() => toggle(p)} aria-label={p} />
              <span className="text-[13px] text-neutral-800">{permLabel(p)}</span>
              <span className="font-mono text-[10px] text-neutral-400">{p}</span>
            </li>
          ))}
        </ul>
      </div>
      <div className="mt-4 flex items-center gap-2">
        <button
          type="button"
          onClick={() => onCreate(name, description, perms)}
          disabled={busy || !name.trim()}
          className="rounded-md bg-blue-600 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          Create role
        </button>
        <button
          type="button"
          onClick={onCancel}
          disabled={busy}
          className="rounded-md border border-neutral-200 bg-white px-3 py-1.5 text-[12px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}
