import { describe, it, expect } from "vitest";
import {
  DIAGRAM_CONTENT_TYPE,
  DiagramParseError,
  MIN_H,
  MIN_W,
  addNode,
  arrowPoints,
  chooseSides,
  connect,
  deleteNodes,
  docBounds,
  duplicateNodes,
  edgeGeometry,
  emptyDiagram,
  GAP,
  GRID,
  autoLayout,
  findFreeSpot,
  hitTestNode,
  isDiagramContentType,
  layoutText,
  makeNode,
  nodesInRect,
  nodeRect,
  parseDiagram,
  placeAdjacent,
  quickAdd,
  rectsOverlap,
  resizeNode,
  serializeDiagram,
  snapToNeighbours,
  starterDiagram,
  styleForNext,
  toSvg,
  wrapText,
  type DiagramDoc,
  type DiagramNode,
  type Rect,
} from "./diagram";

function node(over: Partial<DiagramNode> = {}): DiagramNode {
  return { id: "n1", shape: "rect", x: 0, y: 0, w: 100, h: 60, text: "", color: "gray", ...over };
}

describe("isDiagramContentType", () => {
  it("matches the diagram type, ignoring charset params and case", () => {
    expect(isDiagramContentType(DIAGRAM_CONTENT_TYPE)).toBe(true);
    expect(isDiagramContentType(`${DIAGRAM_CONTENT_TYPE}; charset=utf-8`)).toBe(true);
    expect(isDiagramContentType(DIAGRAM_CONTENT_TYPE.toUpperCase())).toBe(true);
  });

  it("does not swallow plain JSON — those still render as JSON, not a canvas", () => {
    expect(isDiagramContentType("application/json")).toBe(false);
    expect(isDiagramContentType("text/markdown")).toBe(false);
  });
});

describe("parseDiagram", () => {
  it("reads an empty body as an empty diagram", () => {
    expect(parseDiagram("")).toEqual(emptyDiagram());
    expect(parseDiagram("   \n ")).toEqual(emptyDiagram());
  });

  it("throws only on input that isn't a JSON object", () => {
    expect(() => parseDiagram("{nope")).toThrow(DiagramParseError);
    expect(() => parseDiagram("[1,2]")).toThrow(DiagramParseError);
    expect(() => parseDiagram('"a string"')).toThrow(DiagramParseError);
  });

  it("fills defaults for missing node fields rather than rejecting the node", () => {
    const doc = parseDiagram(JSON.stringify({ nodes: [{ id: "a" }] }));
    expect(doc.nodes).toHaveLength(1);
    expect(doc.nodes[0]).toMatchObject({ id: "a", shape: "rect", x: 0, y: 0, color: "gray", text: "" });
    expect(doc.nodes[0].w).toBeGreaterThanOrEqual(MIN_W);
    expect(doc.nodes[0].h).toBeGreaterThanOrEqual(MIN_H);
  });

  it("clamps undersized boxes and falls back on unknown shapes/colors", () => {
    const doc = parseDiagram(
      JSON.stringify({ nodes: [{ id: "a", w: 1, h: 1, shape: "hexagon", color: "chartreuse" }] }),
    );
    expect(doc.nodes[0]).toMatchObject({ w: MIN_W, h: MIN_H, shape: "rect", color: "gray" });
  });

  it("renames duplicate node ids so later edits can't hit two boxes at once", () => {
    const doc = parseDiagram(JSON.stringify({ nodes: [{ id: "a" }, { id: "a" }] }));
    expect(doc.nodes).toHaveLength(2);
    expect(doc.nodes[0].id).not.toBe(doc.nodes[1].id);
  });

  it("drops edges with a missing endpoint — nothing to draw between", () => {
    const doc = parseDiagram(
      JSON.stringify({
        nodes: [{ id: "a" }, { id: "b" }],
        edges: [
          { id: "e1", from: "a", to: "b" },
          { id: "e2", from: "a", to: "ghost" },
          { id: "e3", from: "a", to: "a" },
        ],
      }),
    );
    expect(doc.edges.map((e) => e.id)).toEqual(["e1"]);
  });

  it("ignores junk entries and unknown fields", () => {
    const doc = parseDiagram(
      JSON.stringify({ nodes: [null, 7, { id: "a", bogus: true }], edges: ["x"], extra: 1 }),
    );
    expect(doc.nodes).toHaveLength(1);
    expect(doc.nodes[0]).not.toHaveProperty("bogus");
    expect(doc.edges).toEqual([]);
  });
});

