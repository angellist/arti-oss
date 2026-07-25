"use client";

import { useMemo, useState } from "react";
import { parseDiagram, toSvg, type DiagramDoc } from "@/lib/diagram";
import DiagramFigure from "./DiagramSvg";

// Read-only render of a diagram artifact body. Everything a reader can do here
// is non-destructive: look, and take a copy (SVG or the source JSON). Editing
// lives behind the viewer's Edit action, which versions the artifact.
export default function DiagramView({
  body,
  title,
  fileName = "diagram",
  className,
}: {
  body: string;
  title?: string;
  fileName?: string;
  className?: string;
}) {
  const [showSource, setShowSource] = useState(false);
  // A malformed body must not blank the page: fall back to showing the raw
  // text, which is also the only way to repair it by hand.
  const parsed = useMemo<{ doc: DiagramDoc | null; err: string }>(() => {
    try {
      return { doc: parseDiagram(body), err: "" };
    } catch (e) {
      return { doc: null, err: e instanceof Error ? e.message : String(e) };
    }
  }, [body]);

  if (!parsed.doc) {
    return (
      <div className="rounded-md border border-amber-200 bg-amber-50 p-4">
        <div className="text-[12px] font-medium text-amber-800">This diagram couldn&apos;t be parsed</div>
        <div className="mt-1 text-[11px] text-amber-700">{parsed.err}</div>
        <pre className="mt-3 max-h-[50vh] overflow-auto whitespace-pre-wrap break-words rounded border border-amber-200 bg-white p-3 font-mono text-[11px] text-neutral-700">
          {body}
        </pre>
      </div>
    );
  }

  const doc = parsed.doc;
  const download = () => {
    const blob = new Blob([toSvg(doc)], { type: "image/svg+xml" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${fileName}.svg`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className={className}>
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[11px] text-neutral-500">
          {doc.nodes.length} {doc.nodes.length === 1 ? "shape" : "shapes"} · {doc.edges.length}{" "}
          {doc.edges.length === 1 ? "connector" : "connectors"}
        </span>
        <span className="ml-auto flex items-center gap-2">
          <button
            type="button"
            onClick={download}
            className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 transition hover:bg-neutral-50"
          >
            Download SVG
          </button>
          <button
            type="button"
            onClick={() => setShowSource((s) => !s)}
            className="rounded-md border border-neutral-200 bg-white px-2.5 py-1 text-[11px] text-neutral-700 transition hover:bg-neutral-50"
          >
            {showSource ? "Hide source" : "Show source"}
          </button>
        </span>
      </div>
      {showSource ? (
        <pre className="max-h-[70vh] overflow-auto whitespace-pre-wrap break-words rounded-md border border-neutral-200 bg-neutral-50 p-4 font-mono text-[11px] leading-relaxed text-neutral-800">
          {body}
        </pre>
      ) : (
        <div className="rounded-md border border-neutral-200 bg-white p-2">
          <DiagramFigure doc={doc} title={title} className="h-[min(70vh,720px)] w-full" />
        </div>
      )}
    </div>
  );
}
