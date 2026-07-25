"use client";

import { useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import Link from "next/link";
import {
  hardDeleteArtifact,
  hasPerm,
  sameEmail,
  unarchiveArtifact,
} from "@/lib/arti";
import type { ArtifactInfo, Me } from "@/lib/types";
import { formatBytes } from "@/lib/format";
import CreatorName from "@/components/CreatorName";

const PAGE_SIZE = 50;

function timeAgo(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime();
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

export default function ArchivedTable({
  rows,
  total,
  page,
  me,
}: {
  rows: ArtifactInfo[];
  total: number;
  page: number;
  me: Me;
}) {
  const router = useRouter();
  const sp = useSearchParams();
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string>("");

  function pageHref(p: number): string {
    const next = new URLSearchParams(sp.toString());
    if (p <= 1) next.delete("page");
    else next.set("page", String(p));
    const qs = next.toString();
    return qs ? `/archived?${qs}` : "/archived";
  }

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const from = total === 0 ? 0 : (page - 1) * PAGE_SIZE + 1;
  const to = Math.min(total, from + rows.length - 1);

  const canActOn = (row: ArtifactInfo) => hasPerm(me, "MANAGE_ARTIFACTS") || sameEmail(row.creator, me.email);

  const doUnarchive = async (row: ArtifactInfo) => {
    if (!canActOn(row)) return;
    setBusy(row.artifact_id);
    setErr("");
    try {
      await unarchiveArtifact(row.artifact_id);
      router.refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  const doHardDelete = async (row: ArtifactInfo) => {
    if (!hasPerm(me, "MANAGE_ARTIFACTS")) return;
    const ok = window.confirm(
      `Permanently delete "${row.title}"? This cannot be undone — the row and any S3 blob references go away.`,
    );
    if (!ok) return;
    setBusy(row.artifact_id);
    setErr("");
    try {
      await hardDeleteArtifact(row.artifact_id);
      router.refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section>
      {err ? (
        <div className="border-b border-rose-200 bg-rose-50 px-6 py-2 text-xs text-rose-700">
          error: {err}
        </div>
      ) : null}
      <div className="overflow-x-auto">
        <table className="min-w-full table-fixed bg-white text-[13px] leading-snug">
          <thead className="border-b border-neutral-200 bg-neutral-50 text-left text-[11px] uppercase tracking-wider text-neutral-500">
            <tr>
              <th className="min-w-[280px] w-[40%] pl-6 pr-2 py-2 font-medium">title</th>
              <th className="w-44 px-2 py-2 font-medium">slug</th>
              <th className="w-36 px-2 py-2 font-medium">creator</th>
              <th className="w-36 px-2 py-2 font-medium">archived</th>
              <th className="w-56 px-2 pr-6 py-2 font-medium">actions</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-6 py-10 text-center text-neutral-400">
                  no archived artifacts
                </td>
              </tr>
            ) : (
              rows.map((row) => (
                <tr key={row.artifact_id} className="border-b border-neutral-100">
                  <td className="pl-6 pr-2 py-2">
                    <div className="flex items-center gap-2 truncate">
                      <Link
                        href={`/a/${row.artifact_id}`}
                        className="truncate text-blue-700 hover:underline"
                      >
                        {row.title}
                      </Link>
                      {row.artifact_type === "PACKAGE" ? <span aria-label="package">📦</span> : null}
                    </div>
                    <div className="mt-0.5 text-[11px] text-neutral-500">
                      {row.content_type}
                      {row.size_bytes != null ? (
                        <span> · {formatBytes(row.size_bytes)}</span>
                      ) : null}
                    </div>
                  </td>
                  <td className="px-2 py-2 font-mono text-[12px] text-neutral-600">
                    {row.named_slug ?? "—"}
                    {row.version != null ? (
                      <span className="text-neutral-400"> v{row.version}</span>
                    ) : null}
                  </td>
                  <td className="px-2 py-2 text-neutral-600">
                    <CreatorName email={row.creator} />
                  </td>
                  <td className="px-2 py-2 text-neutral-500" title={row.deleted_at ?? undefined}>
                    {row.deleted_at ? timeAgo(row.deleted_at) : "—"}
                  </td>
                  <td className="px-2 pr-6 py-2">
                    <div className="flex gap-1.5">
                      <button
                        type="button"
                        disabled={!canActOn(row) || busy === row.artifact_id}
                        onClick={() => doUnarchive(row)}
                        className="rounded-md border border-neutral-200 bg-white px-2 py-1 text-[11px] text-neutral-700 transition hover:bg-neutral-50 disabled:cursor-not-allowed disabled:opacity-40"
                        title={canActOn(row) ? "restore" : "only the creator or an admin can unarchive"}
                      >
                        ↩ unarchive
                      </button>
                      <button
                        type="button"
                        disabled={!hasPerm(me, "MANAGE_ARTIFACTS") || busy === row.artifact_id}
                        onClick={() => doHardDelete(row)}
                        className="rounded-md border border-rose-200 bg-white px-2 py-1 text-[11px] text-rose-700 transition hover:bg-rose-50 disabled:cursor-not-allowed disabled:opacity-40"
                        title={hasPerm(me, "MANAGE_ARTIFACTS") ? "permanently delete (admin)" : "delete is admin-only"}
                      >
                        🗑 delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      <nav className="flex items-center justify-between border-t border-neutral-200 bg-white px-6 py-2 text-[11px] text-neutral-500">
        <span>
          {total === 0 ? (
            "no archived artifacts"
          ) : (
            <>
              showing <span className="font-medium text-neutral-700">{from}</span>–
              <span className="font-medium text-neutral-700">{to}</span> of{" "}
              <span className="font-medium text-neutral-700">{total}</span>
            </>
          )}
        </span>
        {totalPages > 1 ? (
          <div className="flex items-center gap-1">
            <PageLink
              disabled={page <= 1}
              href={pageHref(page - 1)}
              label="← prev"
            />
            <span className="px-2 text-neutral-500">
              page {page} of {totalPages}
            </span>
            <PageLink
              disabled={page >= totalPages}
              href={pageHref(page + 1)}
              label="next →"
            />
          </div>
        ) : null}
      </nav>
    </section>
  );
}

function PageLink({
  href,
  label,
  disabled,
}: {
  href: string;
  label: string;
  disabled: boolean;
}) {
  if (disabled) {
    return (
      <span className="rounded border border-neutral-100 px-2.5 py-0.5 text-neutral-300">
        {label}
      </span>
    );
  }
  return (
    <Link
      href={href}
      className="rounded border border-neutral-200 px-2.5 py-0.5 text-neutral-600 transition hover:bg-neutral-50"
    >
      {label}
    </Link>
  );
}