describe("serializeDiagram", () => {
  it("round-trips a document unchanged", () => {
    const doc = starterDiagram();
    expect(parseDiagram(serializeDiagram(doc))).toEqual(doc);
  });

  it("is byte-stable across re-serialization, so a no-op edit produces no diff", () => {
    const once = serializeDiagram(starterDiagram());
    expect(serializeDiagram(parseDiagram(once))).toBe(once);
  });

  it("ends with a newline and omits default edge fields", () => {
    const doc: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a" }), node({ id: "b", x: 200 })],
      edges: [{ id: "e", from: "a", to: "b", arrow: "end", label: "" }],
    };
    const out = serializeDiagram(doc);
    expect(out.endsWith("\n")).toBe(true);
    expect(out).not.toContain('"arrow"');
    expect(out).not.toContain('"label"');
    expect(out).not.toContain('"dashed"');
  });
});

describe("chooseSides", () => {
  it("connects facing sides when boxes sit side by side", () => {
    const a = node({ id: "a", x: 0 });
    const b = node({ id: "b", x: 300 });
    expect(chooseSides(a, b)).toEqual(["right", "left"]);
    expect(chooseSides(b, a)).toEqual(["left", "right"]);
  });

  it("routes top-to-bottom when boxes are stacked", () => {
    const a = node({ id: "a", y: 0 });
    const b = node({ id: "b", y: 300 });
    expect(chooseSides(a, b)).toEqual(["bottom", "top"]);
    expect(chooseSides(b, a)).toEqual(["top", "bottom"]);
  });

  it("picks the axis with the larger gap for a diagonal pair", () => {
    // Far apart horizontally, barely offset vertically → horizontal.
    const a = node({ id: "a", x: 0, y: 0 });
    const b = node({ id: "b", x: 500, y: 40 });
    expect(chooseSides(a, b)).toEqual(["right", "left"]);
  });

  it("still resolves when boxes overlap on both axes", () => {
    const a = node({ id: "a", x: 0, y: 0, w: 200, h: 200 });
    const b = node({ id: "b", x: 20, y: 100, w: 200, h: 200 });
    expect(chooseSides(a, b)).toEqual(["bottom", "top"]);
  });
});

describe("edgeGeometry", () => {
  it("starts and ends exactly on the two boxes' edges", () => {
    const a = node({ id: "a", x: 0, y: 0, w: 100, h: 60 });
    const b = node({ id: "b", x: 300, y: 0, w: 100, h: 60 });
    const g = edgeGeometry(a, b);
    expect(g.start).toEqual({ x: 100, y: 30 });
    expect(g.end).toEqual({ x: 300, y: 30 });
    expect(g.d.startsWith("M 100 30")).toBe(true);
  });

  it("enters a left-side port heading east so the arrowhead meets it square-on", () => {
    const g = edgeGeometry(node({ id: "a" }), node({ id: "b", x: 300 }));
    expect(g.endAngle).toBe(0);
  });

  it("enters a top port heading south (270 in the math-orientation convention)", () => {
    const g = edgeGeometry(node({ id: "a" }), node({ id: "b", y: 300 }));
    expect(g.endAngle).toBe(270);
    // The arrowhead's tail must sit ABOVE the tip in SVG coordinates, or the
    // head points back the way the line came.
    const [tip, tail] = arrowPoints(g.end, g.endAngle, 10);
    expect(tail.y).toBeLessThan(tip.y);
  });

  it("enters a bottom port heading north", () => {
    const g = edgeGeometry(node({ id: "a", y: 300 }), node({ id: "b", y: 0 }));
    expect(g.endAngle).toBe(90);
    const [tip, tail] = arrowPoints(g.end, g.endAngle, 10);
    expect(tail.y).toBeGreaterThan(tip.y);
  });

  it("points reverse arrowheads into the source port for every side pairing", () => {
    expect(edgeGeometry(node({ id: "a", x: 0 }), node({ id: "b", x: 300 })).startAngle).toBe(180);
    expect(edgeGeometry(node({ id: "a", x: 300 }), node({ id: "b", x: 0 })).startAngle).toBe(0);
    expect(edgeGeometry(node({ id: "a", y: 0 }), node({ id: "b", y: 300 })).startAngle).toBe(90);
    expect(edgeGeometry(node({ id: "a", y: 300 }), node({ id: "b", y: 0 })).startAngle).toBe(270);
  });
});

