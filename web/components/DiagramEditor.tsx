"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  COLORS,
  COLOR_KEYS,
  DEFAULT_H,
  DEFAULT_W,
  FONT_FAMILY,
  FONT_SIZE,
  GRID,
  SNAP_TOLERANCE,
  addNode,
  autoHeight,
  autoLayout,
  bringToFront,
  connect as connectNodes,
  deleteEdges,
  deleteNodes,
  docBounds,
  duplicateNodes,
  findFreeSpot,
  hitTestNode,
  makeNode,
  moveNodes,
  nodeByID,
  nodeRect,
  nodesInRect,
  normalizeRect,
  quickAdd,
  resizeNode,
  sendToBack,
  serializeDiagram,
  snap,
  snapToNeighbours,
  toSvg,
  updateEdge,
  updateNode,
  updateNodes,
  type ColorKey,
  type DiagramDoc,
  type DiagramNode,
  type Guide,
  type LayoutDirection,
  type Point,
  type Rect,
  type ShapeKind,
  type Side,
} from "@/lib/diagram";
import { EdgeShape, NodeShape, SELECT_COLOR } from "./DiagramSvg";

// The drag-and-drop diagram canvas.
//
// The editor is deliberately uncontrolled: it owns the working document, the
// undo stack and the viewport, and hands the finished document back through
// `onSave`. That keeps every caller — the "new diagram" page and the artifact
// viewer's edit mode — free to decide what saving MEANS (create vs. version)
// without re-implementing any canvas behaviour.
//
// Interaction model, in one place because it's the thing that's easy to break:
//   • pointerdown on a node        → select (shift = add) and start moving
//   • pointerdown on a handle      → resize, opposite edge pinned
//   • CLICK a port dot             → quick-add a connected shape on that side,
//                                    positioned and aligned automatically
//   • DRAG from a port dot         → draw a connector; drop on a node to link,
//                                    on empty canvas to spawn a linked box
//   • pointerdown on empty canvas  → marquee select (space/middle-drag = pan)
//   • double-click                 → edit the label in an overlay textarea
// A move drag aligns to nearby shapes and shows guides; ⌘ restricts it to the
// grid and the backtick key suspends snapping entirely.
// Every mutation goes through `commit`, which is what makes undo a plain stack
// of whole-document snapshots.

const MIME = "application/x-arti-shape";
const MIN_ZOOM = 0.2;
const MAX_ZOOM = 3;
// Screen-pixel movement below which a port gesture counts as a click
// (quick-add) rather than a drag (draw a connector).
const CLICK_SLOP = 4;

