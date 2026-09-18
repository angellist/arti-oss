import Link from "next/link";
import { headers } from "next/headers";
import { notFound } from "next/navigation";
import type { Metadata } from "next";
import { fetchBlockedBody, getBlockedDoc } from "@/lib/arti";
import { relativeTime } from "@/lib/time";

export const dynamic = "force-dynamic";

export const metadata: Metadata = { title: "blocked document" };

// The review page for one blocked document. It is deliberately NOT the viewer:
// /s/<slug> answers 404 for everyone, admins included, and this URL is the one
// exception, so the two never share a path and no ordinary read grows an admin
// branch. The server refuses any slug that is not currently blocked, so this
// page cannot show an ordinary document.
//
// The body is rendered as text, never as markup. The server sends it as plain
// text under nosniff for the same reason: a document blocked for being
// dangerous should not run in the session of the person reviewing it.
export default async function BlockedDocumentPage({ params }: { params: Promise<{ ident: string }> }) {
  const { ident } = await params;
  const cookie = (await headers()).get("cookie") ?? undefined;

  const doc = await getBlockedDoc(ident, cookie).catch(() => null);
  if (!doc) notFound();

  const body = doc.body_readable ? await fetchBlockedBody(ident, cookie).catch(() => null) : null;

  return (
    <main className="min-h-screen pb-12">
      <div className="border-b border-rose-200 bg-rose-50 px-6 py-3">
        <p className="text-[13px] font-semibold text-rose-800">
          Blocked · nobody else can see this document
        </p>
        <p className="mt-0.5 text-[12px] text-rose-700">
          This page is the only way to read it. The document itself is unchanged; lift the block
          on the{" "}
          <Link href="/settings/blocks" className="underline">
            Blocked Documents
          </Link>{" "}
          page to put it back in front of everyone.
        </p>
      </div>

      <div className="px-6 pt-4">
        <h1 className="text-base font-semibold text-neutral-900">{doc.title}</h1>
        <p className="mt-1 text-[12px] text-neutral-500">
          <span className="font-mono">{doc.named_slug ?? doc.artifact_id}</span>
          {doc.version ? ` · v${doc.version}` : ""} · {doc.artifact_type} · {doc.content_type} ·{" "}
          {doc.creator} · <span title={doc.created_at}>{relativeTime(doc.created_at)}</span>
          {doc.archived ? " · archived" : ""}
        </p>
        {doc.description ? <p className="mt-2 text-[13px] text-neutral-700">{doc.description}</p> : null}
      </div>

      <div className="px-6 py-4">
        {body ? (
          <>
            {body.truncated ? (
              <p className="mb-2 text-[12px] text-neutral-500">
                Showing the first {Math.round(doc.max_bytes / 1024)} KiB. Lift the block to read the
                rest.
              </p>
            ) : null}
            <pre className="overflow-x-auto whitespace-pre-wrap break-words rounded-md border border-neutral-200 bg-neutral-50 p-3 text-[12px] text-neutral-800">
              {body.body}
            </pre>
          </>
        ) : (
          <p className="text-[12px] text-neutral-500">
            {doc.body_readable
              ? "The body could not be read."
              : `A ${doc.artifact_type} is a zip, so it cannot be reviewed as text here. Lift the block to open it.`}
          </p>
        )}
      </div>
    </main>
  );
}
