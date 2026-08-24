"use client";

import { useEffect, useRef, useState } from "react";
import { EmailSuggest } from "@/components/EmailSuggest";
import type { Group } from "@/lib/types";
import { createGroup, deleteGroup, listGroups, updateGroup } from "@/lib/arti";

// GroupsManager is the admin CRUD surface for user groups. A left list selects
// a group; the right panel edits its name (click the title, like an artifact
// title) and membership (a row per member, add via the input, remove via ×).
// Member edits auto-save, mirroring the access popover.
export default function GroupsManager({ initialGroups }: { initialGroups: Group[] }) {
  const [groups, setGroups] = useState<Group[]>(initialGroups);
  const [selected, setSelected] = useState<string | null>(initialGroups[0]?.name ?? null);
  const [creating, setCreating] = useState(false);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const current = groups.find((g) => g.name === selected) ?? null;

  // refresh re-pulls the canonical list after any mutation so member counts /
  // normalization (server-side lowercasing, dedupe) are reflected.
  const refresh = async (keep?: string | null) => {
    const gs = await listGroups();
    setGroups(gs);
    if (keep !== undefined) setSelected(keep);
    else if (selected && !gs.find((g) => g.name === selected)) setSelected(gs[0]?.name ?? null);
  };

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

  const saveMembers = (next: string[]) =>
    run(async () => {
      if (!current) return;
      await updateGroup(current.name, { members: next });
      await refresh(current.name);
    });

  const saveDisplayName = (displayName: string) =>
    run(async () => {
      if (!current) return;
      await updateGroup(current.name, { display_name: displayName });
      await refresh(current.name);
    });

  const remove = () =>
    run(async () => {
      if (!current) return;
      if (!window.confirm(`Delete group "${current.name}"? Artifacts granted to it lose that access.`)) return;
      await deleteGroup(current.name);
      await refresh(null);
    });

  return (
    <div className="flex flex-col gap-0 md:flex-row">
      {/* ─── left: group list ─────────────────────────────────────── */}
      <div className="w-full shrink-0 border-b border-neutral-200 md:w-64 md:border-b-0 md:border-r">
        <div className="flex items-center justify-between px-4 py-3">
          <span className="text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
            {groups.length} group{groups.length === 1 ? "" : "s"}
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
          {groups.map((g) => (
            <li key={g.name}>
              <button
                type="button"
                onClick={() => {
                  setSelected(g.name);
                  setCreating(false);
                  setErr("");
                }}
                className={
                  "block w-full px-4 py-2 text-left hover:bg-neutral-50 " +
                  (selected === g.name && !creating ? "bg-neutral-100" : "")
                }
              >
                <div className="truncate text-[13px] text-neutral-800">{g.display_name || g.name}</div>
                <div className="flex items-center gap-1 truncate text-[11px] text-neutral-400">
                  <SlugBadge slug={g.name} />· {g.member_count} member{g.member_count === 1 ? "" : "s"}
                </div>
              </button>
            </li>
          ))}
          {groups.length === 0 && !creating ? (
            <li className="px-4 py-3 text-[12px] text-neutral-400">No groups yet. Create one.</li>
          ) : null}
        </ul>
      </div>

      {/* ─── right: detail / create ────────────────────────────────── */}
      <div className="min-w-0 flex-1 px-6 py-4">
        {err ? (
          <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
            {err}
          </div>
        ) : null}

        {creating ? (
          <CreateGroupForm
            busy={busy}
            onCancel={() => setCreating(false)}
            onCreate={(name, displayName, members) =>
              run(async () => {
                const g = await createGroup({ name, display_name: displayName, members });
                setCreating(false);
                await refresh(g.name);
              })
            }
          />
        ) : current ? (
          <div className="max-w-xl">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <EditableGroupName
                  key={current.name}
                  displayName={current.display_name}
                  fallback={current.name}
                  busy={busy}
                  onSave={saveDisplayName}
                />
                <div className="mt-0.5">
                  <SlugBadge slug={current.name} />
                </div>
              </div>
              <button
                type="button"
                onClick={remove}
                disabled={busy}
                className="shrink-0 rounded-md border border-neutral-200 px-2.5 py-1 text-[11px] text-rose-600 hover:bg-rose-50 disabled:opacity-50"
              >
                Delete group
              </button>
            </div>

            <div className="mt-5">
              <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
                Members ({current.member_count})
              </label>
              <MembersEditor key={current.name} members={current.members} busy={busy} onChange={saveMembers} />
              <p className="mt-1.5 text-[10px] leading-snug text-neutral-500">
                Members are emails or globs (e.g. <code className="bg-neutral-100 px-1">*@example.com</code>). Changes
                save immediately and apply to every artifact granted to this group.
              </p>
            </div>
          </div>
        ) : (
          <div className="text-[13px] text-neutral-400">Select a group, or create one.</div>
        )}
      </div>
    </div>
  );
}

