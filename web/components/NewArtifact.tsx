"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { ArtiError, createArtifact, latestVersionForSlug } from "@/lib/arti";
import { serializeDiagram, starterDiagram, type DiagramDoc } from "@/lib/diagram";
import {
  conflictSuggestion,
  createInputFor,
  defaultSlug,
  normalizeSlug,
  resolveSlug,
  type NewKind,
} from "@/lib/newartifact";
import DiagramEditor from "./DiagramEditor";
import DraftHeader from "./DraftHeader";
import MarkdownEditor from "./MarkdownEditor";
import SlugConflictModal from "./SlugConflictModal";
import { WIDTH_CLASS, useViewerPrefs, type Width } from "./ViewerToolbar";

// The "New artifact" page, shared by /new/text and /new/diagram. One shell, one
// draft, one commit.
//
// Everything on the page is local state until Create fires a single POST. That
// is why the header renders plain input boxes instead of the viewer's
// click-to-edit controls, and why the editors below are mounted WITHOUT their
// own save bars: a page with two commit buttons has to explain which one is
// real, and this one doesn't need to.

interface Conflict {
  slug: string;
  version: number | null;
  creator: string | null;
  // The slug exists but this caller can't read/write it, so we know nothing
  // about it beyond "taken" — no version to quote.
  unreadable?: boolean;
  // Which check raised this. Only the advisory blur check may retract its own
  // verdict; a conflict the server stated by rejecting a create must survive a
  // later "looks free" answer, because the advisory check is access-filtered
  // and reports free for exactly the slug a 404 means we cannot write.
  source: "check" | "create";
}