describe("arrowPoints", () => {
  it("puts the tip at the anchor and the tail behind it, along the travel direction", () => {
    const [tip, l, r] = arrowPoints({ x: 100, y: 50 }, 0, 10);
    expect(tip).toEqual({ x: 100, y: 50 });
    // Heading east (angle 0) → the tail sits to the WEST of the tip.
    expect(l.x).toBeCloseTo(90);
    expect(r.x).toBeCloseTo(90);
    expect(Math.abs(l.y - r.y)).toBeCloseTo(9);
  });

  it("rotates with the incoming angle", () => {
    // 90 is north in this convention, so the tail sits BELOW the tip in SVG
    // coordinates (y grows downward).
    const [, l] = arrowPoints({ x: 0, y: 0 }, 90, 10);
    expect(l.y).toBeCloseTo(10, 1);
  });
});

describe("connect", () => {
  const doc: DiagramDoc = { version: 1, nodes: [node({ id: "a" }), node({ id: "b" })], edges: [] };

  it("adds an edge between two existing nodes", () => {
    expect(connect(doc, "a", "b").edges).toHaveLength(1);
  });

  it("refuses self-loops, unknown endpoints, and duplicates in either direction", () => {
    expect(connect(doc, "a", "a")).toBe(doc);
    expect(connect(doc, "a", "ghost")).toBe(doc);
    const once = connect(doc, "a", "b");
    expect(connect(once, "a", "b").edges).toHaveLength(1);
    expect(connect(once, "b", "a").edges).toHaveLength(1);
  });
});

describe("deleteNodes", () => {
  it("also removes every edge that touched the deleted nodes", () => {
    const doc: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a" }), node({ id: "b" }), node({ id: "c" })],
      edges: [
        { id: "ab", from: "a", to: "b" },
        { id: "bc", from: "b", to: "c" },
      ],
    };
    const out = deleteNodes(doc, ["b"]);
    expect(out.nodes.map((n) => n.id)).toEqual(["a", "c"]);
    expect(out.edges).toEqual([]);
  });
});

describe("duplicateNodes", () => {
  it("copies internal edges but never half-connected ones", () => {
    const doc: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a" }), node({ id: "b" }), node({ id: "c" })],
      edges: [
        { id: "ab", from: "a", to: "b" },
        { id: "bc", from: "b", to: "c" },
      ],
    };
    const { doc: out, ids } = duplicateNodes(doc, ["a", "b"]);
    expect(ids).toHaveLength(2);
    expect(out.nodes).toHaveLength(5);
    // ab is internal to the selection and gets copied; bc leaves it and does not.
    expect(out.edges).toHaveLength(3);
    const copied = out.edges.find((e) => e.id !== "ab" && e.id !== "bc")!;
    expect(ids).toContain(copied.from);
    expect(ids).toContain(copied.to);
  });

  it("offsets the copies so they don't hide under the originals", () => {
    const doc = addNode(emptyDiagram(), node({ id: "a", x: 10, y: 10 }));
    const { doc: out, ids } = duplicateNodes(doc, ["a"], 24);
    const copy = out.nodes.find((n) => n.id === ids[0])!;
    expect(copy).toMatchObject({ x: 34, y: 34 });
  });
});

describe("findFreeSpot", () => {
  it("returns the requested spot when nothing is there", () => {
    expect(findFreeSpot(emptyDiagram(), 100, 100, 160, 80)).toEqual({ x: 100, y: 100 });
  });

  it("nudges clear of an existing shape, so click-to-add can't stack invisibly", () => {
    const doc = addNode(emptyDiagram(), node({ x: 100, y: 100, w: 160, h: 80 }));
    const spot = findFreeSpot(doc, 100, 100, 160, 80);
    expect(spot).not.toEqual({ x: 100, y: 100 });
    expect(rectsOverlap({ ...spot, w: 160, h: 80 }, { x: 100, y: 100, w: 160, h: 80 })).toBe(false);
  });

  it("gives up gracefully rather than looping forever on a crowded canvas", () => {
    // A wall of overlapping boxes along the diagonal the search walks.
    const nodes = Array.from({ length: 60 }, (_, i) =>
      node({ id: `n${i}`, x: i * 24, y: i * 24, w: 200, h: 200 }),
    );
    const spot = findFreeSpot({ version: 1, nodes, edges: [] }, 0, 0, 160, 80);
    expect(Number.isFinite(spot.x) && Number.isFinite(spot.y)).toBe(true);
  });
});

