// Vanilla controller for the comments overlay. Kept outside React because
// it does imperative DOM work (wrapping text ranges in <mark>, positioning
// floating cards by anchor geometry) that would fight reconciliation. The
// React wrapper (CommentsLayer) just mounts/unmounts it.
//
// It overlays arti-rendered content (text/markdown), anchoring comments by
// text-quote, pin, or document-level. CSS is injected once under an `ac-`
// prefix so it never collides with the app's Tailwind.

import {
  listComments,
  createThread,
  replyComment,
  resolveThread,
  reopenThread,
  deleteComment as apiDeleteComment,
  editComment as apiEditComment,
  type ThreadDTO,
  type CommentDTO,
} from "./arti";
import {
  computeShift as computeShiftFrom,
  CARD_RIGHT,
  CARD_WIDTH,
  type Shift,
} from "./commentsGeometry";

// The overlay talks to the server through this small interface so it can be
// driven by either the cookie-based client (in-app viewer) or a token-based
// client (the bundle injected into a sandboxed served-HTML page).
export interface CommentsApi {
  list(artifactId: string): Promise<{ threads: ThreadDTO[] }>;
  create(artifactId: string, anchor: ThreadDTO["anchor"], body: string): Promise<ThreadDTO>;
  reply(threadId: string, body: string): Promise<CommentDTO>;
  resolve(threadId: string): Promise<void>;
  reopen(threadId: string): Promise<void>;
  del(threadId: string, commentId: string): Promise<void>;
  edit(threadId: string, commentId: string, body: string): Promise<CommentDTO>;
}

const cookieApi: CommentsApi = {
  list: listComments,
  create: createThread,
  reply: replyComment,
  resolve: resolveThread,
  reopen: reopenThread,
  del: apiDeleteComment,
  edit: apiEditComment,
};

export interface OverlayOpts {
  container: HTMLElement | null; // the rendered doc body to anchor into (null → nothing to anchor)
  artifactId: string;
  me: { email: string; name?: string; picture?: string; is_admin: boolean } | null;
  allowPin?: boolean; // point/pin comments are HTML-only
  api?: CommentsApi; // defaults to the cookie-based client
}

const AV = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c] as string));

const CSS = `
.ac-hl{background:rgba(250,204,21,.28);border-radius:2px;cursor:pointer;transition:background .12s}
.ac-hl.ac-resolved{background:rgba(120,120,110,.16)}
.ac-hl.ac-active{background:rgba(250,204,21,.92);box-shadow:0 0 0 1px rgba(202,138,4,.85)}
.ac-hl.ac-draft{background:rgba(96,165,250,.3)}
.ac-layer{position:fixed;inset:0;pointer-events:none;z-index:60;font-family:Inter,system-ui,sans-serif}
.ac-layer.ac-hidden{display:none}
.ac-pinning,.ac-pinning *{cursor:crosshair !important}
.ac-pin{position:fixed;transform:translate(-50%,-100%);pointer-events:auto;cursor:pointer;width:24px;height:30px;display:grid;place-items:center}
.ac-pin .ac-bub{width:24px;height:24px;border-radius:50% 50% 50% 2px;transform:rotate(45deg);background:#2563eb;box-shadow:0 2px 6px rgba(37,99,235,.4);display:grid;place-items:center}
.ac-pin .ac-n{transform:rotate(-45deg);color:#fff;font-size:11px;font-weight:700}
.ac-pin.ac-resolved .ac-bub{background:#a8a8a2;box-shadow:none}
.ac-pin.ac-active .ac-bub,.ac-pin.ac-draft .ac-bub{outline:3px solid #eff4ff}
.ac-card{position:fixed;width:${CARD_WIDTH}px;pointer-events:auto;background:rgba(255,255,255,.6);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(230,229,225,.8);border-radius:13px;box-shadow:0 6px 24px -10px rgba(40,40,30,.24);padding:11px 12px;cursor:pointer}
.ac-cmt{position:relative}
.ac-del,.ac-edit,.ac-link{position:absolute;top:0;border:none;background:transparent;color:#c9c8c3;font-size:12px;cursor:pointer;padding:2px 4px;line-height:1;opacity:0}
.ac-link{right:0}
.ac-del{right:18px}
.ac-edit{right:36px}
.ac-cmt:hover .ac-del,.ac-cmt:hover .ac-edit,.ac-cmt:hover .ac-link{opacity:1}
.ac-del:hover{color:#be123c}
.ac-edit:hover{color:#2563eb}
.ac-link:hover{color:#2563eb}
.ac-link.ac-copied{color:#15803d;opacity:1}
.ac-cmt.ac-flash{border-radius:6px;animation:ac-flash 1.7s ease-out}
@keyframes ac-flash{0%,25%{background:rgba(250,204,21,.45)}100%{background:transparent}}
.ac-edit-input{font:inherit;font-size:12.5px;width:100%;border:1px solid #2563eb;border-radius:6px;padding:5px 8px;background:#fff;resize:none;min-height:32px}
.ac-edit-input:focus{outline:none}
.ac-edit-acts{display:flex;gap:4px;margin-top:4px}
.ac-panel{position:fixed;width:${CARD_WIDTH}px;max-height:70vh;overflow:auto;pointer-events:auto;background:rgba(255,255,255,.6);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(230,229,225,.8);border-radius:13px;box-shadow:0 10px 34px -12px rgba(40,40,30,.34);padding:6px}
.ac-panel h4{margin:8px 8px 4px;font-size:10px;text-transform:uppercase;letter-spacing:.09em;color:#9b9b95;font-weight:600}
.ac-prow{display:flex;align-items:flex-start;gap:8px;padding:8px;border-radius:9px;cursor:pointer}
.ac-prow:hover{background:#f6f5f2}
.ac-prow.ac-res{opacity:.65}
.ac-prow .ac-pbody{flex:1;min-width:0}
.ac-prow .ac-pmeta{font-size:10px;color:#9b9b95;font-weight:600;margin-bottom:2px}
.ac-prow .ac-psnip{font-size:12px;color:#33332e;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.ac-prow .ac-pacts{display:flex;gap:4px;flex-shrink:0}
.ac-card.ac-active{border-color:#2563eb;box-shadow:0 0 0 3px #eff4ff,0 10px 30px -12px rgba(40,40,30,.35)}
.ac-card.ac-resolved{opacity:.6}
.ac-card.ac-draft{border-color:#2563eb;box-shadow:0 0 0 3px #eff4ff;cursor:default}
.ac-chip{display:inline-flex;align-items:center;gap:5px;font-size:10.5px;line-height:1.4;max-height:22px;font-weight:600;color:#6b6b66;background:#f6f5f2;border:1px solid #efeeea;border-radius:6px;padding:2px 7px;margin-bottom:8px;max-width:100%;overflow:hidden}
.ac-chip .ac-q{color:#9b9b95;font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:170px}
.ac-cmt{display:flex;gap:9px;margin:7px 0}
.ac-av{position:relative;overflow:hidden;flex:0 0 24px;width:24px;height:24px;border-radius:50%;display:grid;place-items:center;font-size:10px;font-weight:700;color:#fff;background:#2563eb}
.ac-av img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}
.ac-who{font-size:12px;font-weight:600;color:#1c1c1a}
.ac-when{font-size:10.5px;color:#9b9b95;margin-left:6px}
.ac-text{font-size:12.5px;color:#33332e;margin-top:1px;white-space:pre-wrap;word-wrap:break-word}
.ac-snip{font-size:12.5px;color:#44443e;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden;padding-right:16px}
.ac-more{font-size:11px;color:#2563eb;font-weight:600;margin-top:4px}
.ac-reply{display:flex;margin-top:8px}
.ac-reply textarea{flex:1;min-width:0;font:inherit;font-size:12.5px;line-height:1.45;border:1px solid #e6e5e1;border-radius:8px;padding:7px 10px;background:#f6f5f2;resize:none;box-sizing:border-box;min-height:34px;max-height:160px;overflow-y:auto}
.ac-reply textarea:focus{outline:none;border-color:#2563eb;background:#fff}
.ac-acts{display:flex;gap:6px;margin-top:9px;padding-top:9px;border-top:1px solid #efeeea}
.ac-mini{font:inherit;font-size:11.5px;font-weight:600;border-radius:7px;padding:5px 9px;cursor:pointer;border:1px solid #e6e5e1;background:#fff;color:#6b6b66}
.ac-mini.ac-primary{background:#2563eb;border-color:#2563eb;color:#fff}
.ac-mini.ac-done{background:#ecfdf3;border-color:#bbf7d0;color:#15803d}
.ac-empty{font-size:12px;color:#9b9b95;font-style:italic}
.ac-fabs{position:fixed;right:18px;bottom:18px;z-index:70;display:flex;flex-direction:column;gap:10px;font-family:Inter,system-ui,sans-serif}
.ac-fab{position:relative;width:46px;height:46px;border-radius:50%;border:1px solid rgba(230,229,225,.7);background:rgba(255,255,255,.58);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);color:#6b6b66;font-size:18px;line-height:1;cursor:pointer;box-shadow:0 6px 20px -8px rgba(40,40,30,.34);display:grid;place-items:center}
.ac-fab[aria-pressed=true]{background:rgba(37,99,235,.82);border-color:rgba(37,99,235,.82);color:#fff}
.ac-fab .ac-badge{position:absolute;top:-3px;right:-3px;background:#2563eb;color:#fff;font-size:9.5px;font-weight:700;font-style:normal;border-radius:9px;min-width:16px;height:16px;display:grid;place-items:center;padding:0 3px;border:2px solid #fff}
.ac-float{position:fixed;z-index:80;transform:translateX(-100%);display:none;font-family:Inter,system-ui,sans-serif}
.ac-float.ac-show{display:block}
.ac-float button{font:inherit;font-size:12px;font-weight:600;color:#fff;background:rgba(37,99,235,.86);backdrop-filter:blur(6px);-webkit-backdrop-filter:blur(6px);border:none;border-radius:8px;padding:7px 12px;cursor:pointer;box-shadow:0 4px 14px -4px rgba(37,99,235,.5)}
.ac-zoomable{cursor:zoom-in}
.ac-zoomable:hover{outline:2px solid rgba(37,99,235,.45);outline-offset:2px}
.ac-lb{position:fixed;inset:0;z-index:90;background:rgba(22,22,20,.74);backdrop-filter:blur(4px);-webkit-backdrop-filter:blur(4px);display:grid;place-items:center;pointer-events:auto;font-family:Inter,system-ui,sans-serif}
.ac-lb-stage{background:#fff;border-radius:12px;box-shadow:0 24px 70px -24px rgba(0,0,0,.6);padding:18px;max-width:min(1040px,72vw);max-height:88vh;overflow:auto}
.ac-lb-vwrap{position:relative;display:inline-block;line-height:0}
.ac-lb.ac-pinning .ac-lb-vwrap,.ac-lb.ac-pinning .ac-lb-vwrap *{cursor:crosshair}
.ac-lb-close{position:fixed;top:16px;right:18px;width:38px;height:38px;border-radius:50%;border:none;background:rgba(255,255,255,.92);color:#33332e;font-size:17px;cursor:pointer;display:grid;place-items:center;box-shadow:0 6px 18px -6px rgba(0,0,0,.5)}
.ac-lb-cmt{position:fixed;top:19px;right:66px;height:34px;padding:0 13px;border-radius:17px;border:none;background:rgba(255,255,255,.92);color:#33332e;font:600 12px Inter,system-ui,sans-serif;cursor:pointer;box-shadow:0 6px 18px -6px rgba(0,0,0,.5)}
.ac-lb-cmt.ac-on{background:#2563eb;color:#fff}
.ac-lb-hint{position:fixed;bottom:18px;left:50%;transform:translateX(-50%);background:rgba(0,0,0,.55);color:#fff;font-size:11.5px;padding:6px 12px;border-radius:8px;pointer-events:none}
.ac-lb-card{position:fixed;width:300px;max-height:76vh;overflow:auto;background:#fff;border-radius:12px;box-shadow:0 16px 44px -16px rgba(0,0,0,.5);padding:12px}
.ac-lb-pin{position:absolute;transform:translate(-50%,-100%);width:22px;height:28px;display:grid;place-items:center;pointer-events:auto;cursor:pointer}
.ac-lb-pin .ac-bub{width:22px;height:22px;border-radius:50% 50% 50% 2px;transform:rotate(45deg);background:#2563eb;display:grid;place-items:center;box-shadow:0 2px 6px rgba(37,99,235,.5)}
.ac-lb-pin.ac-draft .ac-bub{background:#1d4ed8;outline:3px solid #eff4ff}
.ac-lb-pin .ac-n{transform:rotate(-45deg);color:#fff;font-size:10px;font-weight:700}
.ac-toast{position:fixed;left:50%;bottom:24px;transform:translateX(-50%);z-index:95;background:rgba(22,22,20,.9);color:#fff;font:500 12.5px Inter,system-ui,sans-serif;padding:9px 15px;border-radius:9px;box-shadow:0 8px 24px -8px rgba(0,0,0,.5);opacity:0;transition:opacity .25s}
.ac-toast.ac-show{opacity:1}
.ac-min{position:fixed;pointer-events:auto;cursor:pointer;display:inline-flex;align-items:center;gap:3px;height:26px;padding:0 9px;border-radius:13px;background:rgba(255,255,255,.6);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(230,229,225,.8);box-shadow:0 4px 16px -8px rgba(40,40,30,.3);font-size:12px;line-height:1;color:#6b6b66;font-weight:600;white-space:nowrap}
.ac-min:hover{border-color:#c9c8c3;color:#33332e;box-shadow:0 7px 20px -8px rgba(40,40,30,.42)}
.ac-min .ac-min-n{font-size:11px;color:#9b9b95;font-weight:700}
.ac-min-btn{position:absolute;top:6px;right:8px;z-index:2;border:none;background:transparent;color:#b8b7b1;font-size:18px;line-height:1;cursor:pointer;padding:0 3px}
.ac-min-btn:hover{color:#33332e}
/* The first comment's hover actions share the top-right corner with the
   minimize "–". Shift THEM left — padding on the row can't, since the actions
   are absolutely positioned off the padding-box edge, which padding doesn't
   move — so both the actions and the "–" stay clickable. Only the first row
   reaches that corner. */
.ac-card.ac-active>.ac-cmt:first-of-type .ac-link{right:26px}
.ac-card.ac-active>.ac-cmt:first-of-type .ac-del{right:44px}
.ac-card.ac-active>.ac-cmt:first-of-type .ac-edit{right:62px}
/* Left-bias the document + toolbar content so the fixed comment column clears
   the text (see applyDocShift). The header BAR stays full-bleed; only its inner
   content shifts. --ac-shift is computed per doc/viewport. */
html.ac-doc-shift [data-arti-doc]{transform:translateX(calc(-1 * var(--ac-shift,0px)))}
html.ac-doc-shift [data-arti-topbar]>*:first-child{transform:translateX(calc(-1 * var(--ac-shift-tb,0px)))}
`;