// sameGuides avoids a setState per pointermove while a guide is simply
// persisting — without it every mouse move during an aligned drag re-renders
// the whole canvas with an identical guide array.
function sameGuides(a: Guide[], b: Guide[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((g, i) => g.axis === b[i].axis && g.at === b[i].at && g.from === b[i].from && g.to === b[i].to);
}

type Handle = "nw" | "n" | "ne" | "e" | "se" | "s" | "sw" | "w";

const HANDLES: Handle[] = ["nw", "n", "ne", "e", "se", "s", "sw", "w"];

type Drag =
  // `base` is the document as it stood when the drag began. Recomputing from
  // it on every move (rather than accumulating deltas) is what keeps snapping
  // exact and makes a cancelled drag a no-op.
  | { kind: "move"; ids: string[]; start: Point; base: DiagramDoc }
  | { kind: "resize"; id: string; handle: Handle; start: Point; base: DiagramDoc }
  | { kind: "marquee"; start: Point; cur: Point; additive: boolean }
  // `origin` is where the gesture started, so pointerup can tell a click
  // (quick-add) from a drag (draw a connector). `side` is the port it left.
  | { kind: "connect"; from: string; side: Side; origin: Point; cur: Point; over: string | null }
  | { kind: "pan"; startClient: Point; origin: { x: number; y: number } };

interface View {
  x: number;
  y: number;
  k: number;
}

const SHAPE_TOOLS: Array<{ kind: ShapeKind; label: string; hint: string }> = [
  { kind: "rounded", label: "Rounded", hint: "Start / end" },
  { kind: "rect", label: "Box", hint: "Step" },
  { kind: "diamond", label: "Diamond", hint: "Decision" },
  { kind: "ellipse", label: "Ellipse", hint: "State" },
  { kind: "note", label: "Note", hint: "Comment" },
  { kind: "text", label: "Text", hint: "Label" },
];

export default function DiagramEditor({
  initialDoc,
  onSave,
  onCancel,
  getDocRef,
  onDirtyChange,
  saveLabel = "Save",
  banner,
  saving = false,
  error = "",
  fileName = "diagram",
  disableSaveWhenClean = false,
}: {
  initialDoc: DiagramDoc;
  // Omit when the HOST owns the commit (the "New artifact" page): the action
  // bar then drops its Save button, leaving exactly one commit on the page.
  onSave?: (doc: DiagramDoc) => void | Promise<void>;
  onCancel?: () => void;
  // Lets a host pull the current document at ITS commit time. A pull, not a
  // push: `doc` changes on every pointermove of a drag, so lifting it on each
  // change would re-render the host ~60×/sec for the whole gesture.
  getDocRef?: React.MutableRefObject<(() => DiagramDoc) | null>;
  // The one piece of canvas state a host does need reactively (to guard against
  // navigating away from unsaved work). A boolean, so it settles immediately.
  onDirtyChange?: (dirty: boolean) => void;
  saveLabel?: string;
  banner?: React.ReactNode;
  saving?: boolean;
  error?: string;
  // Base name for the "Download SVG" file.
  fileName?: string;
  // Existing artifacts should not publish an identical new version. New
  // diagrams leave this enabled because their untouched starter is v1.
  disableSaveWhenClean?: boolean;
}) {
  const [doc, setDoc] = useState<DiagramDoc>(initialDoc);
  const [past, setPast] = useState<DiagramDoc[]>([]);
  const [future, setFuture] = useState<DiagramDoc[]>([]);
  const [selNodes, setSelNodes] = useState<string[]>([]);
  const [selEdge, setSelEdge] = useState<string | null>(null);
  const [drag, setDrag] = useState<Drag | null>(null);
  const [editingID, setEditingID] = useState<string | null>(null);
  const [editingText, setEditingText] = useState("");
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const [snapOn, setSnapOn] = useState(true);
  const [spaceHeld, setSpaceHeld] = useState(false);
  // Alignment guides shown during the current move drag.
  const [guides, setGuides] = useState<Guide[]>([]);
  // Quick-add dots can be hidden (Q) — on a dense diagram they're noise.
  const [quickAddOn, setQuickAddOn] = useState(true);
  // Backtick held = place freely: no grid, no alignment.
  const [freeform, setFreeform] = useState(false);

  const wrapRef = useRef<HTMLDivElement>(null);
  const svgRef = useRef<SVGSVGElement>(null);
  // Mirrors of state read from event handlers, where a stale closure would
  // silently act on an older document. Synced in an effect (never during
  // render): effects flush before any user event can fire, so a handler
  // always sees the committed values.
  const docRef = useRef(doc);
  const pastRef = useRef(past);
  const futureRef = useRef(future);
  const editingIDRef = useRef<string | null>(null);
  const editingTextRef = useRef("");
  // Read inside the pointermove handler, which is subscribed per-drag and so
  // would otherwise capture whatever `freeform` was when the drag began.
  const freeformRef = useRef(false);
  useEffect(() => {
    docRef.current = doc;
    pastRef.current = past;
    futureRef.current = future;
    editingIDRef.current = editingID;
    editingTextRef.current = editingText;
    freeformRef.current = freeform;
  }, [doc, past, future, editingID, editingText, freeform]);
  // True while a move drag has actually shifted something — a click that
  // merely selects must not push an undo step.
  const movedRef = useRef(false);

  const baseline = useMemo(() => serializeDiagram(initialDoc), [initialDoc]);
  const current = useMemo(() => serializeDiagram(doc), [doc]);
  const dirty = current !== baseline;

  // ---- history -----------------------------------------------------------

  // commit records the CURRENT doc on the undo stack and installs `next`.
  // Continuous gestures call it once, at the point the gesture starts, then
  // mutate transiently — otherwise a single drag would leave 60 undo steps.
  const commit = useCallback((next: DiagramDoc) => {
    setPast((p) => [...p.slice(-99), docRef.current]);
    setFuture([]);
    setDoc(next);
  }, []);

  const undo = useCallback(() => {
    const p = pastRef.current;
    if (p.length === 0) return;
    setPast(p.slice(0, -1));
    setFuture((f) => [docRef.current, ...f]);
    setDoc(p[p.length - 1]);
  }, []);

  const redo = useCallback(() => {
    const f = futureRef.current;
    if (f.length === 0) return;
    setFuture(f.slice(1));
    setPast((p) => [...p, docRef.current]);
    setDoc(f[0]);
  }, []);

  // ---- coordinates -------------------------------------------------------

  // toDiagram converts a client (screen) point into document space. Every
  // pointer handler goes through it, so pan/zoom stay invisible to the
  // interaction logic.
  const toDiagram = useCallback(
    (clientX: number, clientY: number): Point => {
      const r = svgRef.current?.getBoundingClientRect();
      if (!r) return { x: 0, y: 0 };
      return { x: (clientX - r.left - view.x) / view.k, y: (clientY - r.top - view.y) / view.k };
    },
    [view],
  );

  // frame centres an arbitrary document-space box in the viewport, scaling up to
  // `maxK` at most.
  const frame = useCallback((b: Rect, maxK: number) => {
    const el = wrapRef.current;
    if (!el) return;
    const w = el.clientWidth;
    const h = el.clientHeight;
    if (w === 0 || h === 0 || b.w === 0 || b.h === 0) return;
    const k = Math.max(MIN_ZOOM, Math.min(w / b.w, h / b.h, maxK));
    setView({ k, x: w / 2 - (b.x + b.w / 2) * k, y: h / 2 - (b.y + b.h / 2) * k });
  }, []);

  // Fit never zooms PAST 1:1 — a two-box diagram blown up to fill a 4K screen
  // looks broken; leaving it at 100% reads as intentional.
  const fit = useCallback(() => {
    frame(docBounds(docRef.current, 48), 1);
  }, [frame]);

  // fitTo frames just the given shapes — "zoom to selection". This one DOES
  // magnify past 1:1: zooming to one small box in a large diagram is a request
  // to look closely at it, so capping at 100% would make the action do nothing.
  const fitTo = useCallback(
    (ids: string[]) => {
      const picked = docRef.current.nodes.filter((n) => ids.includes(n.id));
      if (picked.length === 0) return;
      frame(docBounds({ version: 1, nodes: picked, edges: [] }, 48), MAX_ZOOM);
    },
    [frame],
  );

  // Fit once, after the container has a measured size.
  useLayoutEffect(() => {
    fit();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const zoomBy = useCallback((factor: number, centerClient?: Point) => {
    const el = wrapRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const cx = centerClient ? centerClient.x - r.left : r.width / 2;
    const cy = centerClient ? centerClient.y - r.top : r.height / 2;
    setView((v) => {
      const k = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, v.k * factor));
      // Keep the point under the cursor fixed while the scale changes.
      return { k, x: cx - ((cx - v.x) * k) / v.k, y: cy - ((cy - v.y) * k) / v.k };
    });
  }, []);

  // ---- selection helpers -------------------------------------------------

  const selectOnly = (ids: string[]) => {
    setSelNodes(ids);
    setSelEdge(null);
  };

  const selectedNodes = doc.nodes.filter((n) => selNodes.includes(n.id));
  const selectedEdge = selEdge ? doc.edges.find((e) => e.id === selEdge) : undefined;

  // ---- text editing ------------------------------------------------------

  // Takes the NODE, not an id: a node created in this same event handler
  // isn't in docRef yet (the mirror syncs in an effect), so a lookup here
  // would silently skip editing the box that was just dropped.
  const startEditing = (n: DiagramNode) => {
    setEditingID(n.id);
    setEditingText(n.text);
  };

  // finishEditing closes the label overlay and RETURNS the resulting document.
  // Returning it (rather than only setting state) is what lets Save use the
  // just-typed label: a setState scheduled here wouldn't have flushed by the
  // time the save request is built.
  const finishEditing = useCallback(
    (keep: boolean): DiagramDoc => {
      const id = editingIDRef.current;
      const base = docRef.current;
      if (!id) return base;
      setEditingID(null);
      if (!keep) return base;
      const n = nodeByID(base, id);
      if (!n || n.text === editingTextRef.current) return base;
      // Grow the box if the new label needs more room — a shape that clips its
      // own words looks like a rendering bug.
      const withText = updateNode(base, id, { text: editingTextRef.current });
      const next = updateNode(withText, id, { h: autoHeight(nodeByID(withText, id)!) });
      commit(next);
      return next;
    },
    [commit],
  );

  // ---- node / edge mutations --------------------------------------------

  // `at` is the CENTRE the new shape should sit on. avoidOverlap is for
  // click-to-add, where that centre is just the middle of the viewport and
  // repeated clicks would otherwise pile shapes on one spot.
  const addShape = (kind: ShapeKind, at: Point, { autoEdit = true, avoidOverlap = false } = {}) => {
    const probe = makeNode(kind, 0, 0);
    const raw = { x: at.x - probe.w / 2, y: at.y - probe.h / 2 };
    const snapped = snapOn ? { x: snap(raw.x), y: snap(raw.y) } : raw;
    const pos = avoidOverlap
      ? findFreeSpot(docRef.current, snapped.x, snapped.y, probe.w, probe.h)
      : snapped;
    const n = { ...probe, x: pos.x, y: pos.y };
    commit(addNode(docRef.current, n));
    selectOnly([n.id]);
    if (autoEdit) startEditing(n);
    return n;
  };

  const deleteSelection = useCallback(() => {
    if (selEdge) {
      commit(deleteEdges(docRef.current, [selEdge]));
      setSelEdge(null);
      return;
    }
    if (selNodes.length === 0) return;
    commit(deleteNodes(docRef.current, selNodes));
    setSelNodes([]);
  }, [selEdge, selNodes, commit]);

  const duplicateSelection = useCallback(() => {
    if (selNodes.length === 0) return;
    const { doc: next, ids } = duplicateNodes(docRef.current, selNodes);
    commit(next);
    selectOnly(ids);
  }, [selNodes, commit]);

  const setColor = (color: ColorKey) => {
    if (selNodes.length === 0) return;
    commit(updateNodes(docRef.current, selNodes, { color }));
  };

  const setShape = (shape: ShapeKind) => {
    if (selNodes.length === 0) return;
    commit(updateNodes(docRef.current, selNodes, { shape }));
  };

  // Tidy: re-layer the connected shapes into an evenly spaced flow. Applies to
  // the selection when there is one, otherwise to the whole diagram — the same
  // button reads as "clean up this branch" or "clean up everything" depending
  // on what you've got selected.
  const tidy = (direction: LayoutDirection) => {
    const scope = selNodes.length > 1 ? selNodes : null;
    commit(autoLayout(docRef.current, scope, direction));
  };

  // ---- pointer interactions ---------------------------------------------

  const onNodePointerDown = (e: React.PointerEvent, n: DiagramNode) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    if (editingID && editingID !== n.id) finishEditing(true);
    let ids: string[];
    if (e.shiftKey) {
      ids = selNodes.includes(n.id) ? selNodes.filter((x) => x !== n.id) : [...selNodes, n.id];
      setSelNodes(ids);
      setSelEdge(null);
      // A shift-click that DESELECTS shouldn't then drag the node away.
      if (!ids.includes(n.id)) return;
    } else {
      ids = selNodes.includes(n.id) ? selNodes : [n.id];
      selectOnly(ids);
    }
    movedRef.current = false;
    setDrag({ kind: "move", ids, start: toDiagram(e.clientX, e.clientY), base: docRef.current });
  };

  const onHandlePointerDown = (e: React.PointerEvent, n: DiagramNode, handle: Handle) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    setDrag({ kind: "resize", id: n.id, handle, start: toDiagram(e.clientX, e.clientY), base: docRef.current });
  };

  // A port is dual-purpose, which is what makes building a flow fast: CLICK it
  // to quick-add a connected shape in that direction (placed and aligned for
  // you), or DRAG from it to wire up an existing shape. Which one you meant is
  // decided on pointerup by whether the pointer actually travelled.
  const onPortPointerDown = (e: React.PointerEvent, n: DiagramNode, side: Side) => {
    if (e.button !== 0) return;
    e.stopPropagation();
    setDrag({
      kind: "connect",
      from: n.id,
      side,
      origin: toDiagram(e.clientX, e.clientY),
      cur: toDiagram(e.clientX, e.clientY),
      over: null,
    });
  };

  // quickAddFrom is the shared path for a port click and for ⌥+arrow.
  const quickAddFrom = useCallback(
    (sourceID: string, dir: Side) => {
      const source = nodeByID(docRef.current, sourceID);
      if (!source) return;
      const { doc: next, node } = quickAdd(docRef.current, source, dir);
      commit(next);
      selectOnly([node.id]);
      startEditing(node);
    },
    // startEditing/selectOnly are stable setState wrappers; commit is memoised.
    [commit],
  );

  // Hover is computed from the pointer position against each node's rect GROWN
  // by the port ring, not from per-node enter/leave: the ports sit outside the
  // shape, so leaving the box on the way to a port would otherwise hide the
  // very dot the user is reaching for.
  const onCanvasPointerMove = (e: React.PointerEvent) => {
    if (drag) return;
    const p = toDiagram(e.clientX, e.clientY);
    const pad = (PORT_OFFSET + 8) / view.k;
    let found: string | null = null;
    for (let i = doc.nodes.length - 1; i >= 0; i--) {
      const n = doc.nodes[i];
      if (p.x >= n.x - pad && p.x <= n.x + n.w + pad && p.y >= n.y - pad && p.y <= n.y + n.h + pad) {
        found = n.id;
        break;
      }
    }
    setHoverNode((h) => (h === found ? h : found));
  };

  const onCanvasPointerDown = (e: React.PointerEvent) => {
    if (editingID) finishEditing(true);
    // Middle button or held space pans — the two conventions people reach for.
    if (e.button === 1 || spaceHeld) {
      setDrag({ kind: "pan", startClient: { x: e.clientX, y: e.clientY }, origin: { x: view.x, y: view.y } });
      return;
    }
    if (e.button !== 0) return;
    const p = toDiagram(e.clientX, e.clientY);
    if (!e.shiftKey) selectOnly([]);
    setDrag({ kind: "marquee", start: p, cur: p, additive: e.shiftKey });
  };

  // The move/up listeners live on the window so a fast drag that leaves the
  // canvas (or the browser chrome) still tracks and still finishes cleanly.
  useEffect(() => {
    if (!drag) return;
    const onMove = (e: PointerEvent) => {
      const p = toDiagram(e.clientX, e.clientY);
      if (drag.kind === "move") {
        const primary = nodeByID(drag.base, drag.ids[0]);
        if (!primary) return;
        let dx = p.x - drag.start.x;
        let dy = p.y - drag.start.y;
        // Snapping has three levels, matching what people expect from this kind
        // of canvas: align to nearby shapes (the default, and the one that
        // makes a diagram look tidy), grid only (⌘/Ctrl), or nothing at all
        // (backtick) for pixel-exact placement.
        const freeform = freeformRef.current;
        const gridOnly = e.metaKey || e.ctrlKey;
        let nextGuides: Guide[] = [];
        if (snapOn && !freeform) {
          // Alignment to nearby shapes takes PRECEDENCE over the grid, per axis.
          // Grid-snapping first and aligning second would let the grid push a
          // shape out of alignment range — the grid would quietly win, which is
          // backwards: the point of the guides is to land flush with a
          // neighbour, and a neighbour is rarely on a grid multiple.
          const raw: Rect = { x: primary.x + dx, y: primary.y + dy, w: primary.w, h: primary.h };
          const dragging = new Set(drag.ids);
          const others = gridOnly
            ? []
            : drag.base.nodes.filter((n) => !dragging.has(n.id)).map(nodeRect);
          const s = snapToNeighbours(raw, others, SNAP_TOLERANCE);
          dx += s.hitX ? s.dx : snap(raw.x) - raw.x;
          dy += s.hitY ? s.dy : snap(raw.y) - raw.y;
          nextGuides = s.guides;
        }
        setGuides((g) => (sameGuides(g, nextGuides) ? g : nextGuides));
        if (dx !== 0 || dy !== 0) movedRef.current = true;
        setDoc(moveNodes(drag.base, drag.ids, dx, dy));
      } else if (drag.kind === "resize") {
        const base = nodeByID(drag.base, drag.id);
        if (!base) return;
        let dx = p.x - drag.start.x;
        let dy = p.y - drag.start.y;
        if (snapOn) {
          dx = snap(dx);
          dy = snap(dy);
        }
        setDoc(updateNode(drag.base, drag.id, resizeNode(base, drag.handle, dx, dy)));
      } else if (drag.kind === "marquee") {
        setDrag({ ...drag, cur: p });
      } else if (drag.kind === "connect") {
        const over = hitTestNode(docRef.current, p);
        setDrag({ ...drag, cur: p, over: over && over.id !== drag.from ? over.id : null });
      } else if (drag.kind === "pan") {
        setView((v) => ({
          ...v,
          x: drag.origin.x + (e.clientX - drag.startClient.x),
          y: drag.origin.y + (e.clientY - drag.startClient.y),
        }));
      }
    };
    const onUp = () => {
      if (drag.kind === "move" && movedRef.current) {
        // One undo step for the whole gesture: stash the pre-drag document.
        const before = drag.base;
        setPast((p) => [...p.slice(-99), before]);
        setFuture([]);
      } else if (drag.kind === "resize") {
        setPast((p) => [...p.slice(-99), drag.base]);
        setFuture([]);
      } else if (drag.kind === "marquee") {
        const r: Rect = {
          x: drag.start.x,
          y: drag.start.y,
          w: drag.cur.x - drag.start.x,
          h: drag.cur.y - drag.start.y,
        };
        const hit = nodesInRect(docRef.current, r);
        setSelNodes((prev) => (drag.additive ? Array.from(new Set([...prev, ...hit])) : hit));
        setSelEdge(null);
      } else if (drag.kind === "connect") {
        const travelled = Math.hypot(drag.cur.x - drag.origin.x, drag.cur.y - drag.origin.y);
        if (travelled * view.k < CLICK_SLOP) {
          // Barely moved → this was a click on the port: quick-add a shape in
          // that direction, positioned and aligned automatically.
          quickAddFrom(drag.from, drag.side);
        } else if (drag.over) {
          commit(connectNodes(docRef.current, drag.from, drag.over));
        } else {
          // Dropped on empty canvas: spawn the next box there, already wired
          // up. Unlike a port click this respects where you let go, so the
          // position is yours — it only snaps to the grid.
          const n = makeNode("rect", snapOn ? snap(drag.cur.x - DEFAULT_W / 2) : drag.cur.x - DEFAULT_W / 2, snapOn ? snap(drag.cur.y - DEFAULT_H / 2) : drag.cur.y - DEFAULT_H / 2);
          commit(connectNodes(addNode(docRef.current, n), drag.from, n.id));
          selectOnly([n.id]);
          startEditing(n);
        }
      }
      setGuides([]);
      setDrag(null);
    };
    window.addEventListener("pointermove", onMove);
    window.addEventListener("pointerup", onUp);
    return () => {
      window.removeEventListener("pointermove", onMove);
      window.removeEventListener("pointerup", onUp);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [drag, snapOn, toDiagram]);

  // ---- wheel: pinch/⌘ zooms, plain scroll pans --------------------------

  useEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      // Non-passive so the page behind the canvas never scrolls out from
      // under a pan/zoom gesture.
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) {
        zoomBy(Math.exp(-e.deltaY / 300), { x: e.clientX, y: e.clientY });
      } else {
        setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }));
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomBy]);

  // ---- keyboard ----------------------------------------------------------

  useEffect(() => {
    const typing = (t: EventTarget | null) => {
      const el = t as HTMLElement | null;
      if (!el) return false;
      const tag = el.tagName;
      return tag === "INPUT" || tag === "TEXTAREA" || el.isContentEditable;
    };
    const onKey = (e: KeyboardEvent) => {
      if (document.querySelector('[aria-modal="true"]')) return;
      if (e.key === " " && !typing(e.target)) {
        setSpaceHeld(true);
        e.preventDefault();
        return;
      }
      // The label textarea handles its own keys.
      if (typing(e.target)) return;
      const mod = e.metaKey || e.ctrlKey;
      if (mod && e.key.toLowerCase() === "z") {
        e.preventDefault();
        if (e.shiftKey) redo();
        else undo();
        return;
      }
      if (mod && e.key.toLowerCase() === "y") {
        e.preventDefault();
        redo();
        return;
      }
      if (mod && e.key.toLowerCase() === "d") {
        e.preventDefault();
        duplicateSelection();
        return;
      }
      if (mod && e.key.toLowerCase() === "a") {
        e.preventDefault();
        selectOnly(docRef.current.nodes.map((n) => n.id));
        return;
      }
      if (e.key === "Delete" || e.key === "Backspace") {
        e.preventDefault();
        deleteSelection();
        return;
      }
      if (e.key === "Escape") {
        selectOnly([]);
        return;
      }
      if (e.key === "Enter" && selNodes.length === 1) {
        e.preventDefault();
        {
          const n = nodeByID(docRef.current, selNodes[0]);
          if (n) startEditing(n);
        }
        return;
      }
      // Q hides/shows the quick-add dots.
      if (e.key.toLowerCase() === "q") {
        e.preventDefault();
        setQuickAddOn((v) => !v);
        return;
      }
      // 1 frames the whole diagram, 2 frames the selection.
      if (e.key === "1") {
        e.preventDefault();
        fit();
        return;
      }
      if (e.key === "2" && selNodes.length > 0) {
        e.preventDefault();
        fitTo(selNodes);
        return;
      }
      // Backtick suspends snapping for as long as it's held.
      if (e.key === "`") {
        setFreeform(true);
        return;
      }
      const nudge: Record<string, [number, number]> = {
        ArrowLeft: [-1, 0],
        ArrowRight: [1, 0],
        ArrowUp: [0, -1],
        ArrowDown: [0, 1],
      };
      const d = nudge[e.key];
      if (!d) return;
      const dirs: Record<string, Side> = {
        ArrowLeft: "left",
        ArrowRight: "right",
        ArrowUp: "top",
        ArrowDown: "bottom",
      };
      // ⌥+arrow quick-adds a connected shape in that direction — the fastest
      // way to build a flow without touching the mouse.
      if (e.altKey && selNodes.length === 1) {
        e.preventDefault();
        quickAddFrom(selNodes[0], dirs[e.key]);
        return;
      }
      if (selNodes.length > 0) {
        e.preventDefault();
        const step = e.shiftKey ? GRID * 2 : snapOn ? GRID : 1;
        commit(moveNodes(docRef.current, selNodes, d[0] * step, d[1] * step));
      }
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.key === " ") setSpaceHeld(false);
      if (e.key === "`") setFreeform(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("keyup", onKeyUp);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("keyup", onKeyUp);
    };
  }, [undo, redo, deleteSelection, duplicateSelection, selNodes, snapOn, commit, quickAddFrom, fit, fitTo]);

  // Guard unsaved work against a tab close / hard navigation, matching the
  // text editor's behaviour.
  useEffect(() => {
    if (!dirty) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  // ---- save / cancel -----------------------------------------------------

  const cancel = () => {
    if (dirty && !window.confirm("Discard your changes to this diagram?")) return;
    onCancel?.();
  };

  const save = async () => {
    if (disableSaveWhenClean && !dirty) return;
    // Fold an in-flight label edit into the document being saved — otherwise
    // the last thing typed is the one thing that doesn't get persisted.
    const finalDoc = finishEditing(true);
    await onSave?.(finalDoc);
  };

  // Publish the doc getter for a host that owns the commit. It runs
  // finishEditing so an in-flight label edit is folded in — the same guarantee
  // the local Save button gets.
  useEffect(() => {
    if (!getDocRef) return;
    getDocRef.current = () => finishEditing(true);
    return () => {
      getDocRef.current = null;
    };
  }, [getDocRef, finishEditing]);

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const downloadSvg = () => {
    const blob = new Blob([toSvg(docRef.current)], { type: "image/svg+xml" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `${fileName}.svg`;
    a.click();
    URL.revokeObjectURL(url);
  };

  // ---- render ------------------------------------------------------------

  const editingNode = editingID ? nodeByID(doc, editingID) : undefined;
  const marquee = drag?.kind === "marquee" ? normalizeRect({ x: drag.start.x, y: drag.start.y, w: drag.cur.x - drag.start.x, h: drag.cur.y - drag.start.y }) : null;
  const connectFrom = drag?.kind === "connect" ? nodeByID(doc, drag.from) : undefined;
  // Ports show on hover and on a lone selection — always-on dots turn a busy
  // diagram into confetti.
  const portsFor = drag?.kind === "connect" ? null : hoverNode ?? (selNodes.length === 1 ? selNodes[0] : null);
  const portNode = portsFor ? nodeByID(doc, portsFor) : undefined;

  return (
    <div className="flex flex-col gap-2">
      {/* Action bar — mirrors the text editor's bar so Edit feels the same
          whichever body type you're editing. Hosted (no onSave) it isn't a
          commit context, so it drops the blue "you are editing" treatment. */}
      <div
        className={
          "flex flex-wrap items-center gap-2 rounded-md border px-3 py-2 " +
          (onSave ? "border-blue-200 bg-blue-50/60" : "border-neutral-200 bg-neutral-50")
        }
      >
        <span className="text-[12px] text-neutral-700">{banner}</span>
        <span className="ml-auto flex items-center gap-2">
          {error ? <span className="text-[11px] text-rose-600">error: {error}</span> : null}
          <button
            type="button"
            onClick={downloadSvg}
            className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50"
            title="download a standalone .svg of this diagram"
          >
            Download SVG
          </button>
          {onCancel ? (
            <button
              type="button"
              onClick={cancel}
              disabled={saving}
              className="rounded-md border border-neutral-200 bg-white px-3 py-1 text-xs text-neutral-700 transition hover:bg-neutral-50 disabled:opacity-50"
            >
              Cancel
            </button>
          ) : null}
          {onSave ? (
            <button
              type="button"
              onClick={save}
              disabled={saving || (disableSaveWhenClean && !dirty)}
              className="rounded-md bg-blue-600 px-3 py-1 text-xs font-medium text-white shadow-sm transition hover:bg-blue-700 disabled:opacity-50"
            >
              {saving ? "Saving…" : saveLabel}
            </button>
          ) : null}
        </span>
      </div>

      <div className="flex min-h-[70vh] gap-2">
        {/* Shape palette — drag one onto the canvas, or click to drop it in
            the middle. Both paths exist because drag is the discoverable
            gesture and click is the reliable one (touch, trackpad, a11y). */}
        <div className="flex w-[104px] shrink-0 flex-col gap-1 rounded-md border border-neutral-200 bg-neutral-50 p-2">
          <div className="px-1 pb-1 text-[10px] font-medium uppercase tracking-wide text-neutral-500">Shapes</div>
          {SHAPE_TOOLS.map((t) => (
            <button
              key={t.kind}
              type="button"
              draggable
              onDragStart={(e) => {
                e.dataTransfer.setData(MIME, t.kind);
                e.dataTransfer.effectAllowed = "copy";
              }}
              onClick={() => {
                const el = wrapRef.current;
                if (!el) return;
                const r = el.getBoundingClientRect();
                addShape(t.kind, toDiagram(r.left + r.width / 2, r.top + r.height / 2), {
                  avoidOverlap: true,
                });
              }}
              title={`${t.label} — ${t.hint}. Drag onto the canvas or click to add.`}
              className="flex cursor-grab items-center gap-2 rounded-md border border-transparent px-1.5 py-1 text-left text-[11px] text-neutral-700 transition hover:border-neutral-200 hover:bg-white active:cursor-grabbing"
            >
              <ShapeGlyph kind={t.kind} />
              <span className="truncate">{t.label}</span>
            </button>
          ))}
          <div className="mt-1 border-t border-neutral-200 pt-2 text-[10px] leading-relaxed text-neutral-500">
            Drag a shape in, or hover a box and click a <span className="font-medium">+</span> to add
            the next one — it lands aligned. Drag a + instead to connect two shapes.
            Double-click to rename.
            <span className="mt-1.5 block text-neutral-400">
              ⌥+arrow adds · Q hides the dots · 1 fits, 2 zooms to selection
            </span>
          </div>
        </div>

        {/* Canvas */}
        <div
          ref={wrapRef}
          className="relative flex-1 overflow-hidden rounded-md border border-neutral-200 bg-white"
          onDragOver={(e) => {
            if (e.dataTransfer.types.includes(MIME)) {
              e.preventDefault();
              e.dataTransfer.dropEffect = "copy";
            }
          }}
          onDrop={(e) => {
            const kind = e.dataTransfer.getData(MIME) as ShapeKind;
            if (!kind) return;
            e.preventDefault();
            addShape(kind, toDiagram(e.clientX, e.clientY));
          }}
        >
          <svg
            ref={svgRef}
            className="h-full w-full"
            style={{ cursor: spaceHeld ? "grab" : drag?.kind === "pan" ? "grabbing" : "default", touchAction: "none" }}
            onPointerDown={onCanvasPointerDown}
            onPointerMove={onCanvasPointerMove}
          >
            <defs>
              {/* Grid follows the pan/zoom so it reads as graph paper under the
                  drawing rather than a fixed screen texture. */}
              <pattern
                id="arti-diagram-grid"
                width={GRID * 4 * view.k}
                height={GRID * 4 * view.k}
                x={view.x}
                y={view.y}
                patternUnits="userSpaceOnUse"
              >
                <circle cx={0.5} cy={0.5} r={0.9} fill="#d4d4d8" />
              </pattern>
            </defs>
            <rect width="100%" height="100%" fill="url(#arti-diagram-grid)" />
            <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
              {doc.edges.map((e) => (
                <EdgeShape
                  key={e.id}
                  doc={doc}
                  edge={e}
                  selected={selEdge === e.id}
                  onPointerDown={(ev) => {
                    ev.stopPropagation();
                    setSelNodes([]);
                    setSelEdge(e.id);
                  }}
                />
              ))}

              {doc.nodes.map((n) => (
                <NodeShape
                  key={n.id}
                  node={n}
                  selected={selNodes.includes(n.id)}
                  dimText={editingID === n.id}
                  onPointerDown={(e) => onNodePointerDown(e, n)}
                  onDoubleClick={(e) => {
                    e.stopPropagation();
                    startEditing(n);
                  }}
                />
              ))}

              {/* In-flight connector */}
              {drag?.kind === "connect" && connectFrom ? (
                <ConnectPreview from={connectFrom} to={drag.cur} overID={drag.over} doc={doc} />
              ) : null}

              {/* Ports */}
              {portNode && !drag && quickAddOn ? (
                <Ports
                  node={portNode}
                  scale={view.k}
                  onPointerDown={(e, side) => onPortPointerDown(e, portNode, side)}
                />
              ) : null}

              {/* Alignment guides for the drag in progress. Drawn last so they
                  sit above the shapes they relate. */}
              {guides.map((g, i) => (
                <line
                  key={i}
                  x1={g.axis === "x" ? g.at : g.from}
                  y1={g.axis === "x" ? g.from : g.at}
                  x2={g.axis === "x" ? g.at : g.to}
                  y2={g.axis === "x" ? g.to : g.at}
                  stroke="#f43f5e"
                  strokeWidth={1 / view.k}
                  pointerEvents="none"
                />
              ))}

              {/* Resize handles — single selection only; on a multi-select the
                  eight dots would be ambiguous about what they resize. */}
              {selNodes.length === 1 && !drag
                ? (() => {
                    const n = nodeByID(doc, selNodes[0]);
                    return n ? (
                      <Handles node={n} scale={view.k} onPointerDown={(e, h) => onHandlePointerDown(e, n, h)} />
                    ) : null;
                  })()
                : null}

              {marquee ? (
                <rect
                  x={marquee.x}
                  y={marquee.y}
                  width={marquee.w}
                  height={marquee.h}
                  fill={`${SELECT_COLOR}14`}
                  stroke={SELECT_COLOR}
                  strokeWidth={1 / view.k}
                  strokeDasharray={`${4 / view.k} ${3 / view.k}`}
                />
              ) : null}
            </g>
          </svg>

          {/* Label editor — an overlay textarea positioned over the node in
              screen space, so text input keeps native caret/IME behaviour
              instead of re-implementing it inside SVG. */}
          {editingNode ? (
            <textarea
              autoFocus
              value={editingText}
              onChange={(e) => setEditingText(e.target.value)}
              onBlur={() => finishEditing(true)}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  finishEditing(true);
                } else if (e.key === "Escape") {
                  e.preventDefault();
                  // Discard the draft. Reset the ref first so the blur that
                  // follows the overlay unmounting can't write it back.
                  editingTextRef.current = editingNode.text;
                  finishEditing(false);
                }
              }}
              className="absolute resize-none rounded-sm border border-blue-400 bg-white/95 text-center outline-none"
              style={{
                left: view.x + editingNode.x * view.k,
                top: view.y + editingNode.y * view.k,
                width: editingNode.w * view.k,
                height: editingNode.h * view.k,
                fontFamily: FONT_FAMILY,
                fontSize: FONT_SIZE * view.k,
                lineHeight: 1.3,
                padding: 6 * view.k,
                color: COLORS[editingNode.color].text,
              }}
            />
          ) : null}

          {/* Selection style bar */}
          {selectedNodes.length > 0 || selectedEdge ? (
            <div className="absolute left-1/2 top-3 flex -translate-x-1/2 flex-wrap items-center gap-1 rounded-md border border-neutral-200 bg-white/95 px-2 py-1.5 shadow-sm backdrop-blur">
              {selectedNodes.length > 0 ? (
                <>
                  {COLOR_KEYS.map((c) => (
                    <button
                      key={c}
                      type="button"
                      title={c}
                      onClick={() => setColor(c)}
                      className="h-5 w-5 rounded-full border transition hover:scale-110"
                      style={{ background: COLORS[c].fill, borderColor: COLORS[c].stroke }}
                    />
                  ))}
                  <span className="mx-1 h-4 w-px bg-neutral-200" />
                  {SHAPE_TOOLS.map((t) => (
                    <button
                      key={t.kind}
                      type="button"
                      title={`turn into ${t.label}`}
                      onClick={() => setShape(t.kind)}
                      className="rounded p-1 text-neutral-600 transition hover:bg-neutral-100"
                    >
                      <ShapeGlyph kind={t.kind} />
                    </button>
                  ))}
                  <span className="mx-1 h-4 w-px bg-neutral-200" />
                  <BarButton onClick={() => commit(bringToFront(docRef.current, selNodes))} title="bring to front">
                    Front
                  </BarButton>
                  <BarButton onClick={() => commit(sendToBack(docRef.current, selNodes))} title="send to back">
                    Back
                  </BarButton>
                  <BarButton onClick={duplicateSelection} title="duplicate (⌘D)">
                    Duplicate
                  </BarButton>
                </>
              ) : selectedEdge ? (
                <>
                  <BarButton
                    onClick={() => commit(updateEdge(docRef.current, selectedEdge.id, { dashed: !selectedEdge.dashed }))}
                    title="toggle dashed"
                  >
                    {selectedEdge.dashed ? "Solid" : "Dashed"}
                  </BarButton>
                  <BarButton
                    onClick={() =>
                      commit(
                        updateEdge(docRef.current, selectedEdge.id, {
                          arrow: selectedEdge.arrow === "none" ? "end" : selectedEdge.arrow === "both" ? "none" : "both",
                        }),
                      )
                    }
                    title="cycle arrowheads"
                  >
                    Arrows: {selectedEdge.arrow ?? "end"}
                  </BarButton>
                  <BarButton
                    onClick={() => {
                      const label = window.prompt("Connector label", selectedEdge.label ?? "");
                      if (label === null) return;
                      commit(updateEdge(docRef.current, selectedEdge.id, { label: label || undefined }));
                    }}
                    title="label this connector"
                  >
                    Label
                  </BarButton>
                </>
              ) : null}
              <span className="mx-1 h-4 w-px bg-neutral-200" />
              <BarButton onClick={deleteSelection} title="delete (⌫)" danger>
                Delete
              </BarButton>
            </div>
          ) : null}

          {/* Viewport controls */}
          <div className="absolute bottom-3 right-3 flex items-center gap-1 rounded-md border border-neutral-200 bg-white/95 px-1.5 py-1 shadow-sm backdrop-blur">
            <BarButton onClick={undo} title="undo (⌘Z)" disabled={past.length === 0}>
              Undo
            </BarButton>
            <BarButton onClick={redo} title="redo (⇧⌘Z)" disabled={future.length === 0}>
              Redo
            </BarButton>
            <span className="mx-1 h-4 w-px bg-neutral-200" />
            <BarButton
              onClick={() => tidy("vertical")}
              title={
                selNodes.length > 1
                  ? "lay the selected shapes out top-to-bottom"
                  : "lay the whole diagram out top-to-bottom"
              }
            >
              Tidy ↓
            </BarButton>
            <BarButton
              onClick={() => tidy("horizontal")}
              title={
                selNodes.length > 1
                  ? "lay the selected shapes out left-to-right"
                  : "lay the whole diagram out left-to-right"
              }
            >
              Tidy →
            </BarButton>
            <span className="mx-1 h-4 w-px bg-neutral-200" />
            <BarButton
              onClick={() => setSnapOn((s) => !s)}
              title="snap to the grid and align to nearby shapes (hold ⌘ for grid only, ` for neither)"
            >
              {snapOn ? "Snap on" : "Snap off"}
            </BarButton>
            <span className="mx-1 h-4 w-px bg-neutral-200" />
            <BarButton onClick={() => zoomBy(1 / 1.2)} title="zoom out">
              −
            </BarButton>
            <span className="w-10 text-center text-[11px] tabular-nums text-neutral-500">
              {Math.round(view.k * 100)}%
            </span>
            <BarButton onClick={() => zoomBy(1.2)} title="zoom in">
              +
            </BarButton>
            <BarButton onClick={fit} title="fit to content">
              Fit
            </BarButton>
          </div>
        </div>
      </div>
    </div>
  );
}