describe("resizeNode", () => {
  it("keeps the opposite edge pinned when dragging a north-west handle", () => {
    const n = node({ x: 100, y: 100, w: 200, h: 100 });
    const out = resizeNode(n, "nw", 20, 10);
    expect(out).toMatchObject({ x: 120, y: 110, w: 180, h: 90 });
  });

  it("clamps at the minimum size instead of inverting the box", () => {
    const n = node({ x: 0, y: 0, w: 100, h: 60 });
    const out = resizeNode(n, "se", -1000, -1000);
    expect(out.w).toBe(MIN_W);
    expect(out.h).toBe(MIN_H);
  });

  it("stops a west drag from pushing x past the pinned right edge", () => {
    const n = node({ x: 0, y: 0, w: 100, h: 60 });
    const out = resizeNode(n, "w", 1000, 0);
    expect(out.x + out.w).toBe(100);
    expect(out.w).toBe(MIN_W);
  });
});

describe("hit testing", () => {
  const doc: DiagramDoc = {
    version: 1,
    nodes: [node({ id: "under", x: 0, y: 0, w: 100, h: 100 }), node({ id: "over", x: 50, y: 50, w: 100, h: 100 })],
    edges: [],
  };

  it("returns the topmost node under the point (paint order is z-order)", () => {
    expect(hitTestNode(doc, { x: 60, y: 60 })?.id).toBe("over");
    expect(hitTestNode(doc, { x: 10, y: 10 })?.id).toBe("under");
    expect(hitTestNode(doc, { x: 500, y: 500 })).toBeUndefined();
  });

  it("selects every node a marquee touches, including one dragged up-left", () => {
    expect(nodesInRect(doc, { x: -10, y: -10, w: 30, h: 30 })).toEqual(["under"]);
    expect(nodesInRect(doc, { x: 200, y: 200, w: -180, h: -180 }).sort()).toEqual(["over", "under"]);
  });
});

describe("docBounds", () => {
  it("wraps every node with padding", () => {
    const doc: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a", x: 100, y: 50, w: 100, h: 60 }), node({ id: "b", x: 400, y: 200, w: 100, h: 60 })],
      edges: [],
    };
    expect(docBounds(doc, 10)).toEqual({ x: 90, y: 40, w: 420, h: 230 });
  });

  it("gives an empty diagram a usable non-zero box", () => {
    const b = docBounds(emptyDiagram());
    expect(b.w).toBeGreaterThan(0);
    expect(b.h).toBeGreaterThan(0);
  });
});

describe("wrapText", () => {
  it("wraps on word boundaries", () => {
    const lines = wrapText("the quick brown fox jumps", 60);
    expect(lines.length).toBeGreaterThan(1);
    expect(lines.join(" ")).toBe("the quick brown fox jumps");
  });

  it("honors explicit newlines, including blank lines", () => {
    expect(wrapText("a\n\nb", 200)).toEqual(["a", "", "b"]);
  });

  it("hard-splits a word too long to fit rather than overflowing the shape", () => {
    const lines = wrapText("supercalifragilistic", 40);
    expect(lines.length).toBeGreaterThan(1);
    expect(lines.join("")).toBe("supercalifragilistic");
  });
});

describe("layoutText", () => {
  it("centers the label block inside the node", () => {
    const n = node({ w: 200, h: 100, text: "hello" });
    const t = layoutText(n);
    expect(t.lines).toEqual(["hello"]);
    expect(t.cx).toBe(100);
    // One line in a 100-tall box: the baseline lands near the vertical middle.
    expect(t.firstBaseline).toBeGreaterThan(40);
    expect(t.firstBaseline).toBeLessThan(60);
  });
});

