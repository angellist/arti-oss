"use client";

import Link from "next/link";
import { useState } from "react";
import type { Block, BlockedDoc } from "@/lib/types";
import { addBlock, listBlockMatches, listBlocks, removeBlock } from "@/lib/arti";
import { relativeTime } from "@/lib/time";

// BlocksManager is the admin surface for the block list: the patterns that
// take documents away from every reader. Blocking is one click because it is
// the emergency action; lifting asks first, because lifting is what puts a
// document back in front of everyone.
export default function BlocksManager({ initialBlocks }: { initialBlocks: Block[] }) {
  const [blocks, setBlocks] = useState<Block[]>(initialBlocks);
  const [pattern, setPattern] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [matches, setMatches] = useState<Record<string, BlockedDoc[]>>({});

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

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (!pattern.trim()) return;
    void run(async () => {
      await addBlock(pattern.trim(), reason.trim());
      setPattern("");
      setReason("");
      setExpanded(null);
      setMatches({});
      setBlocks(await listBlocks());
    });
  };

  const lift = (p: string) =>
    run(async () => {
      if (!window.confirm(`Lift the block on "${p}"? Every document it hides becomes readable again.`)) return;
      await removeBlock(p);
      setExpanded(null);
      setBlocks(await listBlocks());
    });

  // Matches load on expand rather than with the list: a glob can cover a large
  // family, and most visits here are to add or lift one entry, not to audit.
  const toggle = (p: string) =>
    run(async () => {
      if (expanded === p) {
        setExpanded(null);
        return;
      }
      setExpanded(p);
      if (!matches[p]) {
        const found = await listBlockMatches(p);
        setMatches((m) => ({ ...m, [p]: found }));
      }
    });

  return (
    <div className="px-6 py-4">
      {err ? (
        <div className="mb-3 rounded-md border border-rose-200 bg-rose-50 px-3 py-2 text-[12px] text-rose-700">
          {err}
        </div>
      ) : null}

      <form onSubmit={submit} className="mb-5 flex flex-col gap-2 sm:flex-row">
        <input
          value={pattern}
          onChange={(e) => setPattern(e.target.value)}
          placeholder="slug, slug glob (team-notes-*), or artifact id"
          aria-label="pattern to block"
          className="min-w-0 flex-1 rounded-md border border-neutral-200 px-2.5 py-1.5 text-[13px] text-neutral-900 placeholder:text-neutral-400"
        />
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="reason (optional)"
          aria-label="reason"
          className="min-w-0 rounded-md border border-neutral-200 px-2.5 py-1.5 text-[13px] text-neutral-900 placeholder:text-neutral-400 sm:w-64"
        />
        <button
          type="submit"
          disabled={busy || !pattern.trim()}
          className="shrink-0 rounded-md border border-neutral-200 bg-white px-3 py-1.5 text-[13px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
        >
          Block
        </button>
      </form>

      {blocks.length === 0 ? (
        <p className="text-[12px] text-neutral-400">Nothing is blocked.</p>
      ) : (
        <table className="w-full table-fixed">
          <thead>
            <tr className="border-b border-neutral-200 text-left text-[11px] font-semibold uppercase tracking-wide text-neutral-500">
              <th className="w-[38%] py-1.5">Pattern</th>
              <th className="w-[22%] py-1.5">Blocked by</th>
              <th className="w-[12%] py-1.5">When</th>
              <th className="w-[20%] py-1.5">Reason</th>
              <th className="w-[8%] py-1.5" />
            </tr>
          </thead>
          <tbody>
            {blocks.map((b) => (
              <tr key={b.pattern} className="border-b border-neutral-100 align-top">
                <td className="py-2 pr-2">
                  <button
                    type="button"
                    onClick={() => void toggle(b.pattern)}
                    className="break-all text-left font-mono text-[12px] text-neutral-800 hover:underline"
                    aria-expanded={expanded === b.pattern}
                  >
                    {b.pattern}
                  </button>
                  {expanded === b.pattern ? <Matches docs={matches[b.pattern]} /> : null}
                </td>
                <td className="truncate py-2 pr-2 text-[12px] text-neutral-600">{b.created_by}</td>
                <td className="py-2 pr-2 text-[12px] text-neutral-500" title={b.created_at}>
                  {relativeTime(b.created_at)}
                </td>
                <td className="py-2 pr-2 text-[12px] text-neutral-600">{b.reason}</td>
                <td className="py-2 text-right">
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => void lift(b.pattern)}
                    className="rounded-md border border-neutral-200 bg-white px-2 py-1 text-[11px] text-neutral-700 hover:bg-neutral-50 disabled:opacity-50"
                  >
                    Lift
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

// Matches lists what one pattern hides right now. Each row links to the review
// page, which is the only place a blocked document can be read.
function Matches({ docs }: { docs?: BlockedDoc[] }) {
  if (!docs) return <p className="mt-1.5 text-[11px] text-neutral-400">loading…</p>;
  if (docs.length === 0) return <p className="mt-1.5 text-[11px] text-neutral-400">hides nothing right now</p>;
  return (
    <ul className="mt-1.5 space-y-1">
      {docs.map((d) => (
        <li key={d.artifact_id} className="text-[11px] text-neutral-500">
          {/* A slugless document is blocked by its id, so that id is also how
              the review page reaches it. */}
          <Link
            href={`/admin/blocked/${encodeURIComponent(d.named_slug || d.artifact_id)}`}
            className="text-neutral-700 hover:underline"
          >
            {d.title || d.named_slug || d.artifact_id}
          </Link>
          <span className="ml-1">
            · {d.creator}
            {d.version ? ` · v${d.version}` : ""} · {d.artifact_type}
          </span>
        </li>
      ))}
    </ul>
  );
}