function BarButton({
  children,
  onClick,
  title,
  disabled,
  danger,
}: {
  children: React.ReactNode;
  onClick: () => void;
  title?: string;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      disabled={disabled}
      className={`rounded px-1.5 py-0.5 text-[11px] transition disabled:opacity-40 ${
        danger ? "text-rose-600 hover:bg-rose-50" : "text-neutral-600 hover:bg-neutral-100"
      }`}
    >
      {children}
    </button>
  );
}

// ShapeGlyph is the 14px icon used in the palette and the shape switcher —
// drawn from the same shape vocabulary so the button looks like what it makes.
function ShapeGlyph({ kind }: { kind: ShapeKind }) {
  const common = { fill: "#e4e4e7", stroke: "#71717a", strokeWidth: 1 };
  return (
    <svg width="14" height="14" viewBox="0 0 14 14" aria-hidden="true" className="shrink-0">
      {kind === "ellipse" ? (
        <ellipse cx="7" cy="7" rx="6" ry="4.5" {...common} />
      ) : kind === "diamond" ? (
        <path d="M7 1 L13 7 L7 13 L1 7 Z" {...common} />
      ) : kind === "note" ? (
        <path d="M1.5 2 H9.5 L12.5 5 V12 H1.5 Z" {...common} />
      ) : kind === "text" ? (
        <text x="7" y="10.5" textAnchor="middle" fontSize="10" fill="#52525b" fontFamily={FONT_FAMILY}>
          T
        </text>
      ) : (
        <rect x="1.5" y="3" width="11" height="8" rx={kind === "rounded" ? 4 : 1} {...common} />
      )}
    </svg>
  );
}