describe("toSvg", () => {
  it("emits a self-contained document with no script or external reference", () => {
    const svg = toSvg(starterDiagram());
    expect(svg.startsWith("<svg xmlns=")).toBe(true);
    expect(svg).toContain("viewBox=");
    expect(svg).not.toContain("<script");
    expect(svg).not.toContain("http://xlink");
  });

  it("escapes label text so a stray angle bracket can't break the markup", () => {
    const a = makeNode("rect", 0, 0, '<img src=x onerror="alert(1)">');
    const doc: DiagramDoc = { version: 1, nodes: [a], edges: [] };
    const svg = toSvg(doc);
    expect(svg).not.toContain("<img");
    expect(svg).toContain("&lt;img");
  });

  it("draws an arrowhead per edge by default and none when arrow is 'none'", () => {
    const a = makeNode("rect", 0, 0, "a");
    const b = makeNode("rect", 300, 0, "b");
    const withArrow: DiagramDoc = { version: 1, nodes: [a, b], edges: [{ id: "e", from: a.id, to: b.id }] };
    const without: DiagramDoc = {
      version: 1,
      nodes: [a, b],
      edges: [{ id: "e", from: a.id, to: b.id, arrow: "none" }],
    };
    expect(toSvg(withArrow)).toContain("<polygon");
    expect(toSvg(without)).not.toContain("<polygon");
  });
});

// ---------------------------------------------------------------------------
// Quick-add placement — "add a block and it lands somewhere clean"
// ---------------------------------------------------------------------------

describe("placeAdjacent", () => {
  const src = node({ id: "src", x: 200, y: 200, w: 160, h: 80 });
  const size = { w: 160, h: 80 };
  const doc = (...extra: DiagramNode[]): DiagramDoc => ({
    version: 1,
    nodes: [src, ...extra],
    edges: [],
  });

  it("centres the new shape on the source's axis, one GAP away", () => {
    const below = placeAdjacent(doc(), src, "bottom", size);
    expect(below).toEqual({ x: 200, y: 200 + 80 + GAP });
    const right = placeAdjacent(doc(), src, "right", size);
    expect(right).toEqual({ x: 200 + 160 + GAP, y: 200 });
  });

  it("places above/left by the new shape's own extent, not the source's", () => {
    expect(placeAdjacent(doc(), src, "top", size)).toEqual({ x: 200, y: 200 - GAP - 80 });
    expect(placeAdjacent(doc(), src, "left", size)).toEqual({ x: 200 - GAP - 160, y: 200 });
  });

  it("keeps a shared centre line, so a chain of quick-adds forms a straight column", () => {
    let d = doc();
    let cur = src;
    const centres: number[] = [];
    for (let i = 0; i < 3; i++) {
      const at = placeAdjacent(d, cur, "bottom", size);
      cur = { ...node({ id: `c${i}` }), ...at, w: size.w, h: size.h };
      d = addNode(d, cur);
      centres.push(at.x + size.w / 2);
    }
    // Every link in the chain shares one centre line.
    expect(new Set(centres).size).toBe(1);
    expect(centres[0]).toBe(src.x + src.w / 2);
  });

  it("slides a second sibling ALONG the layer, keeping both at the same depth", () => {
    const first = placeAdjacent(doc(), src, "bottom", size);
    const sibling = { ...node({ id: "sib" }), ...first, w: size.w, h: size.h };
    const second = placeAdjacent(doc(sibling), src, "bottom", size);
    // Same depth (y), moved sideways — NOT drifting diagonally.
    expect(second.y).toBe(first.y);
    expect(second.x).not.toBe(first.x);
    expect(rectsOverlap({ ...second, ...size }, { ...first, ...size })).toBe(false);
  });

  it("slides perpendicular for horizontal travel too", () => {
    const first = placeAdjacent(doc(), src, "right", size);
    const sibling = { ...node({ id: "sib" }), ...first, w: size.w, h: size.h };
    const second = placeAdjacent(doc(sibling), src, "right", size);
    expect(second.x).toBe(first.x);
    expect(second.y).not.toBe(first.y);
  });

  it("balances siblings on alternating sides of the centre line", () => {
    const a = placeAdjacent(doc(), src, "bottom", size);
    const n1 = { ...node({ id: "s1" }), ...a, w: size.w, h: size.h };
    const b = placeAdjacent(doc(n1), src, "bottom", size);
    const n2 = { ...node({ id: "s2" }), ...b, w: size.w, h: size.h };
    const c = placeAdjacent(doc(n1, n2), src, "bottom", size);
    const xs = [a.x, b.x, c.x].sort((p, q) => p - q);
    // One either side of the parent's centre line, not all stacked to one side.
    expect(xs[0]).toBeLessThan(src.x);
    expect(xs[2]).toBeGreaterThan(src.x);
  });

  it("lands on the grid", () => {
    const at = placeAdjacent(doc(), node({ x: 3, y: 7, w: 160, h: 80 }), "bottom", size);
    expect(at.x % GRID).toBe(0);
    expect(at.y % GRID).toBe(0);
  });
});

