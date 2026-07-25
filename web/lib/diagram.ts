// The diagram document model — arti's drag-and-drop flowchart format.
//
// A diagram is stored as an ordinary TEXT artifact whose content_type is
// DIAGRAM_CONTENT_TYPE, so it inherits slugs, versioning, access control,
// search and comments from the artifact layer with no new storage. The body
// is JSON in the shape of DiagramDoc.
//
// Everything here is framework-free and side-effect-free on purpose: the
// editor, the read-only viewer and the standalone SVG export all render from
// these same functions, so what you see while editing is what a reader (or a
// downloaded .svg) gets. See diagram.test.ts for the invariants.

export const DIAGRAM_CONTENT_TYPE = "application/vnd.arti.diagram+json";

// isDiagramContentType ignores charset/params, mirroring the other
// content-type predicates in lib/viewer.ts.
export function isDiagramContentType(ct: string): boolean {
  return ct.split(";")[0].trim().toLowerCase() === DIAGRAM_CONTENT_TYPE;
}

export type ShapeKind = "rect" | "rounded" | "ellipse" | "diamond" | "note" | "text";

// Colors are stored as palette KEYS, never raw hex: a diagram authored today
// keeps rendering sensibly if the palette is retuned, and the JSON stays
// readable/diffable across versions (arti diffs stored text between versions).
export type ColorKey = "gray" | "blue" | "green" | "amber" | "rose" | "purple";

export interface Palette {
  fill: string;
  stroke: string;
  text: string;
}

export const COLORS: Record<ColorKey, Palette> = {
  gray: { fill: "#f4f4f5", stroke: "#a1a1aa", text: "#27272a" },
  blue: { fill: "#eff6ff", stroke: "#60a5fa", text: "#1e3a8a" },
  green: { fill: "#ecfdf5", stroke: "#34d399", text: "#065f46" },
  amber: { fill: "#fffbeb", stroke: "#fbbf24", text: "#78350f" },
  rose: { fill: "#fff1f2", stroke: "#fb7185", text: "#881337" },
  purple: { fill: "#faf5ff", stroke: "#c084fc", text: "#581c87" },
};

export const COLOR_KEYS: ColorKey[] = ["gray", "blue", "green", "amber", "rose", "purple"];

export interface DiagramNode {
  id: string;
  shape: ShapeKind;
  x: number;
  y: number;
  w: number;
  h: number;
  text: string;
  color: ColorKey;
}

export type Side = "top" | "right" | "bottom" | "left";

export interface DiagramEdge {
  id: string;
  from: string;
  to: string;
  label?: string;
  dashed?: boolean;
  // arrow: which ends carry an arrowhead. Default "end".
  arrow?: "none" | "end" | "both";
}

export interface DiagramDoc {
  version: 1;
  nodes: DiagramNode[];
  edges: DiagramEdge[];
}

export const GRID = 8;
export const MIN_W = 48;
export const MIN_H = 32;
export const DEFAULT_W = 168;
export const DEFAULT_H = 72;

// Font metrics used for text wrapping. The editor and the exported SVG both
// declare this family/size, so a line that fits in one fits in the other.
export const FONT_FAMILY =
  "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif";
export const FONT_SIZE = 13;
export const LINE_HEIGHT = 17;
const CHAR_W = FONT_SIZE * 0.56; // average advance width for the family above

export function emptyDiagram(): DiagramDoc {
  return { version: 1, nodes: [], edges: [] };
}

// ---------------------------------------------------------------------------
// Parsing / serialization
// ---------------------------------------------------------------------------

export class DiagramParseError extends Error {}

const SHAPES: ShapeKind[] = ["rect", "rounded", "ellipse", "diamond", "note", "text"];

function num(v: unknown, fallback: number): number {
  return typeof v === "number" && Number.isFinite(v) ? v : fallback;
}

function str(v: unknown, fallback = ""): string {
  return typeof v === "string" ? v : fallback;
}

// parseDiagram is deliberately tolerant: a diagram body is user-editable text
// (someone can open the raw source and hand-edit it), and half-valid content
// should still open in the editor rather than bricking the viewer. Unknown
// fields are dropped, out-of-range numbers are clamped, duplicate ids are
// renamed, and edges pointing at missing nodes are discarded. Only input that
// isn't a JSON object at all throws.
export function parseDiagram(text: string): DiagramDoc {
  const trimmed = text.trim();
  if (!trimmed) return emptyDiagram();
  let raw: unknown;
  try {
    raw = JSON.parse(trimmed);
  } catch (e) {
    throw new DiagramParseError(e instanceof Error ? e.message : "invalid JSON");
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new DiagramParseError("diagram must be a JSON object");
  }
  const obj = raw as Record<string, unknown>;
  const nodes: DiagramNode[] = [];
  const seen = new Set<string>();
  for (const rawNode of Array.isArray(obj.nodes) ? obj.nodes : []) {
    if (!rawNode || typeof rawNode !== "object") continue;
    const n = rawNode as Record<string, unknown>;
    let id = str(n.id);
    if (!id || seen.has(id)) id = newID("n");
    seen.add(id);
    const shape = SHAPES.includes(n.shape as ShapeKind) ? (n.shape as ShapeKind) : "rect";
    nodes.push({
      id,
      shape,
      x: num(n.x, 0),
      y: num(n.y, 0),
      w: Math.max(MIN_W, num(n.w, DEFAULT_W)),
      h: Math.max(MIN_H, num(n.h, DEFAULT_H)),
      text: str(n.text),
      color: COLOR_KEYS.includes(n.color as ColorKey) ? (n.color as ColorKey) : "gray",
    });
  }
  const byID = new Set(nodes.map((n) => n.id));
  const edges: DiagramEdge[] = [];
  const edgeIDs = new Set<string>();
  for (const rawEdge of Array.isArray(obj.edges) ? obj.edges : []) {
    if (!rawEdge || typeof rawEdge !== "object") continue;
    const e = rawEdge as Record<string, unknown>;
    const from = str(e.from);
    const to = str(e.to);
    // A dangling edge has nothing to draw between — dropping it is what keeps
    // hand-edited JSON from throwing during render.
    if (!byID.has(from) || !byID.has(to) || from === to) continue;
    let id = str(e.id);
    if (!id || edgeIDs.has(id)) id = newID("e");
    edgeIDs.add(id);
    const edge: DiagramEdge = { id, from, to };
    const label = str(e.label);
    if (label) edge.label = label;
    if (e.dashed === true) edge.dashed = true;
    if (e.arrow === "none" || e.arrow === "both") edge.arrow = e.arrow;
    edges.push(edge);
  }
  return { version: 1, nodes, edges };
}