// EditableGroupName shows the group's display name (falling back to the slug)
// and turns into an input on click — same interaction as the artifact title:
// Enter saves, Esc/blur cancels. Saving an empty value clears the display name
// (the slug shows through).
function EditableGroupName({
  displayName,
  fallback,
  busy,
  onSave,
}: {
  displayName: string;
  fallback: string;
  busy: boolean;
  onSave: (v: string) => void;
}) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(displayName);
  const inputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (editing) inputRef.current?.select();
  }, [editing]);

  // Seed the draft from the latest displayName each time editing starts (the
  // component is keyed by group name, so it also remounts on group switch).
  const startEdit = () => {
    setDraft(displayName);
    setEditing(true);
  };
  const commit = () => {
    const next = draft.trim();
    if (next !== displayName) onSave(next);
    setEditing(false);
  };
  const cancel = () => {
    setDraft(displayName);
    setEditing(false);
  };

  if (editing) {
    return (
      <input
        ref={inputRef}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          } else if (e.key === "Escape") {
            e.preventDefault();
            cancel();
          }
        }}
        onBlur={cancel}
        placeholder={fallback}
        disabled={busy}
        aria-label="edit group name"
        className="rounded-md border border-blue-300 bg-white px-1.5 py-0.5 text-[15px] font-semibold text-neutral-900 focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
      />
    );
  }
  return (
    <h2
      onClick={startEdit}
      title="click to edit name"
      className="-mx-1 cursor-text truncate rounded px-1 text-[15px] font-semibold text-neutral-900 hover:bg-neutral-100"
    >
      {displayName || fallback}
    </h2>
  );
}

// MembersEditor renders members as a list — one per row with a × to remove —
// and an input at the top to add. add/remove lift the new array via onChange.
function MembersEditor({
  members,
  busy,
  onChange,
}: {
  members: string[];
  busy: boolean;
  onChange: (next: string[]) => void;
}) {
  const [input, setInput] = useState("");

  const add = () => {
    const v = input.trim().toLowerCase();
    if (!v || members.includes(v)) {
      setInput("");
      return;
    }
    onChange([...members, v]);
    setInput("");
  };
  const remove = (m: string) => onChange(members.filter((x) => x !== m));

  return (
    <div className="rounded-md border border-neutral-200">
      <div className="flex items-center gap-2 border-b border-neutral-100 p-2">
        {/* Suggestions only — globs and not-yet-signed-in addresses stay
            typeable, since membership grants to an address, not an account. */}
        <EmailSuggest
          value={input}
          onChange={setInput}
          onSubmit={add}
          exclude={members}
          disabled={busy}
          placeholder="add an email or *@domain — press Enter"
        />
        <button
          type="button"
          onClick={add}
          disabled={busy || !input.trim()}
          className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
        >
          Add
        </button>
      </div>
      {members.length === 0 ? (
        <div className="px-3 py-2 text-[12px] text-neutral-400">No members yet.</div>
      ) : (
        <ul className="divide-y divide-neutral-100">
          {members.map((m) => (
            <li
              key={m}
              className="flex items-center justify-between gap-2 px-3 py-1.5 text-[13px] text-neutral-800"
            >
              {/* Members are emails/globs, i.e. prose-shaped identifiers — they
                  read better in the UI sans than in mono, which is reserved for
                  slugs and code. */}
              <span className="truncate font-sans">{m}</span>
              <button
                type="button"
                onClick={() => remove(m)}
                disabled={busy}
                aria-label={`remove ${m}`}
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
  );
}

function CreateGroupForm({
  busy,
  onCancel,
  onCreate,
}: {
  busy: boolean;
  onCancel: () => void;
  onCreate: (name: string, displayName: string, members: string[]) => void;
}) {
  const [name, setName] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [members, setMembers] = useState<string[]>([]);

  return (
    <div className="max-w-xl">
      <h2 className="text-[15px] font-semibold text-neutral-900">New group</h2>
      <div className="mt-4">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          Name (slug)
        </label>
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="engineering"
          disabled={busy}
          className="w-full rounded-md border border-neutral-200 bg-white px-2 py-1 font-mono text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
        <p className="mt-1 text-[10px] text-neutral-500">
          lowercase letters, digits, and dashes — the group&apos;s unique slug (can&apos;t be changed later).
        </p>
      </div>
      <div className="mt-4">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          Display name
        </label>
        <input
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder="(optional) Engineering"
          disabled={busy}
          className="w-full rounded-md border border-neutral-200 bg-white px-2 py-1 text-[13px] focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-200"
        />
      </div>
      <div className="mt-4">
        <label className="mb-1 block text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
          Members
        </label>
        <MembersEditor members={members} busy={busy} onChange={setMembers} />
      </div>
      <div className="mt-5 flex items-center gap-2">
        <button
          type="button"
          onClick={() => onCreate(name, displayName, members)}
          disabled={busy || !name.trim()}
          className="rounded-md bg-blue-600 px-3 py-1.5 text-[12px] font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          Create group
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

// SlugBadge shows a group's slug in a small pill, replacing the confusing
// `group:<slug>` token text. The slug is the stable identifier; the display
// name is the human label shown next to it.
function SlugBadge({ slug }: { slug: string }) {
  return (
    <span
      title="group slug"
      className="inline-block rounded bg-neutral-100 px-1.5 py-px font-mono text-[10px] text-neutral-500"
    >
      {slug}
    </span>
  );
}