describe("styleForNext", () => {
  it("carries the colour over, so a flow built by clicking stays consistent", () => {
    expect(styleForNext(node({ color: "green", shape: "rect" }))).toEqual({
      shape: "rect",
      color: "green",
    });
  });

  it("does not chain diamonds — a decision's branches are steps", () => {
    expect(styleForNext(node({ shape: "diamond", color: "amber" }))).toEqual({
      shape: "rect",
      color: "amber",
    });
  });

  it("does not chain bare text either", () => {
    expect(styleForNext(node({ shape: "text" })).shape).toBe("rect");
  });

  it("keeps other shapes, including notes and ellipses", () => {
    expect(styleForNext(node({ shape: "note" })).shape).toBe("note");
    expect(styleForNext(node({ shape: "ellipse" })).shape).toBe("ellipse");
  });
});

describe("quickAdd", () => {
  const src = node({ id: "src", x: 200, y: 200, w: 160, h: 80, color: "blue" });
  const base: DiagramDoc = { version: 1, nodes: [src], edges: [] };

  it("adds a connected shape and reports it", () => {
    const { doc, node: added } = quickAdd(base, src, "bottom");
    expect(doc.nodes).toHaveLength(2);
    expect(doc.edges).toHaveLength(1);
    expect(added.color).toBe("blue");
    expect(doc.nodes.some((n) => n.id === added.id)).toBe(true);
  });

  it("points the edge the way the flow reads — forward when adding below/right", () => {
    const down = quickAdd(base, src, "bottom");
    expect(down.doc.edges[0]).toMatchObject({ from: src.id, to: down.node.id });
    const right = quickAdd(base, src, "right");
    expect(right.doc.edges[0]).toMatchObject({ from: src.id, to: right.node.id });
  });

  it("reverses the edge when adding above/left — the new shape feeds in", () => {
    const up = quickAdd(base, src, "top");
    expect(up.doc.edges[0]).toMatchObject({ from: up.node.id, to: src.id });
    const left = quickAdd(base, src, "left");
    expect(left.doc.edges[0]).toMatchObject({ from: left.node.id, to: src.id });
  });

  it("never overlaps the shape it came from", () => {
    for (const dir of ["top", "right", "bottom", "left"] as const) {
      const { node: added } = quickAdd(base, src, dir);
      expect(rectsOverlap(nodeRect(added), nodeRect(src))).toBe(false);
    }
  });
});

// ---------------------------------------------------------------------------
// Alignment guides
// ---------------------------------------------------------------------------

