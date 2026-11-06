// In-place text editing of a standalone HTML artifact. The page is served with
// ?edit=1 into a sandboxed frame whose bridge script reports changed blocks;
// the server splices them into the stored bytes and the result is published as
// a new version through the ordinary create path.

import type { ArtifactInfo, Me } from "./types";
import { ArtiError, errorFrom, type CreateArtifactInput } from "./arti";
import { bytesToBase64 } from "./upload";

export interface HtmlTextEdit {
  eid: number;
  before: string;
  html: string;
}

export const HTML_EDIT_FRAME_SOURCE = "arti-html-edit";
export const HTML_EDIT_HOST_SOURCE = "arti-html-edit-host";

const MAX_EDITS = 5000;
const MAX_EDIT_CHARS = 200_000;

export function isHtmlTextEditable(
  info: Pick<ArtifactInfo, "artifact_type" | "content_type" | "named_slug" | "deleted_at">,
  me: Pick<Me, "html_edit_enabled"> | null | undefined,
): boolean {
  return (
    !!me?.html_edit_enabled &&
    !info.deleted_at &&
    info.artifact_type === "TEXT" &&
    info.content_type.toLowerCase().startsWith("text/html") &&
    !!info.named_slug
  );
}

export function htmlEditFrameSrc(artifactID: string): string {
  return `/api/artifacts/${artifactID}?edit=1`;
}

// parseFrameEdits validates the edits list a frame posted. The frame runs the
// document's own scripts, so its message is untrusted input: anything off-shape
// yields null rather than a partial list.
export function parseFrameEdits(raw: unknown): HtmlTextEdit[] | null {
  if (!Array.isArray(raw) || raw.length > MAX_EDITS) return null;
  const out: HtmlTextEdit[] = [];
  for (const e of raw) {
    if (!e || typeof e !== "object") return null;
    const { eid, before, html } = e as Record<string, unknown>;
    if (typeof eid !== "number" || !Number.isInteger(eid) || eid < 0) return null;
    if (typeof before !== "string" || typeof html !== "string") return null;
    if (before.length > MAX_EDIT_CHARS || html.length > MAX_EDIT_CHARS) return null;
    out.push({ eid, before, html });
  }
  return out;
}

export interface AppliedHtmlEdits {
  bytes: ArrayBuffer;
  rejected: number[];
}

// applyHtmlTextEdits asks the server for the stored page with the edits
// applied. Nothing is written yet.
export async function applyHtmlTextEdits(artifactID: string, edits: HtmlTextEdit[]): Promise<AppliedHtmlEdits> {
  const resp = await fetch(`/api/artifacts/${artifactID}/html-text-edits`, {
    method: "POST",
    credentials: "include",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ edits }),
  });
  if (!resp.ok) throw await errorFrom(resp);
  return { bytes: await resp.arrayBuffer(), rejected: parseRejected(resp.headers.get("X-Arti-Rejected-Edits")) };
}

export function parseRejected(header: string | null): number[] {
  if (!header) return [];
  return header
    .split(",")
    .map((s) => Number(s.trim()))
    .filter((n) => Number.isInteger(n) && n >= 0);
}

// htmlEditSaveInput publishes the edited page as the next version, pinned to
// the version that was edited so a concurrent publish fails with 409 instead
// of being overwritten.
export function htmlEditSaveInput(
  info: Pick<ArtifactInfo, "title" | "content_type" | "named_slug" | "version" | "description">,
  bytes: ArrayBuffer,
): CreateArtifactInput {
  return {
    title: info.title,
    content_type: info.content_type,
    artifact_type: "TEXT",
    content_base64: bytesToBase64(bytes),
    named_slug: info.named_slug ?? "",
    description: info.description ?? undefined,
    expected_latest_version: info.version ?? undefined,
  };
}

export function htmlEditErrorMessage(e: unknown): string {
  if (e instanceof ArtiError && e.code === "stale-base-version") {
    return "A newer version was published while you were editing. Open the latest version and re-apply your edits.";
  }
  return e instanceof Error ? e.message : String(e);
}