// Ports are the four "pull a connector from here" dots. They sit OUTSIDE the
// shape: the edge midpoints are where the resize handles live, and two
// controls stacked on the same pixel means one of them can never be grabbed.
const PORT_OFFSET = 13;

function Ports({
  node,
  scale,
  onPointerDown,
}: {
  node: DiagramNode;
  scale: number;
  onPointerDown: (e: React.PointerEvent, side: Side) => void;
}) {
  // Keep the ring at a constant on-screen distance/size regardless of zoom.
  const off = PORT_OFFSET / scale;
  const r = 6.5 / scale;
  const ports: Array<{ side: Side; at: Point }> = [
    { side: "top", at: { x: node.x + node.w / 2, y: node.y - off } },
    { side: "right", at: { x: node.x + node.w + off, y: node.y + node.h / 2 } },
    { side: "bottom", at: { x: node.x + node.w / 2, y: node.y + node.h + off } },
    { side: "left", at: { x: node.x - off, y: node.y + node.h / 2 } },
  ];
  return (
    <g>
      {ports.map(({ side, at }) => (
        <g
          key={side}
          style={{ cursor: "pointer" }}
          onPointerDown={(e) => onPointerDown(e, side)}
        >
          <circle cx={at.x} cy={at.y} r={r} fill="#ffffff" stroke={SELECT_COLOR} strokeWidth={1.5 / scale} />
          {/* A "+" inside the dot: these both add a shape (click) and draw a
              connector (drag), and the glyph is what advertises the first. */}
          <line
            x1={at.x - r * 0.45}
            y1={at.y}
            x2={at.x + r * 0.45}
            y2={at.y}
            stroke={SELECT_COLOR}
            strokeWidth={1.4 / scale}
            strokeLinecap="round"
            pointerEvents="none"
          />
          <line
            x1={at.x}
            y1={at.y - r * 0.45}
            x2={at.x}
            y2={at.y + r * 0.45}
            stroke={SELECT_COLOR}
            strokeWidth={1.4 / scale}
            strokeLinecap="round"
            pointerEvents="none"
          />
        </g>
      ))}
    </g>
  );
}