interface Draft {
  type: "text" | "pin";
  anchor: Record<string, unknown>;
  markIds: string[];
  text?: string; // in-progress composer text, preserved across re-renders (e.g. resolving another thread)
}

export function mountCommentsOverlay(opts: OverlayOpts): () => void {
  const { container, artifactId, me } = opts;
  const api = opts.api ?? cookieApi;
  const allowPin = !!opts.allowPin && !!container;

  // Position everything below arti's sticky toolbar (if present) so the
  // cards / panel sit inside the document view, not over the header.
  // Full-page views have no toolbar → falls back to the top.
  const topGuard = (): number => {
    const tb = document.querySelector("[data-arti-topbar]");
    const b = tb ? tb.getBoundingClientRect().bottom : 0;
    return Math.max(12, b + 8);
  };

  // one-time style injection
  if (!document.getElementById("ac-style")) {
    const st = document.createElement("style");
    st.id = "ac-style";
    st.textContent = CSS;
    document.head.appendChild(st);
  }

  // DOM roots
  const layer = el("div", "ac-layer");
  const fabs = el("div", "ac-fabs");
  const floatEl = el("div", "ac-float");
  floatEl.innerHTML = `<button>💬 Comment</button>`;
  fabs.innerHTML =
    `<button class="ac-fab" data-fab="comments" aria-pressed="false" title="Show / hide comments"><span>💬</span><i class="ac-badge" data-badge>0</i></button>` +
    (allowPin ? `<button class="ac-fab" data-fab="pin" title="Pin a spot (HTML only)"><span>📍</span></button>` : "") +
    `<button class="ac-fab" data-fab="panel" title="All comments"><span>📋</span></button>`;
  // Append the floating chrome to <html>, NOT <body>: a served page whose
  // <body> carries a transform/filter/animation (e.g. `body{animation:…}` with
  // a translateY keyframe) makes <body> the containing block for our
  // position:fixed elements, so they'd anchor to the tall document instead of
  // the viewport. <html> escapes that.
  document.documentElement.append(layer, fabs, floatEl);

  let threads: ThreadDTO[] = [];
  let mode: "pin" | null = null;
  let active: string | null = null;
  let commentsOn = false; // start with comments hidden; the 💬 FAB toggles them on
  let panelOpen = false;
  let draft: Draft | null = null;
  let pendingRange: Range | null = null;
  let floatArmed = false; // a selection has a live Comment button that must track scroll
  let dead = false;
  const mediaCleanups: Array<() => void> = []; // undo the per-media zoom wiring on teardown
  let closeLightbox: (() => void) | null = null; // close + unbind an open lightbox (used by teardown)

  // ── per-thread "minimize" ───────────────────────────────────────────
  // A text thread can be tucked from its full margin card down to a tiny
  // marker (💬 n) in the same column, independent of the global 💬 show/hide.
  // The minimized-thread IDs persist per artifact in localStorage so a
  // tidied-up review view survives reloads. (In the sandboxed served-page
  // embed the document has an opaque origin, so localStorage throws — the
  // try/catch degrades to in-memory, i.e. no cross-reload persistence there.)
  // Only text threads minimize: pins already collapse to a map-pin and
  // doc-level threads have no card.
  const MIN_KEY = `arti-cmt-min:${artifactId}`;
  const minimized = new Set<string>(loadMinimized());
  function loadMinimized(): string[] {
    try {
      const v = JSON.parse(localStorage.getItem(MIN_KEY) || "[]");
      return Array.isArray(v) ? v.filter((x) => typeof x === "string") : [];
    } catch {
      return [];
    }
  }
  function saveMinimized() {
    try { localStorage.setItem(MIN_KEY, JSON.stringify([...minimized])); } catch { /* storage unavailable (opaque origin) — stay in-memory */ }
  }
  function setMinimized(id: string, on: boolean) {
    if (on) minimized.add(id); else minimized.delete(id);
    saveMinimized();
  }
  // Drop IDs that no longer map to an open text thread so localStorage doesn't
  // accumulate stale entries (thread resolved/deleted, or quote edited away).
  function pruneMinimized() {
    const keep = new Set(threads.filter((t) => t.anchor.type === "text" && t.status !== "resolved").map((t) => t.id));
    let changed = false;
    for (const id of [...minimized]) if (!keep.has(id)) { minimized.delete(id); changed = true; }
    if (changed) saveMinimized();
  }

  // ── anchoring helpers ───────────────────────────────────────────────
  const offsetOf = (node: Node, nodeOff: number): number => {
    if (!container) return 0;
    let off = 0;
    const w = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
    let n: Node | null;
    while ((n = w.nextNode())) {
      if (n === node) return off + nodeOff;
      off += (n.nodeValue || "").length;
    }
    return off;
  };
  const blockAtPoint = (x: number, y: number): HTMLElement | null => {
    if (!container) return null;
    let e = document.elementFromPoint(x, y) as HTMLElement | null;
    while (e && e.parentElement !== container) e = e.parentElement;
    return e && container.contains(e) ? e : null;
  };
  const blockIndex = (e: HTMLElement): number => (container ? Array.prototype.indexOf.call(container.children, e) : -1);

  const markFor = (id: string) =>
    container ? Array.from(container.querySelectorAll<HTMLElement>(`mark.ac-hl[data-ac-thread="${id}"]`)) : [];

  // Unwrap just one thread's <mark>s (the per-thread version of clearMarks),
  // restoring the original text so a partially-stripped highlight can be
  // cleanly re-seeded without double-wrapping.
  const unwrapThread = (id: string) =>
    markFor(id).forEach((m) => { const p = m.parentNode!; while (m.firstChild) p.insertBefore(m.firstChild, m); p.removeChild(m); p.normalize(); });

  // How many <mark> segments a thread's highlight had at its last successful
  // seed. A quote that spans inline markup produces several segments, so
  // maybeReseed compares against this to detect a PARTIALLY-stripped highlight
  // (some segments gone, some left) — not just a fully-missing one.
  const seededCount = new Map<string, number>();

  // wrap a range in marks (multi-node safe)
  function wrapRange(range: Range, id: string, draftCls = false): string[] {
    const picked: { n: Text; s: number; e: number }[] = [];
    const w = document.createTreeWalker(range.commonAncestorContainer, NodeFilter.SHOW_TEXT);
    let n: Node | null;
    while ((n = w.nextNode())) {
      if (!range.intersectsNode(n)) continue;
      const t = n as Text;
      let s = 0, e = t.nodeValue!.length;
      if (t === range.startContainer) s = range.startOffset;
      if (t === range.endContainer) e = range.endOffset;
      if (s < e) picked.push({ n: t, s, e });
    }
    if (!picked.length && range.startContainer.nodeType === 3) {
      picked.push({ n: range.startContainer as Text, s: range.startOffset, e: range.endOffset });
    }
    picked.forEach(({ n: t, s, e }) => {
      const r = document.createRange();
      r.setStart(t, s);
      r.setEnd(t, e);
      const m = document.createElement("mark");
      m.className = "ac-hl" + (draftCls ? " ac-draft" : "");
      m.dataset.acThread = id;
      try {
        r.surroundContents(m);
      } catch { /* boundary spanning element; skip */ }
    });
    return [id];
  }

  // Find the quote in the container and wrap it — matching ACROSS text nodes.
  // A selection that crosses inline markdown (code/bold/italic/links) yields a
  // quote spanning several DOM text nodes; `wrapRange` already handles that at
  // creation time, but re-seeding must too. (The previous version searched
  // `indexOf(quote)` within each text node in isolation, so a cross-node quote
  // could never be re-seeded: the highlight vanished on the first reseed and its
  // card floated to the bottom-right corner, unrecoverably.)
  function seedText(t: ThreadDTO) {
    if (!container) return;
    const quote = String(t.anchor.quote || "");
    if (!quote) return;
    const start = typeof t.anchor.start === "number" ? (t.anchor.start as number) : -1;

    // Concatenate every text node, remembering each node's global start offset,
    // so a match found in the joined string maps back to (node, localOffset).
    const nodes: { node: Text; at: number }[] = [];
    let full = "";
    const w = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
    let n: Node | null;
    while ((n = w.nextNode())) {
      nodes.push({ node: n as Text, at: full.length });
      full += n.nodeValue || "";
    }
    if (!nodes.length) return;

    // Occurrence of `quote` nearest to `start` (or the first when start unknown).
    // Content is immutable per version, so `start` is normally exact; nearest/
    // first keeps it robust if a client omits or mis-sends the offset.
    let hit = -1;
    let bestDist = Infinity;
    let from = 0;
    let idx: number;
    while ((idx = full.indexOf(quote, from)) >= 0) {
      const dist = start < 0 ? 0 : Math.abs(idx - start);
      if (dist < bestDist) {
        bestDist = dist;
        hit = idx;
      }
      from = idx + quote.length;
      if (start < 0) break; // first occurrence is enough
    }
    if (hit < 0) return;

    // Map a global offset back to the text node that contains it.
    const locate = (off: number): { node: Text; offset: number } => {
      let i = nodes.length - 1;
      while (i > 0 && nodes[i].at > off) i--;
      return { node: nodes[i].node, offset: off - nodes[i].at };
    };
    const a = locate(hit);
    const b = locate(hit + quote.length);
    const r = document.createRange();
    r.setStart(a.node, a.offset);
    r.setEnd(b.node, b.offset);
    wrapRange(r, t.id);
    // Record the ACTUAL number of <mark> segments created (wrapRange returns
    // just [id], not one entry per segment), so maybeReseed can detect a
    // partially-stripped multi-segment highlight. seedText's callers clear the
    // thread's marks first, so markFor counts only what we just created.
    seededCount.set(t.id, markFor(t.id).length);
  }
  // Unwrap highlight marks. By default the in-progress draft's highlight
  // (.ac-draft) is preserved: reseed() runs on every resolve/reopen/render, and
  // wiping the draft mark would strand the open composer card (its anchor gone,
  // it gets parked at the top of the gutter instead of beside its selection).
  // Teardown passes includeDraft to fully restore the original DOM.
  function clearMarks(includeDraft = false) {
    if (!container) return;
    container.querySelectorAll(includeDraft ? "mark.ac-hl" : "mark.ac-hl:not(.ac-draft)").forEach((m) => {
      const p = m.parentNode!;
      while (m.firstChild) p.insertBefore(m.firstChild, m);
      p.removeChild(m);
      p.normalize();
    });
  }
  // (Re)seed text highlights: open text threads always, plus the active one
  // even if resolved — so clicking a resolved comment re-highlights its spot.
  function reseed() {
    clearMarks();
    threads.filter((t) => t.anchor.type === "text" && (t.status !== "resolved" || t.id === active)).forEach(seedText);
  }

  // ── media (zoomable images / diagrams) ──────────────────────────────
  // Auto-detected poppable blocks: images, inline SVG, mermaid charts, plus any
  // explicit opt-in; opt out (or exclude a noisy ancestor) with data-arti-zoom="off".
  // Only the OUTERMOST match is kept (so a .mermaid wrapper wins over its child <svg>).
  const mediaSel = "img, svg, .mermaid, [data-arti-zoom]";
  const mediaEls = (): HTMLElement[] =>
    container
      ? Array.from(container.querySelectorAll<HTMLElement>(mediaSel))
          .filter((e) => e.getAttribute("data-arti-zoom") !== "off" && !e.closest("[data-arti-zoom='off']"))
          .filter((e, _, arr) => !arr.some((o) => o !== e && o.contains(e)))
          // Skip tiny inline icons (e.g. brand glyphs) unless explicitly opted in
          // with data-arti-zoom — they shouldn't become zoomable/pinnable.
          .filter((e) => {
            if (e.hasAttribute("data-arti-zoom")) return true;
            const r = e.getBoundingClientRect();
            return r.width >= 64 && r.height >= 64;
          })
      : [];

  // ── geometry for floating cards/pins ────────────────────────────────
  // Resolve the element a pin is anchored to: a media element (anchor.media =
  // index into mediaEls) or a top-level block (anchor.blockIndex).
  // `media` lets hot paths (place() on scroll/resize) pass a pre-computed list
  // instead of re-running querySelectorAll for every pin/badge.
  const pinEl = (anchor: Record<string, unknown>, media?: HTMLElement[]): HTMLElement | undefined =>
    typeof anchor.media === "number"
      ? (media ?? mediaEls())[anchor.media as number]
      : (container?.children[(anchor.blockIndex as number) ?? -1] as HTMLElement | undefined);
  // Screen position of a pin's tip, computed from its element + relative offset
  // (works even when the marker isn't rendered, e.g. a resolved pin).
  const pinPointOf = (anchor: Record<string, unknown>): { left: number; top: number } | null => {
    const b = pinEl(anchor);
    if (!b) return null;
    const r = b.getBoundingClientRect();
    return { left: r.left + (anchor.rx as number) * r.width, top: r.top + (anchor.ry as number) * r.height };
  };
  // Stable, doc-wide pin number by CREATION order (oldest = 1). Unlike indexing
  // the threads array (new pins unshift to the front), this never renumbers the
  // existing pins when a new one is added.
  const pinNo = (t: ThreadDTO): number =>
    threads
      .filter((x) => x.anchor.type === "pin" && x.status !== "resolved")
      .sort((a, b) => a.created_at.localeCompare(b.created_at))
      .findIndex((x) => x.id === t.id) + 1;

  // ── data ops ────────────────────────────────────────────────────────
  const find = (id: string) => threads.find((t) => t.id === id);

  async function load() {
    try {
      const r = await api.list(artifactId);
      if (dead) return;
      threads = r.threads;
      pruneMinimized();
      reseed();
      render();
      wireMedia();
      openFromHash();
    } catch {
      /* not authed / no access — leave overlay empty */
    }
  }

  // ── rendering ───────────────────────────────────────────────────────
  function displayName(email: string, authorName?: string): string {
    if (email === me?.email && me.name) return me.name;
    if (authorName) return authorName;
    const local = email.split("@")[0];
    return local.split(/[._-]/).map((p) => p.charAt(0).toUpperCase() + p.slice(1)).join(" ");
  }
  function initials(name: string): string {
    const parts = name.split(/\s+/).filter(Boolean);
    if (parts.length >= 2) return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
    if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
    return "?";
  }
  function pictureUrl(email: string, authorPicture?: string): string {
    if (email === me?.email && me.picture) return me.picture;
    return authorPicture || "";
  }
  // Deterministic tint per participant (hashed from their email, so the same
  // person keeps the same color everywhere), used behind the initials when
  // there's no photo — and as the fallback when a photo fails to load.
  const AV_PALETTE = ["#2563eb", "#7c3aed", "#db2777", "#dc2626", "#ea580c", "#d97706", "#ca8a04", "#16a34a", "#059669", "#0891b2", "#0284c7", "#4f46e5", "#9333ea", "#c026d3", "#e11d48", "#65a30d"];
  function avatarColor(key: string): string {
    let h = 0;
    for (let i = 0; i < key.length; i++) h = (h * 31 + key.charCodeAt(i)) >>> 0;
    return AV_PALETTE[h % AV_PALETTE.length];
  }
  function avatar(email: string, authorName?: string, authorPicture?: string) {
    const name = displayName(email, authorName);
    const color = avatarColor(email || name);
    const pic = pictureUrl(email, authorPicture);
    // Colored initials sit UNDERNEATH; the photo (if any) layers on top. Google
    // Workspace avatars (lh3.googleusercontent.com) return 403 when a referrer
    // is sent — which is why they recently stopped rendering — so force
    // referrerpolicy=no-referrer. If the image still fails, onerror strips it
    // and the initials show through. (onerror is also wired in JS after render,
    // via wireAvatars, for CSP-strict pages where inline handlers don't fire.)
    const img = pic
      ? `<img src="${AV(pic)}" alt="" referrerpolicy="no-referrer" loading="lazy" onerror="this.remove()">`
      : "";
    return `<div class="ac-av" style="background:${color}"><span>${AV(initials(name))}</span>${img}</div>`;
  }
  // Belt-and-suspenders for the inline onerror above: strip a broken avatar
  // photo so the colored initials underneath show instead of a broken glyph.
  function wireAvatars(root: ParentNode) {
    root.querySelectorAll<HTMLImageElement>(".ac-av img").forEach((im) => { im.onerror = () => im.remove(); });
  }
  function cmtHTML(c: CommentDTO, tid: string) {
    const name = displayName(c.author, c.author_name);
    const edited = c.edited_at ? ` <span class="ac-when">(edited)</span>` : "";
    const own = me && c.author === me.email;
    if (editing && editing.tid === tid && editing.cid === c.id) {
      return `<div class="ac-cmt">${avatar(c.author, c.author_name, c.author_picture)}<div style="flex:1;min-width:0"><div><span class="ac-who">${AV(name)}</span></div><textarea class="ac-edit-input" data-editinput="${tid}|${c.id}">${AV(c.body)}</textarea><div class="ac-edit-acts"><button class="ac-mini ac-primary" data-editsave="${tid}|${c.id}">Save</button><button class="ac-mini" data-editcancel="1">Cancel</button></div></div></div>`;
    }
    // You can only delete or edit your own comments — never anyone else's.
    // The permalink, by contrast, is available to anyone who can see the comment.
    const del = own ? `<button class="ac-del" data-delc="${tid}|${c.id}" title="delete your comment">✕</button>` : "";
    const edit = own ? `<button class="ac-edit" data-editc="${tid}|${c.id}" title="edit your comment">✎</button>` : "";
    const link = `<button class="ac-link" data-permalink="${c.id}" title="copy link to this comment">🔗</button>`;
    return `<div class="ac-cmt" data-cid="${c.id}">${avatar(c.author, c.author_name, c.author_picture)}<div><div><span class="ac-who">${AV(name)}</span>${edited}</div><div class="ac-text">${AV(c.body)}</div></div>${link}${edit}${del}</div>`;
  }
  function anchoredCard(t: ThreadDTO): string {
    const expanded = active === t.id;
    // Text threads no longer carry the excerpt/quote header — the highlight in
    // the prose (brighter when this thread is active) already shows what's being
    // discussed, so the card stays compact in both the small (collapsed) and
    // large (open) states. All that remains is a small "–" minimize control in
    // the top-right corner. Pins keep a tiny label since they have no highlight.
    const minBtn =
      t.anchor.type === "text" && t.status !== "resolved"
        ? `<button class="ac-min-btn" data-min="${t.id}" title="Minimize to margin" aria-label="Minimize comment">–</button>`
        : "";
    const header = t.anchor.type === "pin" ? `<span class="ac-chip">📍 pin</span>` : "";
    if (!expanded) {
      const c = t.comments[0];
      const body = c ? `<div class="ac-cmt">${avatar(c.author, c.author_name, c.author_picture)}<div><div class="ac-snip">${AV(c.body)}</div></div></div>` : `<div class="ac-empty">No comments</div>`;
      const more = t.comments.length > 1 ? `<div class="ac-more">+${t.comments.length - 1} more</div>` : "";
      return `<div class="ac-card${t.status === "resolved" ? " ac-resolved" : ""}" data-tid="${t.id}">${minBtn}${header}${body}${more}</div>`;
    }
    const cmts = t.comments.map((c) => cmtHTML(c, t.id)).join("");
    const acts =
      t.status === "resolved"
        ? `<div class="ac-acts"><button class="ac-mini" data-reopen="${t.id}">↩ Reopen</button></div>`
        : `<div class="ac-reply"><textarea rows="1" placeholder="Reply…" data-reply="${t.id}"></textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-send="${t.id}">Reply</button><button class="ac-mini ac-done" data-resolve="${t.id}">✓ Resolve</button></div>`;
    return `<div class="ac-card ac-active${t.status === "resolved" ? " ac-resolved" : ""}" data-tid="${t.id}">${minBtn}${header}${cmts}${acts}</div>`;
  }
  // Minimized text thread: a compact pill in the column; click restores the card.
  function minMarker(t: ThreadDTO): string {
    // Truncate BEFORE escaping: slicing after AV() could cut inside an entity
    // (e.g. `&quot;` → `&quot`), which the browser decodes back to `"` and that
    // would terminate the title attribute early and corrupt the element.
    const tip = t.comments[0] ? AV(t.comments[0].body.slice(0, 140)) : "";
    return `<div class="ac-min" data-mintid="${t.id}" title="${tip}">💬<span class="ac-min-n">${t.comments.length}</span></div>`;
  }
  function draftCard(): string {
    // Show the quoted snippet from the start (same chip as a saved card), not a
    // generic "new comment" — so you can see exactly what you're commenting on.
    const chip = draft!.type === "text"
      ? `<span class="ac-chip">💬 <span class="ac-q">“${AV(String(draft!.anchor.quote || ""))}”</span></span>`
      : `<span class="ac-chip">📍 new pin</span>`;
    return `<div class="ac-card ac-draft" data-draft="1">${chip}<div class="ac-reply"><textarea rows="1" placeholder="Write a comment…" data-draftinput autofocus>${AV(draft!.text || "")}</textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-draftsend>Comment</button><button class="ac-mini" data-draftcancel>Cancel</button></div></div>`;
  }
  function render() {
    // Recompute the left-bias on every render — INCLUDING the comments-hidden
    // early-return below and the post-load render() (comments start hidden), so
    // the shift lands as soon as threads/draft change even if place() (which
    // also calls this) is skipped. place() still covers scroll/resize/reflow.
    applyDocShift();
    const badge = fabs.querySelector("[data-badge]")!;
    const openCount = threads.filter((t) => t.anchor.type !== "doc" && t.status !== "resolved").length;
    badge.textContent = String(openCount);
    layer.classList.toggle("ac-hidden", !commentsOn);
    if (!commentsOn) {
      layer.innerHTML = "";
      setScrollPad(0); // no cards shown → drop the extra scroll area
      wireMarks(); // keep existing highlights clickable; clicking one turns comments on
      return;
    }
    if (panelOpen) {
      // panel takes over the margin (the only place resolved threads show)
      layer.innerHTML = panelHTML() + pinsHTML();
    } else {
      // Text threads always show in the right column — a collapsed card until
      // opened, or a tiny marker when the user has minimized that thread. Pin
      // threads show their card ONLY when toggled open (active) — it floats next
      // to the pin, not in the column. See place().
      const anchored = threads.filter((t) =>
        t.anchor.type === "text"
          ? t.status !== "resolved" || t.id === active
          : t.anchor.type === "pin"
            ? t.id === active
            : false,
      );
      const anchoredHTML = anchored
        .map((t) => (t.anchor.type === "text" && minimized.has(t.id) && t.id !== active ? minMarker(t) : anchoredCard(t)))
        .join("");
      layer.innerHTML = anchoredHTML + (draft ? draftCard() : "") + pinsHTML();
    }
    wire();
    wireMarks();
    wireAvatars(layer);
    place();
  }

  // All-comments panel — the only place resolved threads remain visible.
  function panelHTML(): string {
    const live = threads.filter((t) => t.anchor.type !== "doc");
    const open = live.filter((t) => t.status !== "resolved");
    const resolved = live.filter((t) => t.status === "resolved");
    const row = (t: ThreadDTO) => {
      const label = t.anchor.type === "pin" ? "📍 pin" : `💬 “${AV(String(t.anchor.quote || "")).slice(0, 28)}”`;
      const snip = t.comments[0] ? AV(t.comments[0].body) : "(no comments)";
      const acts = t.status === "resolved" ? `<button class="ac-mini" data-preopen="${t.id}">↩</button>` : "";
      return `<div class="ac-prow${t.status === "resolved" ? " ac-res" : ""}" data-pjump="${t.id}"><div class="ac-pbody"><div class="ac-pmeta">${label}</div><div class="ac-psnip">${snip}</div></div><div class="ac-pacts">${acts}</div></div>`;
    };
    return `<div class="ac-panel" data-panel="1"><h4>Open · ${open.length}</h4>${open.map(row).join("") || '<div class="ac-empty" style="padding:8px">none</div>'}` +
      (resolved.length ? `<h4>Resolved · ${resolved.length}</h4>${resolved.map(row).join("")}` : "") + `</div>`;
  }

  // Clicking a highlight opens its thread; also reflect active/resolved state.
  function wireMarks() {
    if (!container) return;
    container.querySelectorAll<HTMLElement>("mark.ac-hl").forEach((m) => {
      const id = m.dataset.acThread;
      const t = id ? find(id) : null;
      m.classList.toggle("ac-resolved", !!(t && t.status === "resolved"));
      m.classList.toggle("ac-active", !!id && active === id);
      m.onclick = (e) => {
        e.stopPropagation();
        if (!id) return;
        // Toggle: first click opens the thread. A second click on the already-
        // open thread closes it — tucking an open TEXT thread into the margin,
        // but only deactivating a resolved one (resolved threads have no card
        // and no marker, so they must not enter the minimized set). Either way
        // reseed() so a resolved thread's highlight is cleared and a minimized
        // thread's highlight is kept.
        if (active !== id) { openThread(id); return; }
        const t = find(id);
        if (t && t.anchor.type === "text" && t.status !== "resolved") setMinimized(id, true);
        active = null;
        reseed();
        render();
      };
    });
  }
  function pinsHTML(): string {
    // Resolved pins don't render a marker (no lingering gray pin).
    const pins = threads.filter((t) => t.anchor.type === "pin" && t.status !== "resolved");
    let s = pins
      .map((t, i) => `<div class="ac-pin${active === t.id ? " ac-active" : ""}" data-pintid="${t.id}" data-pinidx="${i}"><div class="ac-bub"><span class="ac-n">${pinNo(t)}</span></div></div>`)
      .join("");
    if (draft && draft.type === "pin") s += `<div class="ac-pin ac-draft" data-pindraft="1"><div class="ac-bub"><span class="ac-n">+</span></div></div>`;
    return s;
  }

  // ── media: click any image/diagram to enlarge; pin a precise spot to comment ──
  // Wires every detected media element once (idempotent via data-ac-zoom). Called
  // after load + on hydration so client-rendered diagrams (mermaid) get picked up.
  function wireMedia() {
    if (!container) return;
    mediaEls().forEach((m) => {
      if (m.dataset.acZoom === "1") return;
      m.dataset.acZoom = "1";
      m.classList.add("ac-zoomable");
      const onClick = (e: Event) => {
        if (mode === "pin") return; // pin mode drops an INLINE pin (container capture handles it)
        e.preventDefault();
        e.stopPropagation();
        openLightbox(m);
      };
      m.addEventListener("click", onClick);
      mediaCleanups.push(() => { m.removeEventListener("click", onClick); m.classList.remove("ac-zoomable"); delete m.dataset.acZoom; });
    });
  }

  // Enlarge a media element in a full-screen overlay. Existing pins for it are
  // drawn on the enlarged copy; clicking a spot records a precise normalized
  // (rx,ry) and opens the comment composer inline (reusing the pin-draft flow),
  // so the same coordinate shows both here and on the small inline diagram.
  function openLightbox(mediaEl: HTMLElement) {
    const idx = mediaEls().indexOf(mediaEl);
    if (idx < 0) return;
    const lb = el("div", "ac-lb");
    const stage = el("div", "ac-lb-stage"); // white card
    const vwrap = el("div", "ac-lb-vwrap");  // tight wrapper → pin %s map to the visual
    const clone = mediaEl.cloneNode(true) as HTMLElement;
    clone.classList.remove("ac-zoomable");
    delete clone.dataset.acZoom;
    vwrap.appendChild(clone);
    stage.appendChild(vwrap);

    // The element we measure (pin coords) + scale up: the inner <svg> for a
    // .mermaid wrapper, else the clone itself.
    const visual = (clone.matches("img,svg") ? clone : clone.querySelector<HTMLElement>("img,svg")) || clone;
    visual.removeAttribute("width");
    visual.removeAttribute("height");
    if (visual.tagName.toLowerCase() === "img") {
      visual.style.cssText += ";max-width:100%;max-height:80vh;width:auto;height:auto";
    } else { // vector / opt-in element — grow crisply to fill the card
      visual.style.cssText += ";max-width:none;width:min(1000px,66vw);height:auto;max-height:80vh";
    }

    const close = el("button", "ac-lb-close"); close.textContent = "✕"; close.setAttribute("aria-label", "Close");
    const hint = el("div", "ac-lb-hint"); hint.textContent = "Esc to close";
    lb.append(stage, close, hint);
    let cmtBtn: HTMLElement | null = null;
    if (allowPin) { cmtBtn = el("button", "ac-lb-cmt"); cmtBtn.textContent = "📍 Comment on a spot"; lb.appendChild(cmtBtn); }
    document.documentElement.appendChild(lb);
    stage.addEventListener("scroll", repositionCard); // keep the card on its pin
    window.addEventListener("resize", repositionCard);

    let pinning = false;
    function setPinning(on: boolean) {
      pinning = on && allowPin;
      lb.classList.toggle("ac-pinning", pinning);
      cmtBtn?.classList.toggle("ac-on", pinning);
      hint.textContent = pinning ? "Click a spot on the diagram · Esc to cancel" : "Esc to close";
    }
    const onEsc = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      if (card) { vwrap.querySelectorAll(".ac-lb-pin.ac-draft").forEach((p) => p.remove()); clearCard(); return; } // discard an unsaved draft/open card first
      if (pinning) { setPinning(false); return; } // then disarm pinning
      closeLb();
    };
    function closeLb() { lb.remove(); stage.removeEventListener("scroll", repositionCard); document.removeEventListener("keydown", onEsc, true); window.removeEventListener("resize", repositionCard); if (closeLightbox === closeLb) closeLightbox = null; }
    closeLightbox = closeLb; // teardown can detach this if a lightbox is open
    document.addEventListener("keydown", onEsc, true);
    close.onclick = closeLb;
    // One card at a time: a click anywhere off the card/pin/chrome dismisses the
    // open card (dropping an uncommitted draft); a bare backdrop click closes the
    // popup. Placement clicks stopPropagation so they don't trip this.
    lb.addEventListener("click", (e) => {
      const tgt = e.target as HTMLElement;
      if (tgt.closest(".ac-lb-card,.ac-lb-pin,.ac-lb-close,.ac-lb-cmt")) return;
      if (card) { vwrap.querySelectorAll(".ac-lb-pin.ac-draft").forEach((p) => p.remove()); clearCard(); return; }
      if (tgt === lb && !pinning) closeLb();
    });
    if (cmtBtn) cmtBtn.onclick = () => setPinning(!pinning);

    // existing pins on the enlarged copy — click one to read it on the right
    function drawPins() {
      vwrap.querySelectorAll(".ac-lb-pin:not(.ac-draft)").forEach((p) => p.remove());
      const open = threads.filter((t) => t.anchor.type === "pin" && t.status !== "resolved");
      open.filter((t) => t.anchor.media === idx).forEach((t) => {
        const pin = el("div", "ac-lb-pin");
        pin.innerHTML = `<div class="ac-bub"><span class="ac-n">${pinNo(t)}</span></div>`;
        pin.style.left = (t.anchor.rx as number) * 100 + "%";
        pin.style.top = (t.anchor.ry as number) * 100 + "%";
        pin.onclick = (e) => { e.stopPropagation(); showThreadCard(t, pin); };
        vwrap.appendChild(pin);
      });
    }
    drawPins();

    // pinning is EXPLICIT (the button) and STAYS ON until toggled off — a plain
    // click just views. Each placement drops any uncommitted draft and starts fresh.
    vwrap.addEventListener("click", (e) => {
      if (!pinning) return;
      if ((e.target as HTMLElement).closest(".ac-lb-pin")) return;
      e.stopPropagation(); // don't let the backdrop handler dismiss the card we open
      vwrap.querySelectorAll(".ac-lb-pin.ac-draft").forEach((p) => p.remove());
      // Normalize against the OUTER media node (clone) — the same frame inline
      // pins use via pinEl + the same box the lightbox pins render in (vwrap).
      const r = clone.getBoundingClientRect();
      const rx = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
      const ry = Math.max(0, Math.min(1, (e.clientY - r.top) / r.height));
      const dp = el("div", "ac-lb-pin ac-draft");
      dp.innerHTML = `<div class="ac-bub"><span class="ac-n">+</span></div>`;
      dp.style.left = rx * 100 + "%"; dp.style.top = ry * 100 + "%";
      vwrap.appendChild(dp);
      showDraftCard(rx, ry, dp);
    });

    // the comment control: one card at a time, anchored to the pin's
    // bottom-right; the popup stays open.
    let card: HTMLElement | null = null;
    let cardAnchor: HTMLElement | null = null; // the pin element the open card tracks
    const clearCard = () => { if (card) { card.remove(); card = null; } cardAnchor = null; };
    function repositionCard() { if (card && cardAnchor) positionCard(cardAnchor); }
    function positionCard(anchorEl: HTMLElement) {
      if (!card) return;
      const pr = anchorEl.getBoundingClientRect();
      const w = card.offsetWidth || 300, h = card.offsetHeight || 150;
      let left = pr.right + 8;
      if (left + w > window.innerWidth - 8) left = pr.left - w - 8; // flip left if no room
      left = Math.max(8, Math.min(left, window.innerWidth - w - 8));
      const top = Math.max(8, Math.min(pr.bottom + 6, window.innerHeight - h - 8));
      card.style.left = left + "px"; card.style.top = top + "px";
    }
    function showThreadCard(t: ThreadDTO, anchorEl: HTMLElement) {
      clearCard();
      vwrap.querySelectorAll(".ac-lb-pin.ac-draft").forEach((p) => p.remove());
      const num = pinNo(t); // universal, stable pin number across the doc
      card = el("div", "ac-lb-card");
      const cmts = t.comments.map((c) => `<div class="ac-cmt">${avatar(c.author, c.author_name, c.author_picture)}<div style="flex:1;min-width:0"><div><span class="ac-who">${AV(displayName(c.author, c.author_name))}</span></div><div class="ac-text">${AV(c.body)}</div></div></div>`).join("");
      card.innerHTML = `<span class="ac-chip">📍 pin ${num}</span>${cmts}<div class="ac-reply"><textarea rows="1" placeholder="Reply…" data-r></textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-rs>Reply</button><button class="ac-mini ac-done" data-rv>✓ Resolve</button></div>`;
      const inp = card.querySelector<HTMLTextAreaElement>("[data-r]")!;
      autosize(inp); inp.addEventListener("input", () => autosize(inp));
      const send = async () => { const v = inp.value.trim(); if (!v) return; try { const c = await api.reply(t.id, v); t.comments.push(c); showThreadCard(t, anchorEl); } catch { /* ignore */ } };
      card.querySelector("[data-rs]")!.addEventListener("click", send);
      inp.onkeydown = (e) => { const k = e as KeyboardEvent; if (k.key === "Enter" && !k.shiftKey) { k.preventDefault(); send(); } };
      card.querySelector("[data-rv]")!.addEventListener("click", async () => {
        try { await api.resolve(t.id); } catch { return; } // on failure leave the card open
        t.status = "resolved";
        if (active === t.id) active = null; // mirror doResolve so the inline card doesn't linger
        clearCard(); drawPins(); render();
      });
      lb.appendChild(card);
      wireAvatars(card);
      cardAnchor = anchorEl;
      positionCard(anchorEl);
    }
    function showDraftCard(rx: number, ry: number, dp: HTMLElement) {
      clearCard();
      card = el("div", "ac-lb-card");
      card.innerHTML = `<span class="ac-chip">📍 new pin</span><div class="ac-reply"><textarea rows="1" placeholder="Write a comment…" data-i></textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-c>Comment</button><button class="ac-mini" data-x>Cancel</button></div>`;
      const inp = card.querySelector<HTMLTextAreaElement>("[data-i]")!;
      let committing = false;
      const commit = async () => {
        if (committing) return; // guard against double-submit (rapid click / Enter)
        const v = inp.value.trim(); if (!v) return;
        committing = true;
        try {
          const t = await api.create(artifactId, { type: "pin", media: idx, rx, ry } as unknown as ThreadDTO["anchor"], v);
          threads.unshift(t);
          dp.remove(); clearCard(); drawPins();
          commentsOn = true; // else render() clears the layer and the new inline pin is hidden
          render(); // reflect the new pin on the inline diagram too
        } catch { committing = false; /* allow retry on failure */ }
      };
      card.querySelector("[data-c]")!.addEventListener("click", commit);
      inp.onkeydown = (e) => { const k = e as KeyboardEvent; if (k.key === "Enter" && !k.shiftKey) { k.preventDefault(); commit(); } };
      inp.addEventListener("input", () => autosize(inp));
      card.querySelector("[data-x]")!.addEventListener("click", () => { dp.remove(); clearCard(); });
      lb.appendChild(card);
      cardAnchor = dp;
      positionCard(dp);
      setTimeout(() => { inp.focus(); autosize(inp); }, 0);
    }
  }

  // ── document left-bias ──────────────────────────────────────────────
  // When a doc has text comments (which render as cards in the fixed right
  // column at CARD_RIGHT), slide the document AND the toolbar's inner content
  // left just enough that the card column clears the text — spending the empty
  // left margin instead of overlapping. The shift depends only on
  // (has-text-comments, viewport width), never on whether the panel is open, so
  // toggling comments never reflows the text. Comment-free docs — and the
  // full-page view, which has no toolbar — never shift. Cards themselves are
  // left untouched; the doc retreats from them.
  let docShift = 0;                // px the doc body is currently translated left by
  let tbShift = 0;                 // px the toolbar content is translated left by (≤ docShift; capped by the toolbar's own margin)
  let pendingTextCommit = false;   // a text comment is mid-create (draft cleared, thread not yet in `threads`) — keep the shift so it doesn't drop and snap back

  // How far the doc — and, separately, the toolbar content — should slide left.
  // The doc shifts by its OWN available left margin. The toolbar is always
  // rendered at the Wide width, so tying the doc to the toolbar's (usually much
  // smaller) margin wasted the doc's slack and refused to shift on any window
  // narrower than the toolbar (~1700px) — the common case. So the toolbar
  // instead FOLLOWS the doc, capped by its own margin, and so can never slide
  // under the sidebar; at Wide the two margins are equal, so they move in
  // lockstep and stay aligned.
  // Measure, then delegate: the arithmetic lives in commentsGeometry.ts, where
  // it is reachable from a test. This wrapper only decides WHETHER to shift and
  // reads the rects to feed it.
  function computeShift(): Shift {
    const none = { doc: 0, tb: 0 };
    if (!container) return none;
    const topbar = document.querySelector<HTMLElement>("[data-arti-topbar]");
    if (!topbar) return none; // in-app viewer only (the full-page view has no toolbar)
    // Shift for any text thread OR an in-progress text draft — the first
    // comment on a doc must clear the composer before it's ever committed.
    if (!threads.some((t) => t.anchor.type === "text") && draft?.type !== "text" && !pendingTextCommit) return none;
    const main = container.closest("main");
    const docRect = container.getBoundingClientRect();
    const inner = topbar.firstElementChild as HTMLElement | null;
    return computeShiftFrom({
      viewportWidth: window.innerWidth,
      mainLeft: main ? main.getBoundingClientRect().left : 0,
      docLeft: docRect.left,
      docRight: docRect.right,
      toolbarInnerLeft: inner ? inner.getBoundingClientRect().left : docRect.left,
      docShift,
      tbShift,
    });
  }

  // Recomputed cheaply on every place() (load / render / scroll / resize /
  // container reflow). Scroll doesn't change horizontal geometry, so this
  // no-ops during scroll; it only moves on resize, width toggle, or when the
  // first text comment lands on a doc.
  function applyDocShift() {
    // Never touch the shared documentElement after teardown: an async handler
    // (e.g. commitDraft) can resolve and call render()→applyDocShift() after
    // dispose() already cleared the shift, which would re-leak the class/var
    // onto the page (and any overlay that replaced this one).
    if (dead) return;
    const { doc, tb } = computeShift();
    if (doc === docShift && tb === tbShift) return;
    docShift = doc;
    tbShift = tb;
    const root = document.documentElement;
    root.style.setProperty("--ac-shift", doc + "px");
    root.style.setProperty("--ac-shift-tb", tb + "px");
    root.classList.toggle("ac-doc-shift", doc > 0);
  }

  // ── scroll-area padding ─────────────────────────────────────────────
  // Margin cards live in document space and are NOT clamped to the viewport, so
  // a card anchored near the end of the doc (or a tall/expanded one) can hang
  // below the last line of text — and, because the document itself isn't that
  // tall, there's no way to scroll down to read its bottom. We grow the scroll
  // area by appending a zero-width spacer to the doc body, sized so the lowest
  // card's bottom becomes reachable. It sits INSIDE the container (=== the
  // scroller's content in every view), so it extends whichever element actually
  // scrolls. Height is measured against the container's NATURAL bottom (minus
  // the spacer we already added), so it's stable and never feeds back on itself.
  let scrollPad = 0;
  let padEl: HTMLElement | null = null;
  function setScrollPad(px: number) {
    if (!container) return;
    px = Math.max(0, Math.round(px));
    if (px === scrollPad && (px === 0 || padEl?.parentNode === container)) return;
    scrollPad = px;
    if (px === 0) { padEl?.remove(); return; }
    if (!padEl) {
      padEl = el("div", "ac-scroll-pad");
      padEl.setAttribute("aria-hidden", "true");
      padEl.style.cssText = "width:1px;pointer-events:none";
    }
    if (padEl.parentNode !== container) container.appendChild(padEl);
    padEl.style.height = px + "px";
  }

  function place() {
    applyDocShift();
    const right = CARD_RIGHT, vw = window.innerWidth, vh = window.innerHeight, gap = 10;
    const top0 = topGuard();
    // panel pinned just below the toolbar
    const panel = layer.querySelector<HTMLElement>(".ac-panel");
    if (panel) { panel.style.top = top0 + "px"; panel.style.right = right + "px"; }
    const media = mediaEls(); // compute once per place(); reused by the pin loop
    // pins
    layer.querySelectorAll<HTMLElement>(".ac-pin").forEach((pin) => {
      let t: ThreadDTO | undefined;
      let anchor: Record<string, unknown> | undefined;
      if (pin.dataset.pintid) {
        t = find(pin.dataset.pintid);
        anchor = t?.anchor;
      } else if (draft && draft.type === "pin") anchor = draft.anchor;
      if (!anchor) return;
      const b = pinEl(anchor, media);
      if (!b) { pin.style.display = "none"; return; }
      pin.style.display = ""; // reset if an earlier pass hid it before the media existed
      const r = b.getBoundingClientRect();
      pin.style.left = r.left + (anchor.rx as number) * r.width + "px";
      pin.style.top = r.top + (anchor.ry as number) * r.height + "px";
    });
    const cards = Array.from(layer.querySelectorAll<HTMLElement>(".ac-card"));
    const isPinCard = (c: HTMLElement) =>
      c.dataset.draft ? draft?.type === "pin" : find(c.dataset.tid || "")?.anchor.type === "pin";

    // Pin cards float right next to their pin (bottom-right; flipped to the
    // left when there isn't room), never in the column with highlight cards.
    cards.filter(isPinCard).forEach((c) => {
      const anchor = c.dataset.draft ? draft!.anchor : find(c.dataset.tid!)!.anchor;
      const pt = pinPointOf(anchor);
      if (!pt) { c.style.display = "none"; return; }
      const w = c.offsetWidth || CARD_WIDTH, h = c.offsetHeight;
      let left = pt.left + 16;
      if (left + w > vw - 8) left = pt.left - w - 16; // flip to the pin's left
      left = Math.min(Math.max(8, left), vw - w - 8);
      const top = Math.min(Math.max(top0, pt.top + 8), vh - h - 12);
      c.style.left = left + "px";
      c.style.right = "auto";
      c.style.top = top + "px";
    });

    // Text-highlight cards live in DOCUMENT space and scroll with the page —
    // like margin notes that are part of the doc, sliding in and out of view as
    // you scroll. Each card's doc Y is its highlight's doc Y (top-aligned),
    // pushed down only enough to clear the card above it (anti-overlap stack).
    // Because doc Y is scroll-invariant, the set has ONE deterministic layout
    // per state (which thread is open / how tall each card is); scrolling just
    // re-projects it onto the fixed layer with `top = docY - scrollY`. There is
    // no viewport clamp — a card whose highlight has scrolled off scrolls off
    // with it. (`right`/`gap` from above.)
    // Union top of a column item's highlight marks. Only the top drives the
    // stack; text cards and minimized markers share ONE stack (both right-
    // aligned to the same edge) so a marker never collides with a neighbour.
    const colTop = (c: HTMLElement): number | null => {
      const marks = c.dataset.draft
        ? Array.from(container?.querySelectorAll<HTMLElement>("mark.ac-hl.ac-draft") ?? [])
        : c.dataset.tid ? markFor(c.dataset.tid)
          : c.dataset.mintid ? markFor(c.dataset.mintid) : [];
      if (!marks.length) return null;
      let t = Infinity;
      marks.forEach((m) => { t = Math.min(t, m.getBoundingClientRect().top); });
      return t;
    };
    const sY = window.scrollY;
    let prevDocBottom = -Infinity;
    let maxColBottom = -Infinity; // lowest card/bubble bottom (viewport px) → drives scroll padding
    const containerRight = container ? container.getBoundingClientRect().right : vw - right;
    const colEls = [
      ...cards.filter((c) => !isPinCard(c)),
      ...Array.from(layer.querySelectorAll<HTMLElement>(".ac-min")),
    ];
    colEls
      .map((c) => ({ c, top: colTop(c) }))
      .sort((a, b) => (a.top ?? 1e9) - (b.top ?? 1e9))
      .forEach(({ c, top }) => {
        if (top == null) {
          // No seeded highlight to anchor to (quote edited away, or not yet
          // hydrated). The in-progress draft and the actively-opened thread are
          // parked at the top of the gutter so they stay readable — opening an
          // orphaned thread from the all-comments panel must still show its
          // card, not close the panel onto nothing. Any other unanchored card
          // (or marker) just hides; it's still reachable from the panel.
          const parked = c.dataset.draft || c.dataset.tid === active;
          if (!parked) { c.style.display = "none"; return; }
          c.style.display = ""; c.style.right = right + "px"; c.style.left = "auto"; c.style.top = top0 + "px"; c.style.clipPath = "none";
          return;
        }
        c.style.display = "";
        // Measure height AFTER restoring display: an item hidden by a prior
        // place() pass (display:none) reports offsetHeight 0, which would make
        // prevDocBottom under-count and let the next one overlap.
        const h = c.offsetHeight;
        // Doc Y aligned to the highlight, pushed down only to clear the item
        // above; then projected onto the fixed layer. prevDocBottom is in doc
        // space so the stack is identical at every scroll position.
        const docY = Math.max(top + sY - 2, prevDocBottom + gap);
        prevDocBottom = docY + h;
        const vTop = docY - sY; // viewport top once projected
        if (c.dataset.mintid) {
          // Collapsed bubbles hug the text: park them just past the doc's
          // (possibly left-shifted) right edge — the left side of the comment
          // gutter — instead of way out at the card column.
          c.style.left = Math.round(Math.min(containerRight + 8, vw - (c.offsetWidth || 44) - 8)) + "px";
          c.style.right = "auto";
        } else {
          c.style.right = right + "px";
          c.style.left = "auto";
        }
        c.style.top = vTop + "px";
        maxColBottom = Math.max(maxColBottom, vTop + h);
        // Pass UNDER the sticky header rather than over it: clip away the part
        // that has scrolled above the header's bottom edge (top0).
        const clip = Math.max(0, top0 - vTop);
        c.style.clipPath = clip > 0 ? `inset(${clip}px 0 0 0)` : "none";
      });

    // Extend the scroll area so the lowest margin card/bubble is fully
    // reachable. Only while cards are shown in the column (not when hidden or
    // when the all-comments panel — which scrolls on its own — is open).
    if (container && commentsOn && !panelOpen && maxColBottom > -Infinity) {
      const natBottom = container.getBoundingClientRect().bottom - scrollPad;
      setScrollPad(maxColBottom - natBottom + 24);
    } else {
      setScrollPad(0);
    }
  }

  // ── interactions ────────────────────────────────────────────────────
  function openThread(id: string) {
    if (draft) return;
    editing = null;
    if (minimized.delete(id)) saveMinimized(); // opening a thread un-tucks it
    commentsOn = true;
    panelOpen = false;
    fabs.querySelector('[data-fab="comments"]')!.setAttribute("aria-pressed", "true");
    active = id;
    reseed(); // re-highlight the anchor even if this thread is resolved
    render();
    const t = find(id);
    const target = t?.anchor.type === "pin" ? pinEl(t.anchor) : markFor(id)[0];
    target?.scrollIntoView({ behavior: "smooth", block: "center" });
  }

  // ── permalinks ──────────────────────────────────────────────────────
  // A comment's permalink is the current page URL with a #comment-<id> hash;
  // openFromHash() (run after load) opens that comment, even when resolved.
  const permalinkFor = (cid: string) => location.origin + location.pathname + "#comment-" + cid;

  async function copyPermalink(cid: string, btn: HTMLElement) {
    try {
      await navigator.clipboard.writeText(permalinkFor(cid));
      const prev = btn.textContent;
      btn.textContent = "✓";
      btn.classList.add("ac-copied");
      setTimeout(() => { btn.textContent = prev; btn.classList.remove("ac-copied"); }, 1200);
    } catch {
      toast("Couldn’t copy link");
    }
  }

  // toast shows a transient message that fades out after ~3s.
  function toast(msg: string) {
    const t = document.createElement("div");
    t.className = "ac-toast";
    t.textContent = msg;
    document.body.appendChild(t);
    requestAnimationFrame(() => t.classList.add("ac-show"));
    setTimeout(() => {
      t.classList.remove("ac-show");
      setTimeout(() => t.remove(), 300);
    }, 3000);
  }

  // openFromHash honours a #comment-<id> deep link: it opens the comment's
  // thread (works for resolved threads too, since openThread reseeds + shows
  // an active resolved card) and briefly flashes the exact comment. A missing
  // comment (deleted) gets a transient toast and is otherwise a no-op. Only
  // text/pin comments render a card; doc-level comments have no overlay card,
  // so the link just lands on the document.
  //
  // One-shot: load() also runs on error-recovery resyncs, so guard against
  // re-snapping the user back to the hashed thread (and re-flashing) after
  // they've navigated elsewhere.
  let hashHandled = false;
  function openFromHash() {
    if (hashHandled) return;
    const m = location.hash.match(/^#comment-(.+)$/);
    if (!m) return;
    const cid = m[1];
    const t = threads.find((th) => th.comments.some((c) => c.id === cid));
    if (!t) {
      hashHandled = true; // comment is gone — don't re-toast on later resyncs
      toast("This comment no longer exists");
      return;
    }
    // openThread no-ops while a draft is in progress; don't burn the one-shot
    // until it can actually open, so a later load() still honours the link.
    if (draft) return;
    hashHandled = true;
    openThread(t.id);
    requestAnimationFrame(() => {
      const cmt = layer.querySelector<HTMLElement>(`.ac-cmt[data-cid="${cid}"]`);
      if (!cmt) return;
      cmt.classList.add("ac-flash");
      setTimeout(() => cmt.classList.remove("ac-flash"), 1700);
    });
  }

  // Grow a composer/reply textarea to fit its content (so long comments wrap and
  // expand instead of scrolling in a single line), capped by the CSS max-height.
  // Returns true if the height changed, so callers can re-run the stack layout.
  function autosize(ta: HTMLTextAreaElement): boolean {
    const before = ta.style.height;
    ta.style.height = "auto";
    ta.style.height = Math.min(ta.scrollHeight, 160) + "px";
    return ta.style.height !== before;
  }

  function wire() {
    layer.querySelectorAll<HTMLTextAreaElement>(".ac-reply textarea, .ac-edit-input").forEach((ta) => {
      autosize(ta);
      // As the card grows taller, restack the margin cards so they don't overlap.
      ta.addEventListener("input", () => { if (autosize(ta)) place(); });
    });
    layer.querySelectorAll<HTMLElement>(".ac-card").forEach((c) => {
      c.onclick = (e) => { if ((e.target as HTMLElement).closest("input,button,textarea")) return; if (editing) return; if (c.dataset.tid) openThread(c.dataset.tid); };
    });
    // "–" minimizes a text card down to its margin marker.
    layer.querySelectorAll<HTMLElement>("[data-min]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const id = b.dataset.min!;
      setMinimized(id, true);
      if (active === id) active = null;
      render();
    }));
    // Clicking a margin marker restores its compact card (does not open it).
    layer.querySelectorAll<HTMLElement>("[data-mintid]").forEach((m) => (m.onclick = (e) => {
      e.stopPropagation();
      setMinimized(m.dataset.mintid!, false);
      render();
    }));
    layer.querySelectorAll<HTMLElement>(".ac-pin[data-pintid]").forEach((p) => {
      // Clicking a pin toggles its comment box (open if closed, close if open).
      p.onclick = (e) => {
        e.stopPropagation();
        const id = p.dataset.pintid!;
        if (active === id) { active = null; render(); } else openThread(id);
      };
    });
    layer.querySelectorAll<HTMLElement>("[data-send]").forEach((b) => (b.onclick = () => doReply(b.dataset.send!)));
    layer.querySelectorAll<HTMLTextAreaElement>("[data-reply]").forEach((i) => (i.onkeydown = (e) => { const k = e as KeyboardEvent; if (k.key === "Enter" && !k.shiftKey) { k.preventDefault(); doReply(i.dataset.reply!); } }));
    layer.querySelectorAll<HTMLElement>("[data-resolve]").forEach((b) => (b.onclick = () => doResolve(b.dataset.resolve!, true)));
    layer.querySelectorAll<HTMLElement>("[data-reopen]").forEach((b) => (b.onclick = () => doResolve(b.dataset.reopen!, false)));
    const ds = layer.querySelector<HTMLElement>("[data-draftsend]");
    if (ds) ds.onclick = () => commitDraft();
    const dc = layer.querySelector<HTMLElement>("[data-draftcancel]");
    if (dc) dc.onclick = () => cancelDraft();
    const dInput = layer.querySelector<HTMLTextAreaElement>("[data-draftinput]");
    if (dInput) {
      dInput.oninput = () => { if (draft) draft.text = dInput.value; }; // survive a re-render mid-compose
      dInput.onkeydown = (e) => { const k = e as KeyboardEvent; if (k.key === "Enter" && !k.shiftKey) { k.preventDefault(); commitDraft(); } };
    }
    layer.querySelectorAll<HTMLElement>("[data-delc]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const [tid, cid] = b.dataset.delc!.split("|");
      doDeleteComment(tid, cid);
    }));
    layer.querySelectorAll<HTMLElement>("[data-editc]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const [tid, cid] = b.dataset.editc!.split("|");
      startEdit(tid, cid);
    }));
    layer.querySelectorAll<HTMLElement>("[data-editsave]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const [tid, cid] = b.dataset.editsave!.split("|");
      doSaveEdit(tid, cid);
    }));
    layer.querySelectorAll<HTMLElement>("[data-editcancel]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      cancelEdit();
    }));
    layer.querySelectorAll<HTMLTextAreaElement>("[data-editinput]").forEach((ta) => {
      ta.onkeydown = (e) => {
        if ((e as KeyboardEvent).key === "Enter" && !(e as KeyboardEvent).shiftKey) {
          e.preventDefault();
          const [tid, cid] = ta.dataset.editinput!.split("|");
          doSaveEdit(tid, cid);
        }
        if ((e as KeyboardEvent).key === "Escape") cancelEdit();
      };
    });
    layer.querySelectorAll<HTMLElement>("[data-permalink]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      copyPermalink(b.dataset.permalink!, b);
    }));
    layer.querySelectorAll<HTMLElement>("[data-pjump]").forEach((r) => (r.onclick = (e) => {
      if ((e.target as HTMLElement).closest("button")) return;
      panelOpen = false;
      openThread(r.dataset.pjump!);
    }));
    layer.querySelectorAll<HTMLElement>("[data-preopen]").forEach((b) => (b.onclick = (e) => { e.stopPropagation(); doResolve(b.dataset.preopen!, false); }));
    const af = layer.querySelector<HTMLElement>("[autofocus]");
    if (af) {
      af.focus();
      // After a re-render that preserved typed text, put the caret at the end
      // so the user keeps typing where they left off (not at the start).
      if (af instanceof HTMLTextAreaElement) af.setSelectionRange(af.value.length, af.value.length);
    }
  }

  let editing: { tid: string; cid: string } | null = null;

  function startEdit(tid: string, cid: string) {
    editing = { tid, cid };
    render();
    const ta = layer.querySelector<HTMLTextAreaElement>(`[data-editinput="${tid}|${cid}"]`);
    if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
  }
  function cancelEdit() {
    editing = null;
    render();
  }
  async function doSaveEdit(tid: string, cid: string) {
    const ta = layer.querySelector<HTMLTextAreaElement>(`[data-editinput="${tid}|${cid}"]`);
    const v = ta?.value.trim();
    if (!v) return;
    try {
      const updated = await api.edit(tid, cid, v);
      const t = find(tid);
      if (t) {
        const idx = t.comments.findIndex((c) => c.id === cid);
        if (idx >= 0) t.comments[idx] = updated;
      }
    } catch { /* ignore — resync */ await load(); }
    if (editing && editing.tid === tid && editing.cid === cid) editing = null;
    render();
  }

  async function doDeleteComment(tid: string, cid: string) {
    try { await api.del(tid, cid); } catch { /* ignore */ }
    await load();
  }

  async function doReply(id: string) {
    const inp = layer.querySelector<HTMLTextAreaElement>(`[data-reply="${id}"]`);
    const v = inp?.value.trim();
    if (!v) return;
    // Like commitDraft/doDeleteComment: handle API failure (network/401/403)
    // instead of leaking an unhandled rejection — resync from the server.
    try {
      const c = await api.reply(id, v);
      find(id)?.comments.push(c);
      render();
    } catch {
      await load();
    }
  }
  async function doResolve(id: string, resolve: boolean) {
    editing = null;
    try {
      if (resolve) await api.resolve(id); else await api.reopen(id);
    } catch {
      await load();
      return;
    }
    const t = find(id);
    if (t) t.status = resolve ? "resolved" : "open";
    if (resolve) {
      active = null;
      // A resolved thread shows neither a card nor a marker, so drop it from the
      // minimized set — otherwise a later Reopen would bring it back as a margin
      // marker instead of a normal collapsed card. (pruneMinimized also scrubs
      // this on the next load; this keeps the invariant true without a reload.)
      if (minimized.delete(id)) saveMinimized();
    }
    reseed();
    render();
  }

  function cancelDraft() {
    if (!draft) return;
    if (draft.type === "text") draft.markIds.forEach((id) => markFor(id).forEach((m) => { const p = m.parentNode!; while (m.firstChild) p.insertBefore(m.firstChild, m); p.removeChild(m); p.normalize(); }));
    draft = null;
    render();
  }
  async function commitDraft() {
    if (!draft) return;
    const inp = layer.querySelector<HTMLTextAreaElement>("[data-draftinput]");
    const v = inp?.value.trim();
    if (!v) return;
    const anchor = draft.anchor;
    const markIds = draft.markIds;
    draft = null;
    // Hold the left-bias across the create round-trip: the draft is cleared and
    // the thread isn't in `threads` yet, so without this a scroll/resize during
    // the await would recompute the shift to 0 and snap the doc back.
    if (anchor.type === "text") pendingTextCommit = true;
    try {
      const t = await api.create(artifactId, anchor as ThreadDTO["anchor"], v);
      threads.unshift(t);
      // retag the draft marks with the real thread id
      if (anchor.type === "text") {
        markIds.forEach((tmp) => container?.querySelectorAll<HTMLElement>(`mark.ac-hl[data-ac-thread="${tmp}"]`).forEach((m) => { m.dataset.acThread = t.id; m.classList.remove("ac-draft"); }));
        // Record the new thread's segment count (these retagged marks ARE its
        // highlight — seedText never ran for it) so maybeReseed can later repair
        // a partially-stripped multi-segment highlight on this fresh thread.
        seededCount.set(t.id, markFor(t.id).length);
      }
      active = t.id;
    } catch {
      // failed — drop draft marks
      markIds.forEach((tmp) => container?.querySelectorAll<HTMLElement>(`mark.ac-hl[data-ac-thread="${tmp}"]`).forEach((m) => { const p = m.parentNode!; while (m.firstChild) p.insertBefore(m.firstChild, m); p.removeChild(m); p.normalize(); }));
    }
    pendingTextCommit = false; // thread now in `threads` (or the create failed) — resume normal shift computation
    render();
  }

  // Dismiss the floating Comment button (selection cleared, mode changed, etc.).
  function hideFloat() { floatArmed = false; floatEl.classList.remove("ac-show"); }

  // Position the floating Comment button from its selection's CURRENT viewport
  // rect. The button is position:fixed, so it must be re-projected on every
  // scroll/resize or it stays frozen while the text scrolls away. Hidden while
  // the selection is scrolled out of view; re-shown when it scrolls back.
  function positionFloat() {
    if (!floatArmed || !pendingRange) return;
    const rect = pendingRange.getBoundingClientRect();
    const offscreen = rect.bottom < 4 || rect.top > window.innerHeight - 4;
    floatEl.classList.toggle("ac-show", !offscreen);
    if (offscreen) return;
    // Google-Docs-style: a compact Comment button at the BOTTOM-RIGHT of the
    // selection (right edge at the selection end via CSS translateX(-100%)).
    // Clamp the left so it stays on-screen for selections near the left edge.
    floatEl.style.left = Math.min(Math.max(rect.right, 128), window.innerWidth - 12) + "px";
    floatEl.style.top = (rect.bottom + 8) + "px";
  }

  // text selection → Comment button
  function onMouseUp() {
    setTimeout(() => {
      if (mode === "pin" || draft || !container) { hideFloat(); return; }
      const sel = window.getSelection();
      if (!sel || !sel.rangeCount || sel.isCollapsed) { hideFloat(); return; }
      const r = sel.getRangeAt(0);
      if (!container.contains(r.commonAncestorContainer)) { hideFloat(); return; }
      if ((r.startContainer.parentElement as HTMLElement)?.closest("mark.ac-hl")) return;
      pendingRange = r.cloneRange();
      floatArmed = true;
      positionFloat();
    }, 0);
  }
  (floatEl.firstChild as HTMLButtonElement).onclick = () => {
    if (!pendingRange || !container) return;
    const quote = pendingRange.toString().trim().slice(0, 160);
    const start = offsetOf(pendingRange.startContainer, pendingRange.startOffset);
    const tmp = "draft-" + Date.now();
    wrapRange(pendingRange, tmp, true);
    draft = { type: "text", anchor: { type: "text", quote, start }, markIds: [tmp] };
    window.getSelection()?.removeAllRanges();
    hideFloat();
    pendingRange = null;
    commentsOn = true;
    panelOpen = false;
    render();
  };

  // Arm/disarm pin mode in one place: keeps the FAB state, the crosshair
  // cursor, and the float button in sync.
  function setPinMode(on: boolean) {
    mode = on ? "pin" : null;
    fabs.querySelector('[data-fab="pin"]')?.setAttribute("aria-pressed", String(on));
    container?.classList.toggle("ac-pinning", on);
    if (on) hideFloat();
  }

  // Capture-phase so a click in pin mode is intercepted BEFORE the page's own
  // links/buttons act on it — otherwise clicking a clickable element would
  // navigate / toggle instead of dropping a pin.
  function onContainerClickCapture(e: MouseEvent) {
    if (mode !== "pin" || draft || !container) return;
    // The overlay's own chrome lives inside container (=== document.body for
    // injected HTML). Never treat a click on it as a pin placement — that's
    // what made the 📍 FAB and the draft's Cancel button drop stray pins.
    const tgt = e.target as HTMLElement | null;
    if (tgt && tgt.closest(".ac-fabs, .ac-layer, .ac-float")) return;
    // In pin mode this click only drops a pin: swallow it so the page doesn't
    // navigate or toggle underneath.
    e.preventDefault();
    e.stopPropagation();
    // If the click landed on a media element, anchor a MEDIA pin (the same kind
    // the lightbox creates) so inline and lightbox diagram pins are unified.
    const media = mediaEls();
    const mediaEl = tgt ? media.find((m) => m === tgt || m.contains(tgt)) : undefined;
    const target = mediaEl ?? blockAtPoint(e.clientX, e.clientY);
    if (!target) return;
    const r = target.getBoundingClientRect();
    const rx = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
    const ry = Math.max(0, Math.min(1, (e.clientY - r.top) / r.height));
    const anchor = mediaEl
      ? { type: "pin", media: media.indexOf(mediaEl), rx, ry }
      : { type: "pin", blockIndex: blockIndex(target), rx, ry };
    draft = { type: "pin", anchor, markIds: [] };
    setPinMode(false); // one pin per arming — also stops a later Cancel/Comment click re-placing
    commentsOn = true;
    panelOpen = false;
    render();
  }

  // fabs
  fabs.querySelector('[data-fab="comments"]')!.addEventListener("click", () => {
    commentsOn = !commentsOn;
    fabs.querySelector('[data-fab="comments"]')!.setAttribute("aria-pressed", String(commentsOn));
    render();
  });
  fabs.querySelector('[data-fab="pin"]')?.addEventListener("click", () => {
    setPinMode(mode !== "pin");
  });
  fabs.querySelector('[data-fab="panel"]')!.addEventListener("click", () => {
    panelOpen = !panelOpen;
    commentsOn = true;
    fabs.querySelector('[data-fab="panel"]')!.setAttribute("aria-pressed", String(panelOpen));
    render();
  });

  // ESC cancels an in-progress pin: an open draft first, otherwise disarm
  // pin mode (the crosshair) so a stray click won't drop a pin.
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key !== "Escape") return;
    if (draft) { cancelDraft(); return; }
    if (mode === "pin") setPinMode(false);
  };

  // A re-render that replaces the prose nodes — client hydration, the viewer's
  // Raw toggle, or any other content remount — wipes the <mark>s our text
  // highlights live in. (The width toggle is kept from doing this by memoizing
  // MarkdownBody, but this stays the general safety net.) Watch the container
  // for the overlay's lifetime and restore any highlight that was fully or
  // PARTIALLY stripped. We re-seed only the stale threads (never touching
  // intact highlights), which makes this idempotent AND
  // loop-safe with no arbitrary retry cap: seeding a findable quote restores its
  // marks (so the next pass sees it intact), and an anchor whose quote is no
  // longer in the page simply no-ops (seedText finds no range → no DOM change →
  // no re-fire). The old full-reseed()-with-cap approach gave up permanently
  // after 8 tries whenever a single quote was unfindable, which killed
  // re-seeding for every OTHER highlight on later re-renders.
  let reseedPending = false;
  function maybeReseed() {
    if (!container || dead) return;
    const expected = threads.filter((t) => t.anchor.type === "text" && (t.status !== "resolved" || t.id === active));
    if (!expected.length) return;
    // Per-thread current segment count. A thread is stale if it lost ALL its
    // marks (fully wiped — e.g. hydration/Raw re-render) OR fewer than it had at
    // its last successful seed (a partial strip of a multi-segment highlight,
    // which can happen when a served page re-renders only part of the quoted
    // range). Checking counts (not mere presence) catches the partial case.
    const counts = new Map<string, number>();
    container.querySelectorAll<HTMLElement>("mark.ac-hl").forEach((m) => {
      const id = m.dataset.acThread;
      if (id) counts.set(id, (counts.get(id) || 0) + 1);
    });
    const stale = expected.filter((t) => {
      const c = counts.get(t.id) || 0;
      const exp = seededCount.get(t.id);
      return c === 0 || (exp !== undefined && c < exp);
    });
    if (!stale.length) return; // all highlights intact
    // Clear any surviving fragments first so a partial highlight re-seeds
    // cleanly (no double-wrap); an unfindable quote no-ops in seedText, so this
    // never flickers a thread that can't be restored.
    stale.forEach((t) => { unwrapThread(t.id); seedText(t); });
    wireMarks(); // re-attach highlight click handlers to the restored marks
    place();
  }
  let reseedTimer: ReturnType<typeof setTimeout> | null = null;
  const reseedObserver = new MutationObserver(() => {
    if (reseedPending || dead) return;
    reseedPending = true;
    // wireMedia unconditionally (not gated by maybeReseed's text-thread early
    // returns) so a client-rendered diagram (e.g. mermaid) that appears after
    // load still gets its zoom/pin handler. The `dead` guard stops a debounced
    // batch that fires after teardown from re-adding media listeners the
    // dispose cleanup already removed.
    reseedTimer = setTimeout(() => { reseedPending = false; reseedTimer = null; if (dead) return; maybeReseed(); wireMedia(); }, 250);
  });

  // The viewer's width toggle (Wide/Medium/Narrow) changes the prose
  // container's width via a CSS class — an attribute change that fires no
  // scroll/resize/childList event, so neither the reseed observer above nor the
  // window scroll/resize handlers below catch it. But the reflow moves highlight
  // positions, so the margin cards/markers (positioned by place() off each
  // highlight's rect) would sit at stale offsets until the next scroll. A
  // ResizeObserver on the container fires exactly when its box changes (width
  // toggle, sidebar collapse, font/image load), so re-place then. rAF-coalesced
  // and `dead`-guarded; place() never changes the container's size, so no loop.
  let placePending = false;
  const sizeObserver = new ResizeObserver(() => {
    if (dead || placePending) return;
    placePending = true;
    requestAnimationFrame(() => { placePending = false; if (!dead) place(); });
  });

  // Clicking off an open PIN card hides it (pins are transient — re-open by
  // clicking the pin again). Highlight/text cards are unaffected: they stay.
  const onDocClick = (e: MouseEvent) => {
    if (!active) return;
    const t = find(active);
    if (!t || t.anchor.type !== "pin") return;
    if ((e.target as HTMLElement).closest(".ac-card,.ac-pin,.ac-fabs,.ac-float,.ac-panel,.ac-lb")) return;
    active = null;
    render();
  };
  const onScrollResize = () => { place(); positionFloat(); };
  document.addEventListener("mouseup", onMouseUp);
  document.addEventListener("click", onDocClick);
  document.addEventListener("keydown", onKeyDown);
  if (container) container.addEventListener("click", onContainerClickCapture, true);
  window.addEventListener("resize", onScrollResize, true);
  window.addEventListener("scroll", onScrollResize, true);

  load();
  if (container) {
    // Watch for the overlay's whole lifetime, not just an initial hydration
    // window: content re-renders that replace the prose nodes happen long after
    // mount too — the viewer's Raw toggle, and any re-render on a served page —
    // and each wipes our <mark> highlights, so we must re-seed whenever it
    // happens. (The width toggle is prevented from mutating the prose by
    // memoizing MarkdownBody, so it doesn't rely on this.) maybeReseed no-ops
    // when every expected mark is already present, so an idle observer is cheap;
    // teardown disconnects it.
    reseedObserver.observe(container, { childList: true, subtree: true });
    sizeObserver.observe(container); // re-place cards when the prose box reflows (width toggle, etc.)
  }

  return () => {
    dead = true;
    reseedObserver.disconnect();
    sizeObserver.disconnect();
    if (reseedTimer) clearTimeout(reseedTimer);
    document.removeEventListener("mouseup", onMouseUp);
    document.removeEventListener("click", onDocClick);
    document.removeEventListener("keydown", onKeyDown);
    if (container) container.removeEventListener("click", onContainerClickCapture, true);
    window.removeEventListener("resize", onScrollResize, true);
    window.removeEventListener("scroll", onScrollResize, true);
    mediaCleanups.forEach((fn) => fn());
    if (closeLightbox) closeLightbox(); // remove an open lightbox AND its Escape listener
    padEl?.remove(); // drop the scroll-area spacer we appended to the doc body
    document.documentElement.classList.remove("ac-doc-shift"); // undo the left-bias
    document.documentElement.style.removeProperty("--ac-shift");
    document.documentElement.style.removeProperty("--ac-shift-tb");
    clearMarks(true); // restore original DOM, including any in-progress draft highlight
    layer.remove();
    fabs.remove();
    floatEl.remove();
  };
}

function el(tag: string, cls: string): HTMLElement {
  const e = document.createElement(tag);
  e.className = cls;
  return e;
}
