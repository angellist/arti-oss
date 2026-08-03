"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import type { ArtifactInfo } from "@/lib/types";
import { createArtifact, latestVersionForSlug } from "@/lib/arti";
import { saveAsNewVersionInput, saveConfirmMessage } from "@/lib/edit";
import { emptyDiagram, parseDiagram, serializeDiagram, type DiagramDoc } from "@/lib/diagram";
import DiagramEditor from "./DiagramEditor";

// The viewer's Edit action for a diagram artifact — the canvas equivalent of
// ArtifactEditor. Saving has exactly the same semantics as editing text: it
// POSTs a NEW version of the slug (server assigns MAX(version)+1 and inherits
// labels/scopes/access), and the version being viewed is never mutated. The
// canvas itself knows nothing about artifacts; this file is the whole seam.
export default function DiagramArtifactEditor({
  info,
  body,
  onClose,
}: {
  info: ArtifactInfo;
  body: string;
  onClose: () => void;
}) {
  const router = useRouter();
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  const slug = info.named_slug ?? "";

  // A body that won't parse still opens — on an empty canvas — rather than
  // trapping the artifact in an unrecoverable state. The warning below says so
  // plainly, because saving from here WILL replace the unreadable content.
  const initial = useMemo<{ doc: DiagramDoc; broken: boolean }>(() => {
    try {
      return { doc: parseDiagram(body), broken: false };
    } catch {
      return { doc: emptyDiagram(), broken: true };
    }
  }, [body]);

  // Advisory next-version probe, same best-effort semantics as the text
  // editor's: the server assigns the real number on save.
  const [latest, setLatest] = useState<number | null>(null);
  useEffect(() => {
    let cancelled = false;
    latestVersionForSlug(slug)
      .then((v) => {
        if (!cancelled) setLatest(v);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [slug]);

  const nextV = latest != null ? latest + 1 : null;
  const editingStale = info.version != null && latest != null && latest !== info.version;

  const save = async (doc: DiagramDoc) => {
    if (saving) return;
    if (!window.confirm(saveConfirmMessage(slug, info.version, latest))) return;
    setSaving(true);
    setErr("");
    try {
      const created = await createArtifact(saveAsNewVersionInput(info, serializeDiagram(doc)));
      onClose();
      router.push(created.version != null ? `/s/${slug}/${created.version}` : `/a/${created.artifact_id}`);
      router.refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setSaving(false);
    }
  };

  return (
    <DiagramEditor
      initialDoc={initial.doc}
      onSave={save}
      onCancel={onClose}
      saving={saving}
      error={err}
      saveLabel={nextV != null ? `Save as v${nextV}` : "Save new version"}
      disableSaveWhenClean
      fileName={slug || info.title || "diagram"}
      banner={
        <>
          Editing <span className="font-mono">s/{slug}</span>
          <span className="text-neutral-500">
            {" — "}saving creates {nextV != null ? `v${nextV}` : "a new version"};{" "}
            {info.version != null ? `v${info.version}` : "the current version"} stays unchanged.
          </span>
          {editingStale ? (
            <span className="ml-2 font-medium text-amber-700">
              You are editing v{info.version} — latest is v{latest}.
            </span>
          ) : null}
          {initial.broken ? (
            <span className="ml-2 font-medium text-amber-700">
              The stored body couldn&apos;t be parsed — this canvas started empty, and saving replaces it.
            </span>
          ) : null}
        </>
      }
    />
  );
}