describe("snapToNeighbours", () => {
  const other: Rect = { x: 100, y: 100, w: 200, h: 100 };

  it("does nothing when there is nothing nearby to align to", () => {
    const r = snapToNeighbours({ x: 900, y: 900, w: 100, h: 50 }, [other]);
    expect(r).toMatchObject({ dx: 0, dy: 0 });
    expect(r.guides).toEqual([]);
  });

  it("latches a left edge that is a few pixels off", () => {
    const r = snapToNeighbours({ x: 104, y: 400, w: 100, h: 50 }, [other]);
    expect(r.dx).toBe(-4);
    expect(r.guides.some((g) => g.axis === "x" && g.at === 100)).toBe(true);
  });

  it("aligns centres, not just edges", () => {
    // other's centre x is 200; a 100-wide shape centred there starts at 150.
    const r = snapToNeighbours({ x: 153, y: 400, w: 100, h: 50 }, [other]);
    expect(r.dx).toBe(-3);
    expect(r.guides.some((g) => g.axis === "x" && g.at === 200)).toBe(true);
  });

  it("snaps a far edge to a near edge (right-to-left alignment)", () => {
    // moving right edge at 98 → other's left edge at 100.
    const r = snapToNeighbours({ x: -2, y: 400, w: 100, h: 50 }, [other]);
    expect(r.dx).toBe(2);
  });

  it("decides each axis independently", () => {
    const r = snapToNeighbours({ x: 103, y: 97, w: 100, h: 50 }, [other]);
    expect(r.dx).toBe(-3);
    expect(r.dy).toBe(3);
    expect(r.guides).toHaveLength(2);
  });

  it("prefers the smallest correction when several lines are in range", () => {
    const near: Rect = { x: 102, y: 400, w: 50, h: 50 };
    const far: Rect = { x: 96, y: 400, w: 50, h: 50 };
    const r = snapToNeighbours({ x: 100, y: 600, w: 50, h: 50 }, [far, near]);
    expect(r.dx).toBe(2);
  });

  it("draws each guide long enough to span both shapes", () => {
    const r = snapToNeighbours({ x: 104, y: 400, w: 100, h: 50 }, [other]);
    const g = r.guides.find((x) => x.axis === "x")!;
    expect(g.from).toBeLessThanOrEqual(100);
    expect(g.to).toBeGreaterThanOrEqual(450);
  });

  it("reports which axes found an alignment, so the caller can skip the grid there", () => {
    const off = snapToNeighbours({ x: 900, y: 900, w: 100, h: 50 }, [other]);
    expect([off.hitX, off.hitY]).toEqual([false, false]);
    const oneAxis = snapToNeighbours({ x: 104, y: 900, w: 100, h: 50 }, [other]);
    expect([oneAxis.hitX, oneAxis.hitY]).toEqual([true, false]);
  });

  it("flags an ALREADY-aligned axis even though its delta is zero", () => {
    // Exactly aligned on x. A caller that tested `dx !== 0` would fall through
    // to grid snapping and pull the shape back out of alignment.
    const r = snapToNeighbours({ x: 100, y: 900, w: 100, h: 50 }, [other]);
    expect(r.dx).toBe(0);
    expect(r.hitX).toBe(true);
  });

  it("finds no alignment when there are no neighbours to align to (grid-only mode)", () => {
    const r = snapToNeighbours({ x: 104, y: 97, w: 100, h: 50 }, []);
    expect(r).toMatchObject({ dx: 0, dy: 0, hitX: false, hitY: false });
    expect(r.guides).toEqual([]);
  });

  it("respects a custom tolerance", () => {
    expect(snapToNeighbours({ x: 110, y: 400, w: 100, h: 50 }, [other], 4).dx).toBe(0);
    expect(snapToNeighbours({ x: 110, y: 400, w: 100, h: 50 }, [other], 20).dx).toBe(-10);
  });
});

// ---------------------------------------------------------------------------
// Auto-layout
// ---------------------------------------------------------------------------

