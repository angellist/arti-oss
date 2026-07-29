---
title: Drawing diagrams
order: 5
summary: Build flowcharts and boxes-and-arrows diagrams on a drag-and-drop canvas, versioned like any other artifact and exportable as SVG.
---

# Drawing diagrams

Diagrams are drawn in arti, not uploaded to it. **New diagram** in the left rail
opens a canvas: drag shapes in, wire them together, and save. What you get back
is an ordinary artifact, so a diagram has a slug, versions, labels, scopes,
access control, comments and search — everything on this page comes from the
[artifact model](../overview/concepts.md), not from anything diagram-specific.

## Drawing

**The fast way to build a flow is the `+` dots.** Hover any shape and four of
them appear, just outside its edges. **Click** one and you get the next shape on
that side — already connected, already the right distance away, and centred on
the shape it came from, so a chain you build by clicking comes out as a straight,
evenly spaced column. It opens ready to name, so you can go
click-type-click-type without ever positioning anything. `⌥`+any arrow key does
the same thing without the mouse, and `Q` hides the dots when a dense diagram
makes them noisy.

Add a second shape on the same side and it goes *beside* the first, at the same
depth, rather than on top of it — branches come out as tidy rows. New shapes
inherit the colour of the one they came from, so a flow stays visually
consistent; a diamond's branches come out as boxes, since a decision leads to
steps rather than more decisions.

| To do this | Do that |
|---|---|
| Add the next shape | Hover a shape and **click** a `+` dot (or `⌥`+arrow) |
| Add a shape anywhere | Drag one from the **Shapes** palette onto the canvas — or click it in the palette |
| Name a shape | Double-click it (or select it and press `Enter`), type, then `Enter` to commit / `Shift+Enter` for a new line |
| Move | Drag it. Hold `Shift` while clicking to move several at once |
| Resize | Select a shape and drag one of its eight handles |
| Connect two existing shapes | **Drag** from a `+` dot onto another shape |
| Branch out to a spot you pick | Drag from a `+` dot onto **empty canvas** — you get a new box there, already connected |
| Tidy up | **Tidy ↓** / **Tidy →** in the bottom bar |
| Select several | Drag a box around them on empty canvas, or `Shift`-click each |
| Restyle | With a selection, use the bar at the top: colour, shape, front/back, duplicate, delete |
| Label a connector | Select the connector, then **Label**. The same bar toggles dashed lines and arrowheads |

Shapes carry the usual flowchart meanings — rounded for start/end, box for a
step, diamond for a decision, ellipse for a state, note for a comment, and bare
text for a caption with no outline.

### Alignment

Drag a shape near another and it snaps flush — edge to edge, or centre to
centre — with a red guide showing what it lined up with. Alignment to nearby
shapes wins over the grid, because landing level with a neighbour is almost
always what you meant. Two overrides while dragging:

- hold `⌘` (or `Ctrl`) to ignore the other shapes and snap only to the grid
- hold `` ` `` to turn off snapping altogether for exact placement

**Snap on/off** in the bottom bar disables the whole thing if you'd rather place
everything by hand.

### Tidying up

**Tidy ↓** and **Tidy →** lay connected shapes out as a clean flow: one layer per
hop, even spacing, and each shape centred over the ones it feeds. Connectors
re-route themselves because routing is derived from position, never stored. With
several shapes selected it tidies just those; with nothing selected it does the
whole diagram. It's safe to press repeatedly — a tidy diagram is already at its
fixed point.

### Keyboard

`⌘Z` / `⇧⌘Z` undo and redo · `⌘D` duplicates · `⌘A` selects everything ·
`Delete` removes the selection · arrow keys nudge (hold `Shift` to nudge
further) · `⌥`+arrow adds a connected shape · `Q` toggles the `+` dots ·
`1` fits the diagram, `2` zooms to the selection · `Escape` deselects.

### Moving around

Scroll or trackpad-swipe to pan; hold `Space` (or the middle mouse button) and
drag to pan from anywhere. `⌘`-scroll — or pinch — zooms at the cursor. The
controls in the bottom-right zoom, **Fit** to the drawing, tidy, and toggle
snapping.

## Saving and versions

Saving a new diagram creates the artifact. Saving an edit to an existing one
publishes a **new version of the same slug** — exactly like editing a markdown
artifact — and the version you were looking at is left untouched. Because the
stored form is text, the version compare view diffs two revisions of a diagram
the same way it diffs prose.

If you open an older version and save, that older content (plus your edits)
becomes the newest version; the editor warns before it does.

## Sharing one

- **Download SVG** (in the viewer and the editor) gives you a self-contained
  file with no external fonts or scripts — safe to drop into a doc, a README or
  a slide.
- **Full Page** renders the diagram alone, filling the window — the view to put
  on a screen share.
- The usual `/s/<slug>` and `/a/<uuid>` URLs work, and pin to a version with
  `/s/<slug>/v_<n>`.

## What's stored

The body is JSON with the content type `application/vnd.arti.diagram+json`:
a list of nodes (shape, position, size, text, colour) and a list of edges
between node ids. **Raw Source** in the viewer shows it, and the API, CLI and
MCP tools treat it as any other TEXT artifact — so a diagram can be generated
or updated by a script or an agent, and it will open in the canvas afterwards.

Connector routing is not stored: edges attach to whichever sides of the two
shapes face each other, recomputed on every render, so moving a box re-routes
its connectors instead of leaving them stranded.
