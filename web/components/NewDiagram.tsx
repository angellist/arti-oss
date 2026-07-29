"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { createArtifact, getAggregates, latestVersionForSlug } from "@/lib/arti";
import { bytesToBase64, slugFromFilename } from "@/lib/upload";
import { DIAGRAM_CONTENT_TYPE, serializeDiagram, starterDiagram, type DiagramDoc } from "@/lib/diagram";
import ChipInput from "./ChipInput";
import DiagramEditor from "./DiagramEditor";

// The "new diagram" surface: name it, draw it, save it. Saving creates an
// ordinary TEXT artifact carrying the diagram content type, which is why a
// diagram gets slugs, versions, labels, scopes and access control without any
// diagram-specific storage.
export default function NewDiagram() {
  const router = useRouter();
  const [title, setTitle] = useState("Untitled diagram");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [labels, setLabels] = useState<string[]>([]);
  const [scopes, setScopes] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState("");
  const [scopeSug, setScopeSug] = useState<string[]>([]);
  const [labelSug, setLabelSug] = useState<string[]>([]);
  const [initialDoc] = useState<DiagramDoc>(starterDiagram);

  // Typeahead values for the chip inputs, same source as the upload modal.
  useEffect(() => {
    getAggregates()
      .then((a) => {
        setScopeSug(a.scopes.map((s) => s.scope));
        setLabelSug(a.labels.map((l) => l.label));
      })
      .catch(() => {
        /* suggestions are optional */
      });
  }, []);

  // Until the slug field is touched it tracks the title, matching the upload
  // modal's behaviour (where it tracks the filename).
  const effectiveSlug = (slugTouched ? slug : slugFromFilename(title)).trim();

  const save = async (doc: DiagramDoc) => {
    if (saving) return;
    setSaving(true);
    setErr("");
    try {
      // A slug that already exists would silently version SOMEONE ELSE's
      // diagram, so confirm before doing that rather than after.
      if (effectiveSlug) {
        const existing = await latestVersionForSlug(effectiveSlug).catch(() => null);
        if (
          existing != null &&
          !window.confirm(
            `The slug "${effectiveSlug}" already exists (latest v${existing}).\n\n` +
              `Saving adds this diagram as v${existing + 1} of that slug. Continue?`,
          )
        ) {
          setSaving(false);
          return;
        }
      }
      const info = await createArtifact({
        title: title.trim() || "Untitled diagram",
        content_type: DIAGRAM_CONTENT_TYPE,
        artifact_type: "TEXT",
        content_base64: bytesToBase64(new TextEncoder().encode(serializeDiagram(doc)).buffer as ArrayBuffer),
        named_slug: effectiveSlug || undefined,
        labels: labels.length ? labels : undefined,
        scopes: scopes.length ? scopes : undefined,
      });
      const dest = info.named_slug
        ? `/s/${info.named_slug}` + (info.version ? `/${info.version}` : "")
        : `/a/${info.artifact_id}`;
      router.push(dest);
      router.refresh();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
      setSaving(false);
    }
  };

  return (
    <div className="mx-auto max-w-[1400px] px-6 py-6">
      <h1 className="text-[15px] font-medium text-neutral-800">New diagram</h1>
      <p className="mt-0.5 text-[12px] text-neutral-500">
        Drag shapes onto the canvas, pull a connector between them, and save it as a versioned artifact.
      </p>

      <div className="mt-4 grid gap-3 sm:grid-cols-2">
        <label className="block">
          <span className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">title</span>
          <input
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            className="mt-1 w-full rounded-md border border-neutral-200 px-2.5 py-1.5 text-[13px] text-neutral-800 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
          />
        </label>
        <label className="block">
          <span className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">
            slug <span className="font-normal normal-case text-neutral-400">(optional — enables versioning)</span>
          </span>
          <input
            value={slugTouched ? slug : slugFromFilename(title)}
            onChange={(e) => {
              setSlugTouched(true);
              setSlug(e.target.value);
            }}
            className="mt-1 w-full rounded-md border border-neutral-200 px-2.5 py-1.5 font-mono text-[13px] text-neutral-800 focus:border-blue-400 focus:outline-none focus:ring-1 focus:ring-blue-200"
          />
        </label>
        <div>
          <span className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">labels</span>
          <div className="mt-1">
            <ChipInput
              values={labels}
              onChange={setLabels}
              suggestions={labelSug}
              placeholder="add a label…"
              noun="label"
              chipClass="bg-neutral-100 ring-neutral-200 text-neutral-700"
            />
          </div>
        </div>
        <div>
          <span className="text-[10px] font-medium uppercase tracking-wide text-neutral-500">scopes</span>
          <div className="mt-1">
            <ChipInput
              values={scopes}
              onChange={setScopes}
              suggestions={scopeSug}
              placeholder="add a scope…"
              noun="scope"
              chipClass="bg-purple-50 ring-purple-200 text-purple-800"
            />
          </div>
        </div>
      </div>

      <div className="mt-4">
        <DiagramEditor
          initialDoc={initialDoc}
          onSave={save}
          onCancel={() => router.push("/")}
          saving={saving}
          error={err}
          saveLabel="Create diagram"
          fileName={effectiveSlug || "diagram"}
          banner={
            <>
              Saving creates a new artifact
              {effectiveSlug ? (
                <>
                  {" "}
                  at <span className="font-mono">s/{effectiveSlug}</span>
                </>
              ) : (
                " (no slug — it won't be versionable)"
              )}
              .
            </>
          }
        />
      </div>
    </div>
  );
}
