// Shared SVG rendering for diagrams. The editor canvas, the read-only viewer
// and the standalone export (lib/diagram.ts toSvg) all draw from the same
// geometry functions in lib/diagram.ts — these components are just the React
// spelling of that geometry, so what you edit is what a reader sees and what a
// downloaded .svg contains.

import {
  COLORS,
  FONT_FAMILY,
  FONT_SIZE,
  LINE_HEIGHT,
  arrowPoints,
  docBounds,
  edgeGeometry,
  hasOutline,
  layoutText,
  nodeByID,
  shapeGeom,
  type DiagramDoc,
  type DiagramEdge,
  type DiagramNode,
  type Point,
} from "@/lib/diagram";

export const EDGE_STROKE = "#71717a";
export const SELECT_COLOR = "#2563eb";

export function ArrowHead({ at, angle, color = EDGE_STROKE }: { at: Point; angle: number; color?: string }) {
  const pts = arrowPoints(at, angle)
    .map((p) => `${p.x},${p.y}`)
    .join(" ");
  return <polygon points={pts} fill={color} />;
}

// EdgeShape draws one connector. In the editor an extra transparent, fat
// "hit" path sits under the visible line so a 1.5px stroke is still clickable.
export function EdgeShape({
  doc,
  edge,
  selected,
  onPointerDown,
}: {
  doc: DiagramDoc;
  edge: DiagramEdge;
  selected?: boolean;
  onPointerDown?: (e: React.PointerEvent) => void;
}) {
  const from = nodeByID(doc, edge.from);
  const to = nodeByID(doc, edge.to);
  if (!from || !to) return null;
  const g = edgeGeometry(from, to);
  const color = selected ? SELECT_COLOR : EDGE_STROKE;
  return (
    <g>
      {onPointerDown ? (
        <path
          d={g.d}
          fill="none"
          stroke="transparent"
          strokeWidth={14}
          style={{ cursor: "pointer" }}
          onPointerDown={onPointerDown}
        />
      ) : null}
      <path
        d={g.d}
        fill="none"
        stroke={color}
        strokeWidth={selected ? 2.5 : 1.5}
        strokeDasharray={edge.dashed ? "6 4" : undefined}
        pointerEvents="none"
      />
      {edge.arrow !== "none" ? <ArrowHead at={g.end} angle={g.endAngle} color={color} /> : null}
      {edge.arrow === "both" ? <ArrowHead at={g.start} angle={g.startAngle} color={color} /> : null}
      {edge.label ? (
        <text
          x={g.mid.x}
          y={g.mid.y}
          textAnchor="middle"
          dominantBaseline="middle"
          fontFamily={FONT_FAMILY}
          fontSize={11}
          fill="#52525b"
          paintOrder="stroke"
          stroke="#ffffff"
          strokeWidth={4}
          pointerEvents="none"
        >
          {edge.label}
        </text>
      ) : null}
    </g>
  );
}

// NodeShape draws one box (or ellipse/diamond/note/bare text) plus its wrapped,
// vertically-centered label.
export function NodeShape({
  node,
  selected,
  dimText,
  onPointerDown,
  onDoubleClick,
}: {
  node: DiagramNode;
  selected?: boolean;
  // dimText hides the label while it's being edited in the overlay textarea,
  // so the two don't render on top of each other.
  dimText?: boolean;
  onPointerDown?: (e: React.PointerEvent) => void;
  onDoubleClick?: (e: React.MouseEvent) => void;
}) {
  const c = COLORS[node.color];
  const g = shapeGeom(node);
  const outline = hasOutline(node);
  const stroke = selected ? SELECT_COLOR : c.stroke;
  const strokeWidth = selected ? 2 : 1.5;
  const t = layoutText(node);
  const interactive = !!onPointerDown;
  const common = {
    fill: outline ? c.fill : "transparent",
    stroke: outline ? stroke : "transparent",
    strokeWidth: outline ? strokeWidth : 0,
  };
  return (
    <g
      onPointerDown={onPointerDown}
      onDoubleClick={onDoubleClick}
      style={interactive ? { cursor: "move" } : undefined}
    >
      {/* Bare text still needs a hit area in the editor, hence the transparent
          rect for the no-outline case. */}
      {g.kind === "rect" ? (
        <rect x={g.x} y={g.y} width={g.w} height={g.h} rx={g.rx} {...common} />
      ) : g.kind === "ellipse" ? (
        <ellipse cx={g.cx} cy={g.cy} rx={g.rx} ry={g.ry} {...common} />
      ) : (
        <path d={g.d} {...common} />
      )}
      {!outline && selected ? (
        <rect
          x={node.x}
          y={node.y}
          width={node.w}
          height={node.h}
          rx={2}
          fill="none"
          stroke={SELECT_COLOR}
          strokeWidth={1.5}
          strokeDasharray="4 3"
        />
      ) : null}
      {dimText ? null : (
        <text
          textAnchor="middle"
          fontFamily={FONT_FAMILY}
          fontSize={FONT_SIZE}
          fill={c.text}
          pointerEvents="none"
          style={{ userSelect: "none" }}
        >
          {t.lines.map((line, i) => (
            <tspan key={i} x={t.cx} y={t.firstBaseline + i * LINE_HEIGHT}>
              {line}
            </tspan>
          ))}
        </text>
      )}
    </g>
  );
}

// DiagramFigure is the read-only render: edges under nodes, viewBox fitted to
// the content so a diagram of any size scales into whatever box the page gives
// it. No interaction, no client state — safe to render on the server.
export default function DiagramFigure({
  doc,
  className,
  padding = 24,
  title,
}: {
  doc: DiagramDoc;
  className?: string;
  padding?: number;
  title?: string;
}) {
  const b = docBounds(doc, padding);
  return (
    <svg
      viewBox={`${b.x} ${b.y} ${b.w} ${b.h}`}
      className={className}
      role="img"
      aria-label={title ? `diagram: ${title}` : "diagram"}
      preserveAspectRatio="xMidYMid meet"
    >
      {doc.edges.map((e) => (
        <EdgeShape key={e.id} doc={doc} edge={e} />
      ))}
      {doc.nodes.map((n) => (
        <NodeShape key={n.id} node={n} />
      ))}
    </svg>
  );
}