// Handles are the eight resize grips. They're scaled by 1/k so they stay the
// same physical size at any zoom level.
function Handles({
  node,
  scale,
  onPointerDown,
}: {
  node: DiagramNode;
  scale: number;
  onPointerDown: (e: React.PointerEvent, h: Handle) => void;
}) {
  const r = 4 / scale;
  const pos: Record<Handle, Point> = {
    nw: { x: node.x, y: node.y },
    n: { x: node.x + node.w / 2, y: node.y },
    ne: { x: node.x + node.w, y: node.y },
    e: { x: node.x + node.w, y: node.y + node.h / 2 },
    se: { x: node.x + node.w, y: node.y + node.h },
    s: { x: node.x + node.w / 2, y: node.y + node.h },
    sw: { x: node.x, y: node.y + node.h },
    w: { x: node.x, y: node.y + node.h / 2 },
  };
  const cursor: Record<Handle, string> = {
    nw: "nwse-resize",
    n: "ns-resize",
    ne: "nesw-resize",
    e: "ew-resize",
    se: "nwse-resize",
    s: "ns-resize",
    sw: "nesw-resize",
    w: "ew-resize",
  };
  return (
    <g>
      {HANDLES.map((h) => (
        <rect
          key={h}
          x={pos[h].x - r}
          y={pos[h].y - r}
          width={r * 2}
          height={r * 2}
          fill="#ffffff"
          stroke={SELECT_COLOR}
          strokeWidth={1.5 / scale}
          style={{ cursor: cursor[h] }}
          onPointerDown={(e) => onPointerDown(e, h)}
        />
      ))}
    </g>
  );
}

// ConnectPreview is the rubber-band line while a connector is being drawn,
// plus a highlight on the box it would land on.
function ConnectPreview({
  from,
  to,
  overID,
  doc,
}: {
  from: DiagramNode;
  to: Point;
  overID: string | null;
  doc: DiagramDoc;
}) {
  const over = overID ? nodeByID(doc, overID) : undefined;
  const cx = from.x + from.w / 2;
  const cy = from.y + from.h / 2;
  return (
    <g pointerEvents="none">
      <line x1={cx} y1={cy} x2={to.x} y2={to.y} stroke={SELECT_COLOR} strokeWidth={1.5} strokeDasharray="5 4" />
      <circle cx={to.x} cy={to.y} r={3} fill={SELECT_COLOR} />
      {over ? (
        <rect
          x={over.x - 3}
          y={over.y - 3}
          width={over.w + 6}
          height={over.h + 6}
          rx={6}
          fill="none"
          stroke={SELECT_COLOR}
          strokeWidth={2}
        />
      ) : null}
    </g>
  );
}