export default function NewArtifact({ kind }: { kind: NewKind }) {
  const router = useRouter();

  // The timestamp in the default slug is stamped ONCE, at mount, and shown in
  // the slug box from then on. Recomputing it at submit time would store a
  // different value than the one on screen — the exact confirmation gap this
  // page is built to avoid.
  const [stampedDefault] = useState(() => defaultSlug(kind, new Date()));

  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [labels, setLabels] = useState<string[]>([]);
  const [scopes, setScopes] = useState<string[]>([]);
  // Seeded from the server's own default for a new artifact (everyone
  // authenticated, write mirroring read) so the button reports the truth before
  // anyone touches it. Left untouched, these are omitted from the POST entirely
  // and the server applies that default itself.
  const [access, setAccess] = useState<string[]>(["*"]);
  const [write, setWrite] = useState<string[] | null>(null);
  const [accessTouched, setAccessTouched] = useState(false);

  const [markdown, setMarkdown] = useState("");
  // starterDiagram is a factory. Instantiate exactly once: DiagramEditor derives
  // its dirty baseline from initialDoc, so a fresh object each render would
  // recompute that baseline and defeat the comparison.
  const [starter] = useState<DiagramDoc>(starterDiagram);
  // Pulled from the canvas at Create time rather than mirrored into state: the
  // doc changes on every pointermove, and re-rendering this page (and
  // re-serializing the diagram) per frame would make dragging feel heavy.
  const getDiagramRef = useRef<(() => DiagramDoc) | null>(null);
  const [diagramDirty, setDiagramDirty] = useState(false);

  const [creating, setCreating] = useState(false);
  const [err, setErr] = useState("");
  const [conflict, setConflict] = useState<Conflict | null>(null);

  // Diagrams always author at full width — a canvas squeezed into a reading
  // column is unusable. Markdown honours the persisted per-doc width, so the
  // width you read at is the width you write at — EXCEPT in Split, where two
  // panes need the whole page; the width control hides rather than fight it.
  const { width, setWidth } = useViewerPrefs();
  const [mdMode, setMdMode] = useState<"write" | "split" | "preview">("write");
  const isDiagram = kind === "diagram";
  const forcedWide = isDiagram || mdMode === "split";
  const effectiveWidth: Width = forcedWide ? "wide" : width;

  const resolvedSlug = useMemo(
    () => resolveSlug({ slug, slugTouched, title, titleTouched: title.trim() !== "", stampedDefault }),
    [slug, slugTouched, title, stampedDefault],
  );

  // An untouched draft has nothing worth warning about on the way out.
  const dirty =
    title.trim() !== "" ||
    slugTouched ||
    labels.length > 0 ||
    scopes.length > 0 ||
    accessTouched ||
    (isDiagram ? diagramDirty : markdown !== "");

  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  // Advisory duplicate check when the slug box loses focus, so a collision is
  // reported while the field is still in hand rather than after Create. It is
  // only advisory: latestVersionForSlug is access-filtered (a slug you can't see
  // reads as absent) and any check-then-create can be raced, which is why the
  // POST also carries ensure_new.
  const checkedRef = useRef<string>("");
  // Monotonic id for the in-flight check. Two quick blurs against different
  // slugs race, and without this a slower response for the ABANDONED slug can
  // land last and raise a conflict dialog naming a slug the user has already
  // moved past.
  const checkSeq = useRef(0);
  const checkSlug = async () => {
    const normalized = normalizeSlug(slug);
    if (slugTouched && normalized !== slug) setSlug(normalized);
    const target = slugTouched ? normalized : resolvedSlug;
    if (!target || target === checkedRef.current) return;
    checkedRef.current = target;
    const seq = ++checkSeq.current;
    const existing = await latestVersionForSlug(target).catch(() => null);
    // A newer check started while this one was in flight — its answer is the
    // only one that describes the current slug.
    if (seq !== checkSeq.current) return;
    if (existing != null) {
      setConflict({ slug: target, version: existing, creator: null, source: "check" });
      return;
    }
    // The slug is free now, so retract a stale warning — otherwise tabbing
    // back, fixing the slug and blurring again leaves a dialog that
    // contradicts the field. Exactly one verdict outranks this one: the server
    // rejecting a create for THIS same slug, since the advisory check is
    // access-filtered and answers "free" for a slug it cannot read. A
    // server-raised conflict about a slug the user has since moved off is just
    // as stale as our own.
    setConflict((prev) => {
      if (!prev) return prev;
      if (prev.source === "create" && prev.slug === target) return prev;
      return null;
    });
  };

  const useSuggestedSlug = (next: string) => {
    setSlug(next);
    setSlugTouched(true);
    checkedRef.current = next;
    setConflict(null);
  };

  const create = async () => {
    if (creating) return;
    setCreating(true);
    setErr("");
    try {
      // Read the canvas here, at the single commit point, so an in-flight shape
      // label is folded in (getDocRef runs the editor's finishEditing).
      const body = isDiagram
        ? serializeDiagram(getDiagramRef.current?.() ?? starter)
        : markdown;
      const info = await createArtifact(
        createInputFor({
          kind,
          title,
          slug: resolvedSlug,
          labels,
          scopes,
          body,
          access,
          write,
          accessTouched,
        }),
      );
      const dest = info.named_slug
        ? `/s/${info.named_slug}` + (info.version != null ? `/${info.version}` : "")
        : `/a/${info.artifact_id}`;
      router.push(dest);
      router.refresh();
    } catch (e) {
      // Two server responses both mean "this slug is not yours to create":
      //
      //   409 — ensure_new hit an existing, visible version of the slug.
      //   404 — the slug exists but the caller can't write it, so
      //         checkWriteAccess returns ErrNotFound BEFORE the ensure_new
      //         conflict is ever raised. This is also the case the advisory blur
      //         check can't see, since latestVersionForSlug is access-filtered.
      //
      // Both route to the same dialog; a raw "not found" here would be baffling.
      // 403 deliberately does NOT: that's an ACL-authority failure about the
      // access being set, not a statement about the slug.
      const status = e instanceof ArtiError ? e.status : 0;
      if (resolvedSlug && (status === 409 || status === 404)) {
        setConflict({
          slug: resolvedSlug,
          version: null,
          creator: null,
          unreadable: status === 404,
          source: "create",
        });
      } else {
        setErr(e instanceof Error ? e.message : String(e));
      }
      setCreating(false);
    }
  };

  const leave = () => {
    if (dirty && !window.confirm("Discard this draft?")) return;
    router.push("/");
  };

  return (
    <div>
      <DraftHeader
        kind={kind}
        title={title}
        onTitle={setTitle}
        slug={slug}
        onSlug={(v) => {
          setSlugTouched(true);
          setSlug(v);
        }}
        onSlugBlur={() => void checkSlug()}
        resolvedSlug={resolvedSlug}
        labels={labels}
        onLabels={setLabels}
        scopes={scopes}
        onScopes={setScopes}
        access={access}
        write={write}
        onAccess={(nextAccess, nextWrite) => {
          setAccess(nextAccess);
          setWrite(nextWrite);
          setAccessTouched(true);
        }}
        width={width}
        setWidth={setWidth}
        showWidth={!forcedWide}
        creating={creating}
        error={err}
        onCreate={() => void create()}
        onCancel={leave}
      />

      <div className={`mx-auto ${WIDTH_CLASS[effectiveWidth]} px-6 py-5`}>
        {isDiagram ? (
          // No onSave/onCancel: the canvas keeps its own tools (shapes, undo,
          // zoom) but the page owns the commit.
          <DiagramEditor
            initialDoc={starter}
            getDocRef={getDiagramRef}
            onDirtyChange={setDiagramDirty}
            saving={creating}
            fileName={resolvedSlug || "diagram"}
            banner="Drag shapes onto the canvas, pull a connector between them, then press Create above."
          />
        ) : (
          <MarkdownEditor
            value={markdown}
            onChange={setMarkdown}
            autoFocus
            disabled={creating}
            onModeChange={setMdMode}
          />
        )}
      </div>

      {conflict ? (
        <SlugConflictModal
          slug={conflict.slug}
          suggestion={conflictSuggestion(conflict.slug, new Date())}
          existingVersion={conflict.version}
          existingCreator={conflict.creator}
          unreadable={conflict.unreadable}
          onUse={useSuggestedSlug}
          onCancel={() => {
            // Clear the "already checked this one" memo so blurring the same
            // still-taken slug warns again. Dismissing the dialog is not the
            // same as fixing the slug, and Create would reject it anyway.
            checkedRef.current = "";
            setConflict(null);
          }}
        />
      ) : null}
    </div>
  );
}