// serializeDiagram writes canonical JSON: fixed key order, one node/edge per
// line group, trailing newline. Stability matters because every save is a new
// artifact version and the compare view diffs the raw text — reordered keys
// would show up as spurious changes.
export function serializeDiagram(doc: DiagramDoc): string {
  const nodes = doc.nodes.map((n) => ({
    id: n.id,
    shape: n.shape,
    x: round(n.x),
    y: round(n.y),
    w: round(n.w),
    h: round(n.h),
    text: n.text,
    color: n.color,
  }));
  const edges = doc.edges.map((e) => {
    const out: Record<string, unknown> = { id: e.id, from: e.from, to: e.to };
    if (e.label) out.label = e.label;
    if (e.dashed) out.dashed = true;
    if (e.arrow && e.arrow !== "end") out.arrow = e.arrow;
    return out;
  });
  return JSON.stringify({ version: 1, nodes, edges }, null, 2) + "\n";
}

function round(v: number): number {
  return Math.round(v * 100) / 100;
}

// newID mints a short, collision-resistant id. Math.random is fine here: ids
// only need to be unique within one document, and a diagram is edited by one
// browser at a time (saves are whole-document versions, not merges).
export function newID(prefix: string): string {
  return `${prefix}_${Math.random().toString(36).slice(2, 9)}`;
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

export interface Point {
  x: number;
  y: number;
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export function snap(v: number, grid = GRID): number {
  return Math.round(v / grid) * grid;
}

export function nodeCenter(n: DiagramNode): Point {
  return { x: n.x + n.w / 2, y: n.y + n.h / 2 };
}

export function nodeRect(n: DiagramNode): Rect {
  return { x: n.x, y: n.y, w: n.w, h: n.h };
}

// docBounds is the tight box around every node, grown by `pad`. Used for
// zoom-to-fit and as the exported SVG's viewBox. An empty document gets a
// small default box so callers never divide by a zero-sized viewport.
export function docBounds(doc: DiagramDoc, pad = 24): Rect {
  if (doc.nodes.length === 0) return { x: 0, y: 0, w: 320, h: 200 };
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const n of doc.nodes) {
    minX = Math.min(minX, n.x);
    minY = Math.min(minY, n.y);
    maxX = Math.max(maxX, n.x + n.w);
    maxY = Math.max(maxY, n.y + n.h);
  }
  return { x: minX - pad, y: minY - pad, w: maxX - minX + pad * 2, h: maxY - minY + pad * 2 };
}

export function pointInRect(p: Point, r: Rect): boolean {
  return p.x >= r.x && p.x <= r.x + r.w && p.y >= r.y && p.y <= r.y + r.h;
}

export function rectsOverlap(a: Rect, b: Rect): boolean {
  return a.x < b.x + b.w && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;
}

// hitTestNode returns the TOPMOST node under a point. Later nodes in the array
// paint on top, so the scan runs backwards — the same order the renderer uses.
export function hitTestNode(doc: DiagramDoc, p: Point): DiagramNode | undefined {
  for (let i = doc.nodes.length - 1; i >= 0; i--) {
    if (pointInRect(p, nodeRect(doc.nodes[i]))) return doc.nodes[i];
  }
  return undefined;
}

export function nodesInRect(doc: DiagramDoc, r: Rect): string[] {
  return doc.nodes.filter((n) => rectsOverlap(nodeRect(n), normalizeRect(r))).map((n) => n.id);
}

// normalizeRect turns a drag rectangle (which can have negative w/h when
// dragged up/left) into one with positive extents.
export function normalizeRect(r: Rect): Rect {
  return {
    x: r.w < 0 ? r.x + r.w : r.x,
    y: r.h < 0 ? r.y + r.h : r.y,
    w: Math.abs(r.w),
    h: Math.abs(r.h),
  };
}

// anchorPoint is where an edge attaches on a given side of a node.
export function anchorPoint(n: DiagramNode, side: Side): Point {
  switch (side) {
    case "top":
      return { x: n.x + n.w / 2, y: n.y };
    case "bottom":
      return { x: n.x + n.w / 2, y: n.y + n.h };
    case "left":
      return { x: n.x, y: n.y + n.h / 2 };
    case "right":
      return { x: n.x + n.w, y: n.y + n.h / 2 };
  }
}

// chooseSides picks the port pair for an edge from the nodes' relative
// positions: whichever axis separates them more wins, and the ports face each
// other. This is what makes connections look intentional without the user ever
// choosing a side (Whimsical's "just drag between two boxes" feel).
export function chooseSides(a: DiagramNode, b: DiagramNode): [Side, Side] {
  const ca = nodeCenter(a);
  const cb = nodeCenter(b);
  const dx = cb.x - ca.x;
  const dy = cb.y - ca.y;
  // Signed gaps between the boxes on each axis: positive when they're clear of
  // each other, negative by the amount they overlap. Routing along the axis
  // with the larger gap is what keeps a line from cutting back across the box
  // it just left.
  const hGap = Math.max(b.x - (a.x + a.w), a.x - (b.x + b.w));
  const vGap = Math.max(b.y - (a.y + a.h), a.y - (b.y + b.h));
  const horizontal =
    hGap >= 0 || vGap >= 0
      ? hGap >= vGap
      : // Boxes overlap on both axes (dragged on top of each other): fall back
        // to whichever way the centers are further apart.
        Math.abs(dx) >= Math.abs(dy);
  if (horizontal) return dx >= 0 ? ["right", "left"] : ["left", "right"];
  return dy >= 0 ? ["bottom", "top"] : ["top", "bottom"];
}

export interface EdgeGeometry {
  // d is an SVG path with a single elbow (or a straight run when the ports
  // line up), so crossings stay readable without a full routing engine.
  d: string;
  start: Point;
  end: Point;
  // startAngle is the direction a reverse arrowhead points into the source.
  startAngle: number;
  // endAngle is the incoming direction in degrees, for orienting the arrowhead.
  endAngle: number;
  // mid is where a label is centered.
  mid: Point;
}

// edgeGeometry builds the drawn path between two nodes. The elbow is placed at
// the midpoint of the dominant axis, and the path leaves/enters each port
// perpendicular to its side so an arrowhead always meets the box square-on.
export function edgeGeometry(from: DiagramNode, to: DiagramNode): EdgeGeometry {
  const [sa, sb] = chooseSides(from, to);
  const start = anchorPoint(from, sa);
  const end = anchorPoint(to, sb);
  const horizontal = sa === "left" || sa === "right";
  let d: string;
  let mid: Point;
  if (horizontal) {
    const mx = (start.x + end.x) / 2;
    d = `M ${fmt(start.x)} ${fmt(start.y)} L ${fmt(mx)} ${fmt(start.y)} L ${fmt(mx)} ${fmt(end.y)} L ${fmt(end.x)} ${fmt(end.y)}`;
    mid = { x: mx, y: (start.y + end.y) / 2 };
  } else {
    const my = (start.y + end.y) / 2;
    d = `M ${fmt(start.x)} ${fmt(start.y)} L ${fmt(start.x)} ${fmt(my)} L ${fmt(end.x)} ${fmt(my)} L ${fmt(end.x)} ${fmt(end.y)}`;
    mid = { x: (start.x + end.x) / 2, y: my };
  }
  // Angles follow arrowPoints' convention: degrees counter-clockwise from
  // east, in math orientation (90 = north), NOT SVG's downward y. An arrow
  // landing on the target's top edge is travelling south, hence 270.
  const sideAngle = (side: Side) => (side === "left" ? 0 : side === "right" ? 180 : side === "top" ? 270 : 90);
  const startAngle = sideAngle(sa);
  const endAngle = sideAngle(sb);
  return { d, start, end, startAngle, endAngle, mid };
}

function fmt(v: number): string {
  return String(Math.round(v * 10) / 10);
}

// ---------------------------------------------------------------------------
// Shapes
// ---------------------------------------------------------------------------

export type ShapeGeom =
  | { kind: "rect"; x: number; y: number; w: number; h: number; rx: number }
  | { kind: "ellipse"; cx: number; cy: number; rx: number; ry: number }
  | { kind: "path"; d: string };

// shapeGeom is the single source of truth for what a shape looks like. The
// React canvas and the standalone SVG exporter both render from it, so an
// exported file can't drift from what was on screen.
export function shapeGeom(n: DiagramNode): ShapeGeom {
  switch (n.shape) {
    case "rounded":
      return { kind: "rect", x: n.x, y: n.y, w: n.w, h: n.h, rx: Math.min(16, n.h / 2) };
    case "ellipse":
      return { kind: "ellipse", cx: n.x + n.w / 2, cy: n.y + n.h / 2, rx: n.w / 2, ry: n.h / 2 };
    case "diamond": {
      const cx = n.x + n.w / 2;
      const cy = n.y + n.h / 2;
      return {
        kind: "path",
        d: `M ${fmt(cx)} ${fmt(n.y)} L ${fmt(n.x + n.w)} ${fmt(cy)} L ${fmt(cx)} ${fmt(n.y + n.h)} L ${fmt(n.x)} ${fmt(cy)} Z`,
      };
    }
    case "note": {
      // A rectangle with the top-right corner folded — the classic sticky note.
      const f = 14;
      const r = n.x + n.w;
      const b = n.y + n.h;
      return {
        kind: "path",
        d: `M ${fmt(n.x)} ${fmt(n.y)} L ${fmt(r - f)} ${fmt(n.y)} L ${fmt(r)} ${fmt(n.y + f)} L ${fmt(r)} ${fmt(b)} L ${fmt(n.x)} ${fmt(b)} Z`,
      };
    }
    case "text":
      // Bare text: no outline at all, so it never boxes in a caption.
      return { kind: "rect", x: n.x, y: n.y, w: n.w, h: n.h, rx: 0 };
    case "rect":
    default:
      return { kind: "rect", x: n.x, y: n.y, w: n.w, h: n.h, rx: 4 };
  }
}

// hasOutline is false only for the bare-text shape, which draws its label with
// no fill or stroke.
export function hasOutline(n: DiagramNode): boolean {
  return n.shape !== "text";
}

// Diamonds and ellipses lose width fast away from their centerline, so text
// gets a tighter box than the bounding rect suggests.
function textInset(n: DiagramNode): number {
  if (n.shape === "diamond") return n.w * 0.22;
  if (n.shape === "ellipse") return n.w * 0.12;
  return 10;
}

// wrapText breaks a label into lines that fit `maxWidth` at FONT_SIZE, honoring
// explicit newlines first. Words longer than a line are hard-split rather than
// allowed to overflow the shape. Measurement is metric-based (no DOM), so the
// same result is produced during SSR, in the editor, and in the SVG export.
export function wrapText(text: string, maxWidth: number): string[] {
  const out: string[] = [];
  const maxChars = Math.max(1, Math.floor(maxWidth / CHAR_W));
  for (const para of text.split("\n")) {
    if (para === "") {
      out.push("");
      continue;
    }
    let line = "";
    for (const word of para.split(/\s+/).filter(Boolean)) {
      let w = word;
      // Hard-split an over-long word across as many lines as it needs.
      while (w.length > maxChars) {
        if (line) {
          out.push(line);
          line = "";
        }
        out.push(w.slice(0, maxChars));
        w = w.slice(maxChars);
      }
      const candidate = line ? `${line} ${w}` : w;
      if (candidate.length > maxChars && line) {
        out.push(line);
        line = w;
      } else {
        line = candidate;
      }
    }
    out.push(line);
  }
  return out;
}

export interface TextLayout {
  lines: string[];
  // Baseline y of the first line; subsequent lines advance by LINE_HEIGHT.
  firstBaseline: number;
  cx: number;
}

// layoutText vertically centers the wrapped label inside the node.
export function layoutText(n: DiagramNode): TextLayout {
  const inset = textInset(n);
  const lines = wrapText(n.text, Math.max(CHAR_W, n.w - inset * 2));
  const block = lines.length * LINE_HEIGHT;
  const top = n.y + (n.h - block) / 2;
  return { lines, firstBaseline: top + LINE_HEIGHT * 0.75, cx: n.x + n.w / 2 };
}

// autoHeight grows a node so its wrapped label fits. Used when text is edited:
// a box that clips its own content looks broken, and Whimsical-style editing
// expects the shape to give way to the words.
export function autoHeight(n: DiagramNode): number {
  const inset = textInset(n);
  const lines = wrapText(n.text, Math.max(CHAR_W, n.w - inset * 2));
  return Math.max(n.h, MIN_H, lines.length * LINE_HEIGHT + 20);
}

// ---------------------------------------------------------------------------
// Document edits — all pure: each returns a NEW doc, which is what makes the
// editor's undo stack a plain array of snapshots.
// ---------------------------------------------------------------------------

export function makeNode(shape: ShapeKind, x: number, y: number, text = ""): DiagramNode {
  const w = shape === "text" ? 140 : DEFAULT_W;
  const h = shape === "ellipse" || shape === "diamond" ? 88 : shape === "text" ? 40 : DEFAULT_H;
  return { id: newID("n"), shape, x, y, w, h, text, color: "gray" };
}

// findFreeSpot nudges a would-be position diagonally until it no longer lands
// on an existing shape. Used by click-to-add, where the drop point is the
// canvas centre rather than something the user aimed at — without this, three
// clicks stack three shapes in the same place and only the top one is visible.
// Drag-and-drop skips this: there the position IS the user's choice.
export function findFreeSpot(doc: DiagramDoc, x: number, y: number, w: number, h: number): Point {
  const step = GRID * 3;
  for (let i = 0; i < 40; i++) {
    const at = { x: x + i * step, y: y + i * step };
    if (!doc.nodes.some((n) => rectsOverlap({ ...at, w, h }, nodeRect(n)))) return at;
  }
  return { x, y };
}

export function addNode(doc: DiagramDoc, node: DiagramNode): DiagramDoc {
  return { ...doc, nodes: [...doc.nodes, node] };
}

export function updateNode(doc: DiagramDoc, id: string, patch: Partial<DiagramNode>): DiagramDoc {
  return { ...doc, nodes: doc.nodes.map((n) => (n.id === id ? { ...n, ...patch } : n)) };
}

export function updateNodes(doc: DiagramDoc, ids: string[], patch: Partial<DiagramNode>): DiagramDoc {
  const set = new Set(ids);
  return { ...doc, nodes: doc.nodes.map((n) => (set.has(n.id) ? { ...n, ...patch } : n)) };
}

export function moveNodes(doc: DiagramDoc, ids: string[], dx: number, dy: number): DiagramDoc {
  const set = new Set(ids);
  return { ...doc, nodes: doc.nodes.map((n) => (set.has(n.id) ? { ...n, x: n.x + dx, y: n.y + dy } : n)) };
}

// resizeNode applies a drag on one of the eight handles, keeping the opposite
// edge pinned and never letting the box invert (min width/height clamp).
export function resizeNode(n: DiagramNode, handle: string, dx: number, dy: number): DiagramNode {
  let { x, y, w, h } = n;
  if (handle.includes("e")) w = Math.max(MIN_W, w + dx);
  if (handle.includes("s")) h = Math.max(MIN_H, h + dy);
  if (handle.includes("w")) {
    const nw = Math.max(MIN_W, w - dx);
    x += w - nw;
    w = nw;
  }
  if (handle.includes("n")) {
    const nh = Math.max(MIN_H, h - dy);
    y += h - nh;
    h = nh;
  }
  return { ...n, x, y, w, h };
}

// deleteNodes drops the nodes AND every edge that touched them — an edge with
// a missing endpoint has nothing to draw between, and parseDiagram would strip
// it on the next load anyway.
export function deleteNodes(doc: DiagramDoc, ids: string[]): DiagramDoc {
  const set = new Set(ids);
  return {
    ...doc,
    nodes: doc.nodes.filter((n) => !set.has(n.id)),
    edges: doc.edges.filter((e) => !set.has(e.from) && !set.has(e.to)),
  };
}

export function deleteEdges(doc: DiagramDoc, ids: string[]): DiagramDoc {
  const set = new Set(ids);
  return { ...doc, edges: doc.edges.filter((e) => !set.has(e.id)) };
}

// connect adds an edge, refusing self-loops and duplicates (in either
// direction) so repeated drags between the same pair can't stack invisible
// copies on top of each other.
export function connect(doc: DiagramDoc, from: string, to: string): DiagramDoc {
  if (from === to) return doc;
  if (!doc.nodes.some((n) => n.id === from) || !doc.nodes.some((n) => n.id === to)) return doc;
  const dupe = doc.edges.some(
    (e) => (e.from === from && e.to === to) || (e.from === to && e.to === from),
  );
  if (dupe) return doc;
  return { ...doc, edges: [...doc.edges, { id: newID("e"), from, to }] };
}

export function updateEdge(doc: DiagramDoc, id: string, patch: Partial<DiagramEdge>): DiagramDoc {
  return { ...doc, edges: doc.edges.map((e) => (e.id === id ? { ...e, ...patch } : e)) };
}

// duplicateNodes copies a selection (offset so the copies are visible) along
// with any edges whose BOTH endpoints are in the selection — copying a
// half-connected edge would silently rewire the original.
export function duplicateNodes(
  doc: DiagramDoc,
  ids: string[],
  offset = 24,
): { doc: DiagramDoc; ids: string[] } {
  const set = new Set(ids);
  const idMap = new Map<string, string>();
  const copies: DiagramNode[] = [];
  for (const n of doc.nodes) {
    if (!set.has(n.id)) continue;
    const copy = { ...n, id: newID("n"), x: n.x + offset, y: n.y + offset };
    idMap.set(n.id, copy.id);
    copies.push(copy);
  }
  const newEdges = doc.edges
    .filter((e) => set.has(e.from) && set.has(e.to))
    .map((e) => ({ ...e, id: newID("e"), from: idMap.get(e.from)!, to: idMap.get(e.to)! }));
  return {
    doc: { ...doc, nodes: [...doc.nodes, ...copies], edges: [...doc.edges, ...newEdges] },
    ids: copies.map((n) => n.id),
  };
}

// bringToFront / sendToBack reorder within doc.nodes — paint order IS z-order.
export function bringToFront(doc: DiagramDoc, ids: string[]): DiagramDoc {
  const set = new Set(ids);
  const rest = doc.nodes.filter((n) => !set.has(n.id));
  const moved = doc.nodes.filter((n) => set.has(n.id));
  return { ...doc, nodes: [...rest, ...moved] };
}

export function sendToBack(doc: DiagramDoc, ids: string[]): DiagramDoc {
  const set = new Set(ids);
  const rest = doc.nodes.filter((n) => !set.has(n.id));
  const moved = doc.nodes.filter((n) => set.has(n.id));
  return { ...doc, nodes: [...moved, ...rest] };
}

export function nodeByID(doc: DiagramDoc, id: string): DiagramNode | undefined {
  return doc.nodes.find((n) => n.id === id);
}

// ---------------------------------------------------------------------------
// Standalone SVG export
// ---------------------------------------------------------------------------

function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

// toSvg renders a self-contained SVG document — no external fonts, no CSS, no
// script — suitable for downloading, embedding in a README, or pasting into a
// doc. Arrowheads are drawn as explicit polygons rather than markers so the
// file survives tools that strip <defs>.
export function toSvg(doc: DiagramDoc): string {
  const b = docBounds(doc);
  const parts: string[] = [];
  for (const e of doc.edges) {
    const from = nodeByID(doc, e.from);
    const to = nodeByID(doc, e.to);
    if (!from || !to) continue;
    const g = edgeGeometry(from, to);
    parts.push(
      `<path d="${g.d}" fill="none" stroke="#71717a" stroke-width="1.5"${e.dashed ? ' stroke-dasharray="6 4"' : ""}/>`,
    );
    if (e.arrow !== "none") parts.push(arrowPolygon(g.end, g.endAngle));
    if (e.arrow === "both") {
      parts.push(arrowPolygon(g.start, g.startAngle));
    }
    if (e.label) {
      parts.push(
        `<text x="${fmt(g.mid.x)}" y="${fmt(g.mid.y)}" text-anchor="middle" dominant-baseline="middle" ` +
          `font-family="${esc(FONT_FAMILY)}" font-size="11" fill="#52525b" paint-order="stroke" ` +
          `stroke="#ffffff" stroke-width="4">${esc(e.label)}</text>`,
      );
    }
  }
  for (const n of doc.nodes) {
    const c = COLORS[n.color];
    if (hasOutline(n)) {
      const g = shapeGeom(n);
      if (g.kind === "rect") {
        parts.push(
          `<rect x="${fmt(g.x)}" y="${fmt(g.y)}" width="${fmt(g.w)}" height="${fmt(g.h)}" rx="${fmt(g.rx)}" fill="${c.fill}" stroke="${c.stroke}" stroke-width="1.5"/>`,
        );
      } else if (g.kind === "ellipse") {
        parts.push(
          `<ellipse cx="${fmt(g.cx)}" cy="${fmt(g.cy)}" rx="${fmt(g.rx)}" ry="${fmt(g.ry)}" fill="${c.fill}" stroke="${c.stroke}" stroke-width="1.5"/>`,
        );
      } else {
        parts.push(`<path d="${g.d}" fill="${c.fill}" stroke="${c.stroke}" stroke-width="1.5"/>`);
      }
    }
    const t = layoutText(n);
    if (t.lines.some((l) => l !== "")) {
      const tspans = t.lines
        .map(
          (line, i) =>
            `<tspan x="${fmt(t.cx)}" y="${fmt(t.firstBaseline + i * LINE_HEIGHT)}">${esc(line)}</tspan>`,
        )
        .join("");
      parts.push(
        `<text text-anchor="middle" font-family="${esc(FONT_FAMILY)}" font-size="${FONT_SIZE}" fill="${c.text}">${tspans}</text>`,
      );
    }
  }
  return (
    `<svg xmlns="http://www.w3.org/2000/svg" width="${fmt(b.w)}" height="${fmt(b.h)}" ` +
    `viewBox="${fmt(b.x)} ${fmt(b.y)} ${fmt(b.w)} ${fmt(b.h)}">` +
    `<rect x="${fmt(b.x)}" y="${fmt(b.y)}" width="${fmt(b.w)}" height="${fmt(b.h)}" fill="#ffffff"/>` +
    parts.join("") +
    `</svg>\n`
  );
}

// arrowPoints returns the three corners of an arrowhead at `p`, pointing along
// `angleDeg` (0 = pointing right/east, measured counter-clockwise, matching
// edgeGeometry's endAngle convention).
export function arrowPoints(p: Point, angleDeg: number, size = 9): Point[] {
  const a = (angleDeg * Math.PI) / 180;
  const cos = Math.cos(a);
  const sin = Math.sin(a);
  // Local space: tip at origin, tail back along -x, spread on y.
  const local: Point[] = [
    { x: 0, y: 0 },
    { x: -size, y: -size * 0.45 },
    { x: -size, y: size * 0.45 },
  ];
  // SVG's y axis points down, so rotating by -a keeps the visual convention.
  return local.map((q) => ({
    x: p.x + q.x * cos + q.y * sin,
    y: p.y - q.x * sin + q.y * cos,
  }));
}

function arrowPolygon(p: Point, angleDeg: number): string {
  const pts = arrowPoints(p, angleDeg)
    .map((q) => `${fmt(q.x)},${fmt(q.y)}`)
    .join(" ");
  return `<polygon points="${pts}" fill="#71717a"/>`;
}

// ---------------------------------------------------------------------------
// Quick-add placement
// ---------------------------------------------------------------------------

// GAP is the standard breathing room between a shape and one added next to it.
// Every quick-add and every auto-layout uses this one number, which is what
// makes a diagram built by clicking look deliberately spaced rather than
// hand-dropped.
export const GAP = 56;

// Margin used when testing "is this spot free?" — shapes that merely come
// within a few pixels of each other read as a collision, not as neighbours.
const CLEARANCE = 16;

function grow(r: Rect, by: number): Rect {
  return { x: r.x - by, y: r.y - by, w: r.w + by * 2, h: r.h + by * 2 };
}

// placeAdjacent answers "where should a new shape go if I add one below/right
// of this one?" — the heart of adding a block and having it land somewhere
// clean.
//
// Two rules, in order:
//  1. Centre it on the source's perpendicular axis, GAP away in `dir`. A child
//     added below its parent shares the parent's centre line, so a chain built
//     by clicking comes out as a straight, evenly-spaced column.
//  2. If that spot is occupied (a sibling is already there), slide along the
//     perpendicular axis — never along the direction of travel — until it's
//     free. Siblings therefore form a neat row at the SAME depth instead of
//     drifting diagonally away from the parent.
export function placeAdjacent(
  doc: DiagramDoc,
  source: DiagramNode,
  dir: Side,
  size: { w: number; h: number },
): Point {
  const cx = source.x + source.w / 2 - size.w / 2;
  const cy = source.y + source.h / 2 - size.h / 2;
  let pos: Point;
  switch (dir) {
    case "top":
      pos = { x: cx, y: source.y - GAP - size.h };
      break;
    case "bottom":
      pos = { x: cx, y: source.y + source.h + GAP };
      break;
    case "left":
      pos = { x: source.x - GAP - size.w, y: cy };
      break;
    case "right":
      pos = { x: source.x + source.w + GAP, y: cy };
      break;
  }
  // Perpendicular slide axis: vertical travel slides sideways, and vice versa.
  const horizontalTravel = dir === "left" || dir === "right";
  const others = doc.nodes.filter((n) => n.id !== source.id).map(nodeRect);
  const occupied = (r: Rect) => others.some((o) => rectsOverlap(grow(r, CLEARANCE), o));
  if (!occupied({ ...pos, ...size })) return { x: snap(pos.x), y: snap(pos.y) };
  // Try alternating sides of the centre line so a third sibling balances the
  // row rather than always extending it one way.
  for (let i = 1; i <= 12; i++) {
    for (const sign of [1, -1]) {
      const step = sign * i * (horizontalTravel ? size.h + GAP : size.w + GAP);
      const candidate = horizontalTravel
        ? { x: pos.x, y: pos.y + step }
        : { x: pos.x + step, y: pos.y };
      if (!occupied({ ...candidate, ...size })) return { x: snap(candidate.x), y: snap(candidate.y) };
    }
  }
  return { x: snap(pos.x), y: snap(pos.y) };
}

// styleForNext decides what a quick-added shape looks like. Whimsical's rule is
// that the next shape keeps the current one's styling, which is what makes
// building a flow feel like one continuous act — so colour always carries over.
// Shape carries over too, EXCEPT from a diamond (a decision's branches are
// steps, not more decisions) or bare text (a caption's neighbour is a box).
export function styleForNext(source: DiagramNode): { shape: ShapeKind; color: ColorKey } {
  const shape: ShapeKind =
    source.shape === "diamond" || source.shape === "text" ? "rect" : source.shape;
  return { shape, color: source.color };
}

// quickAdd creates a shape adjacent to `source`, connected to it, and returns
// both the new document and the new node so the caller can select it and drop
// straight into renaming it.
export function quickAdd(
  doc: DiagramDoc,
  source: DiagramNode,
  dir: Side,
): { doc: DiagramDoc; node: DiagramNode } {
  const { shape, color } = styleForNext(source);
  const proto = makeNode(shape, 0, 0);
  const at = placeAdjacent(doc, source, dir, { w: proto.w, h: proto.h });
  const node: DiagramNode = { ...proto, x: at.x, y: at.y, color };
  // Edge direction follows the drawing direction: adding above/left of a shape
  // means the new shape feeds INTO it.
  const backwards = dir === "top" || dir === "left";
  const withNode = addNode(doc, node);
  const connected = backwards
    ? connect(withNode, node.id, source.id)
    : connect(withNode, source.id, node.id);
  return { doc: connected, node };
}

// ---------------------------------------------------------------------------
// Alignment guides (auto-alignment while dragging)
// ---------------------------------------------------------------------------

// SNAP_TOLERANCE is how close (in document units) an edge or centre has to be
// before it latches. Kept in document space so the feel is consistent at any
// zoom, matching how the grid behaves.
export const SNAP_TOLERANCE = 6;

// A Guide is one alignment line to draw: `at` is its position on `axis`, and
// from/to is the span it should cover so the line visibly connects the shapes
// it relates.
export interface Guide {
  axis: "x" | "y";
  at: number;
  from: number;
  to: number;
}

export interface SnapResult {
  dx: number;
  dy: number;
  // Whether an alignment was found on each axis. Distinct from `dx !== 0`,
  // because an ALREADY-aligned shape produces a zero delta that still must
  // suppress grid snapping on that axis — otherwise the grid would drag it
  // back out of alignment.
  hitX: boolean;
  hitY: boolean;
  guides: Guide[];
}

// snapToNeighbours nudges a dragged rect so its edges or centre line up with
// nearby shapes, and reports the guides to draw. Each axis is decided
// independently (a shape can be centre-aligned vertically while its left edge
// snaps horizontally), and the smallest correction wins.
//
// Returns zero deltas and no guides when nothing is within tolerance, so the
// caller can fall back to plain grid snapping.
export function snapToNeighbours(
  moving: Rect,
  others: Rect[],
  tolerance = SNAP_TOLERANCE,
): SnapResult {
  // Candidate lines on each axis: near edge, centre, far edge.
  const linesOf = (r: Rect, axis: "x" | "y") =>
    axis === "x" ? [r.x, r.x + r.w / 2, r.x + r.w] : [r.y, r.y + r.h / 2, r.y + r.h];

  const best: Record<"x" | "y", { delta: number; at: number; other: Rect } | null> = {
    x: null,
    y: null,
  };
  for (const axis of ["x", "y"] as const) {
    const mine = linesOf(moving, axis);
    for (const other of others) {
      for (const theirs of linesOf(other, axis)) {
        for (const m of mine) {
          const delta = theirs - m;
          if (Math.abs(delta) > tolerance) continue;
          if (!best[axis] || Math.abs(delta) < Math.abs(best[axis]!.delta)) {
            best[axis] = { delta, at: theirs, other };
          }
        }
      }
    }
  }

  const guides: Guide[] = [];
  const dx = best.x?.delta ?? 0;
  const dy = best.y?.delta ?? 0;
  const snapped: Rect = { ...moving, x: moving.x + dx, y: moving.y + dy };
  if (best.x) {
    // A vertical guide at x, spanning both shapes so the relationship is legible.
    const o = best.x.other;
    guides.push({
      axis: "x",
      at: best.x.at,
      from: Math.min(snapped.y, o.y),
      to: Math.max(snapped.y + snapped.h, o.y + o.h),
    });
  }
  if (best.y) {
    const o = best.y.other;
    guides.push({
      axis: "y",
      at: best.y.at,
      from: Math.min(snapped.x, o.x),
      to: Math.max(snapped.x + snapped.w, o.x + o.w),
    });
  }
  return { dx, dy, hitX: best.x !== null, hitY: best.y !== null, guides };
}

// ---------------------------------------------------------------------------
// Auto-layout
// ---------------------------------------------------------------------------

export type LayoutDirection = "vertical" | "horizontal";

// autoLayout rearranges connected shapes into a clean layered flow: every edge
// advances one layer along the primary axis, siblings pack along the cross axis
// with GAP between them, and a parent centres over its children. Connectors
// need no rerouting because routing is derived from positions at render time.
//
// `ids` limits the layout to a subset (a selection); null lays out everything.
// Nodes outside the graph, cycles, multiple parents and disconnected components
// are all tolerated — this has to be safe to press on any diagram.
export function autoLayout(
  doc: DiagramDoc,
  ids: string[] | null,
  direction: LayoutDirection,
): DiagramDoc {
  const inScope = ids && ids.length > 0 ? new Set(ids) : new Set(doc.nodes.map((n) => n.id));
  const nodes = doc.nodes.filter((n) => inScope.has(n.id));
  if (nodes.length < 2) return doc;
  const byID = new Map(nodes.map((n) => [n.id, n]));
  const edges = doc.edges.filter((e) => inScope.has(e.from) && inScope.has(e.to));

  const children = new Map<string, string[]>();
  const indegree = new Map<string, number>();
  for (const n of nodes) {
    children.set(n.id, []);
    indegree.set(n.id, 0);
  }
  for (const e of edges) {
    children.get(e.from)!.push(e.to);
    indegree.set(e.to, indegree.get(e.to)! + 1);
  }

  // Depth = longest path from a root, computed by relaxation rather than
  // recursion so a cycle can't blow the stack. Nodes with no incoming edge are
  // roots; a pure cycle (every node has a parent) falls back to the first node
  // in document order so it still gets laid out.
  const depth = new Map<string, number>();
  for (const n of nodes) depth.set(n.id, 0);
  // |V| relaxation rounds is the standard bound for longest-path on a DAG; the
  // early exit keeps it cheap, and the bound also terminates on cyclic input
  // (where depths simply keep climbing until the rounds run out). Nodes with no
  // incoming edge stay at 0 and act as roots; so does anything disconnected.
  for (let round = 0; round < nodes.length; round++) {
    let changed = false;
    for (const e of edges) {
      const next = depth.get(e.from)! + 1;
      if (next > depth.get(e.to)!) {
        depth.set(e.to, next);
        changed = true;
      }
    }
    if (!changed) break;
  }

  // Compact the depths into contiguous ranks. Relaxation on a cycle leaves
  // gaps (a=6, b=4, c=5), and a gap would otherwise mean empty layers — which
  // both wastes space and leaves holes in the layer array.
  const ranks = [...new Set(depth.values())].sort((a, b) => a - b);
  const rankOf = new Map(ranks.map((d, i) => [d, i]));
  for (const n of nodes) depth.set(n.id, rankOf.get(depth.get(n.id)!)!);

  // Group by layer, preserving document order within a layer for stability —
  // pressing the button twice must not shuffle the result.
  const layers: string[][] = ranks.map(() => []);
  for (const n of nodes) layers[depth.get(n.id)!].push(n.id);

  const vertical = direction === "vertical";
  // Primary axis = the direction flow advances; cross axis = within a layer.
  const primarySize = (n: DiagramNode) => (vertical ? n.h : n.w);
  const crossSize = (n: DiagramNode) => (vertical ? n.w : n.h);

  // Layer offsets along the primary axis: each layer starts GAP after the
  // tallest/widest shape in the previous one.
  const layerStart: number[] = [];
  let cursor = 0;
  for (const layer of layers) {
    layerStart.push(cursor);
    const extent = Math.max(...layer.map((id) => primarySize(byID.get(id)!)));
    cursor += extent + GAP;
  }

  // Pass 1: pack each layer along the cross axis in order.
  const cross = new Map<string, number>();
  for (const layer of layers) {
    let at = 0;
    for (const id of layer) {
      cross.set(id, at);
      at += crossSize(byID.get(id)!) + GAP;
    }
  }

  // Pass 2: from the deepest layer upward, centre each parent over its
  // children, then re-resolve overlaps within the layer by pushing later
  // siblings along. Going bottom-up means a parent sees final child positions.
  for (let d = layers.length - 2; d >= 0; d--) {
    for (const id of layers[d]) {
      const kids = (children.get(id) ?? []).filter((k) => cross.has(k) && depth.get(k)! > d);
      if (kids.length === 0) continue;
      const lo = Math.min(...kids.map((k) => cross.get(k)!));
      const hi = Math.max(...kids.map((k) => cross.get(k)! + crossSize(byID.get(k)!)));
      cross.set(id, (lo + hi) / 2 - crossSize(byID.get(id)!) / 2);
    }
    // Restore non-overlap in this layer without undoing the centring more than
    // necessary: sort by current position, then push right where needed.
    const ordered = [...layers[d]].sort((a, b) => cross.get(a)! - cross.get(b)!);
    for (let i = 1; i < ordered.length; i++) {
      const prev = ordered[i - 1];
      const min = cross.get(prev)! + crossSize(byID.get(prev)!) + GAP;
      if (cross.get(ordered[i])! < min) cross.set(ordered[i], min);
    }
  }

  // Anchor the result at the original selection's top-left so the diagram
  // doesn't jump across the canvas when you tidy it.
  const originX = Math.min(...nodes.map((n) => n.x));
  const originY = Math.min(...nodes.map((n) => n.y));
  const minCross = Math.min(...nodes.map((n) => cross.get(n.id)!));

  const placed = new Map<string, Point>();
  for (const n of nodes) {
    const primary = layerStart[depth.get(n.id)!];
    const c = cross.get(n.id)! - minCross;
    placed.set(
      n.id,
      vertical
        ? { x: snap(originX + c), y: snap(originY + primary) }
        : { x: snap(originX + primary), y: snap(originY + c) },
    );
  }

  return {
    ...doc,
    nodes: doc.nodes.map((n) => {
      const p = placed.get(n.id);
      return p ? { ...n, x: p.x, y: p.y } : n;
    }),
  };
}

// ---------------------------------------------------------------------------
// Starter content
// ---------------------------------------------------------------------------

// starterDiagram is what a brand-new canvas opens with — two connected boxes.
// An entirely blank canvas gives no hint that shapes can be dragged in or
// wired together; this shows both in one glance and is trivial to delete.
export function starterDiagram(): DiagramDoc {
  const a = makeNode("rounded", 120, 120, "Start");
  const b = makeNode("rect", 120, 280, "Next step");
  b.color = "blue";
  return { version: 1, nodes: [a, b], edges: [{ id: newID("e"), from: a.id, to: b.id }] };
}