describe("autoLayout", () => {
  // a → b → c, plus a → d, all dumped at messy positions.
  const messy = (): DiagramDoc => ({
    version: 1,
    nodes: [
      node({ id: "a", x: 500, y: 13, w: 160, h: 80 }),
      node({ id: "b", x: 17, y: 900, w: 160, h: 80 }),
      node({ id: "c", x: 640, y: 411, w: 160, h: 80 }),
      node({ id: "d", x: 90, y: 77, w: 160, h: 80 }),
    ],
    edges: [
      { id: "ab", from: "a", to: "b" },
      { id: "bc", from: "b", to: "c" },
      { id: "ad", from: "a", to: "d" },
    ],
  });

  const at = (doc: DiagramDoc, id: string) => doc.nodes.find((n) => n.id === id)!;

  it("advances one layer per edge, top-to-bottom when vertical", () => {
    const out = autoLayout(messy(), null, "vertical");
    expect(at(out, "a").y).toBeLessThan(at(out, "b").y);
    expect(at(out, "b").y).toBeLessThan(at(out, "c").y);
    // b and d are both one hop from a, so they share a layer.
    expect(at(out, "b").y).toBe(at(out, "d").y);
  });

  it("advances left-to-right when horizontal", () => {
    const out = autoLayout(messy(), null, "horizontal");
    expect(at(out, "a").x).toBeLessThan(at(out, "b").x);
    expect(at(out, "b").x).toBeLessThan(at(out, "c").x);
    expect(at(out, "b").x).toBe(at(out, "d").x);
  });

  it("leaves exactly GAP between adjacent layers", () => {
    const out = autoLayout(messy(), null, "vertical");
    expect(at(out, "b").y - (at(out, "a").y + at(out, "a").h)).toBe(GAP);
  });

  it("separates siblings within a layer without overlap", () => {
    const out = autoLayout(messy(), null, "vertical");
    expect(rectsOverlap(nodeRect(at(out, "b")), nodeRect(at(out, "d")))).toBe(false);
  });

  it("centres a parent over its children", () => {
    const out = autoLayout(messy(), null, "vertical");
    const a = at(out, "a");
    const kids = [at(out, "b"), at(out, "d")];
    const span = [
      Math.min(...kids.map((k) => k.x)),
      Math.max(...kids.map((k) => k.x + k.w)),
    ];
    // Within half a grid step of dead centre: positions snap to the grid, so
    // exact centring and grid alignment can't both hold.
    expect(Math.abs(a.x + a.w / 2 - (span[0] + span[1]) / 2)).toBeLessThanOrEqual(GRID / 2);
  });

  it("never overlaps any two shapes", () => {
    const out = autoLayout(messy(), null, "vertical");
    for (let i = 0; i < out.nodes.length; i++) {
      for (let j = i + 1; j < out.nodes.length; j++) {
        expect(rectsOverlap(nodeRect(out.nodes[i]), nodeRect(out.nodes[j]))).toBe(false);
      }
    }
  });

  it("is idempotent — tidying twice changes nothing the second time", () => {
    const once = autoLayout(messy(), null, "vertical");
    expect(serializeDiagram(autoLayout(once, null, "vertical"))).toBe(serializeDiagram(once));
  });

  it("stays anchored near where the shapes already were, on the grid", () => {
    const before = messy();
    const out = autoLayout(before, null, "vertical");
    const originX = Math.min(...before.nodes.map((n) => n.x)); // 17
    const originY = Math.min(...before.nodes.map((n) => n.y)); // 13
    const minX = Math.min(...out.nodes.map((n) => n.x));
    const minY = Math.min(...out.nodes.map((n) => n.y));
    // Anchored to the old top-left (within a grid step, since it snaps) rather
    // than jumping off to the origin.
    expect(Math.abs(minX - originX)).toBeLessThan(GRID);
    expect(Math.abs(minY - originY)).toBeLessThan(GRID);
    expect(minX % GRID).toBe(0);
    expect(minY % GRID).toBe(0);
  });

  it("only moves the selection when given ids", () => {
    const doc = messy();
    const out = autoLayout(doc, ["a", "b"], "vertical");
    expect(at(out, "c")).toMatchObject({ x: 640, y: 411 });
    expect(at(out, "d")).toMatchObject({ x: 90, y: 77 });
  });

  it("leaves a single shape (or an empty selection) untouched", () => {
    const doc = messy();
    expect(autoLayout(doc, ["a"], "vertical")).toBe(doc);
    expect(autoLayout(emptyDiagram(), null, "vertical")).toEqual(emptyDiagram());
  });

  it("terminates on a cycle rather than hanging", () => {
    const cyclic: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a" }), node({ id: "b" }), node({ id: "c" })],
      edges: [
        { id: "1", from: "a", to: "b" },
        { id: "2", from: "b", to: "c" },
        { id: "3", from: "c", to: "a" },
      ],
    };
    const out = autoLayout(cyclic, null, "vertical");
    expect(out.nodes).toHaveLength(3);
    for (const n of out.nodes) {
      expect(Number.isFinite(n.x) && Number.isFinite(n.y)).toBe(true);
    }
  });

  it("handles disconnected shapes without stacking them", () => {
    const loose: DiagramDoc = {
      version: 1,
      nodes: [node({ id: "a", w: 100, h: 50 }), node({ id: "b", x: 300, w: 100, h: 50 })],
      edges: [],
    };
    const out = autoLayout(loose, null, "vertical");
    expect(rectsOverlap(nodeRect(out.nodes[0]), nodeRect(out.nodes[1]))).toBe(false);
  });

  it("keeps every shape's size — layout moves, it does not resize", () => {
    const before = messy();
    const out = autoLayout(before, null, "vertical");
    for (const n of before.nodes) {
      const after = at(out, n.id);
      expect([after.w, after.h]).toEqual([n.w, n.h]);
    }
  });
});
