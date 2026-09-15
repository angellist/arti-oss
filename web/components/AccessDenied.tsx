import { DenialInfo } from "@/lib/types";

// AccessDenied replaces the bare 404 for a reader who followed a real link to
// a document that is closed to them. The 404 was accurate — the API cannot
// confirm a restricted artifact exists on its ordinary read path — but it read
// as a broken link, so the reader's next move was to go back to whoever shared
// it and ask what happened. This page answers that instead: what the document
// is, and the address that can grant access.
export default function AccessDenied({ info }: { info: DenialInfo }) {
  const subject = `arti access request: ${info.title}`;
  const body = [
    `Hi — I don't have access to this arti document and would like to read it.`,
    ``,
    info.named_slug ? `Document: ${info.title} (${info.named_slug})` : `Document: ${info.title}`,
  ].join("\n");
  const mailto = `mailto:${encodeURIComponent(info.owner)}?subject=${encodeURIComponent(subject)}&body=${encodeURIComponent(body)}`;

  return (
    <main className="flex min-h-screen items-start justify-center bg-neutral-50 px-4 py-16">
      <div className="w-full max-w-xl rounded-lg border border-neutral-200 bg-white p-8 shadow-sm">
        <p className="text-[11px] font-semibold uppercase tracking-wider text-amber-700">
          Access required
        </p>
        <h1 className="mt-3 text-2xl font-semibold text-neutral-900">{info.title}</h1>
        {info.named_slug ? (
          <p className="mt-1 font-mono text-[13px] text-neutral-500">{info.named_slug}</p>
        ) : null}

        <p className="mt-6 text-[14px] leading-6 text-neutral-700">
          This document exists, but it is not shared with you. Its owner can grant you access.
        </p>

        <dl className="mt-6 border-t border-neutral-200 pt-4 text-[14px]">
          <div className="flex items-baseline gap-3">
            <dt className="w-20 shrink-0 text-neutral-500">Owner</dt>
            <dd className="min-w-0 break-all font-mono text-[13px] text-neutral-900">
              {info.owner}
            </dd>
          </div>
        </dl>

        <a
          href={mailto}
          className="mt-6 inline-block rounded-md bg-neutral-900 px-4 py-2 text-[14px] font-medium text-white transition hover:bg-neutral-700"
        >
          Ask {info.owner} for access
        </a>
      </div>
    </main>
  );
}
