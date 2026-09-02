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
  mentionPeople,
  grantReadAccess,
  type ThreadDTO,
  type CommentDTO,
} from "./arti";
import {
  computeShift as computeShiftFrom,
  minMarkerLeft,
  railTop,
  CARD_RIGHT,
  CARD_WIDTH,
  RAIL_RIGHT,
  RAIL_WIDTH,
  type Shift,
} from "./commentsGeometry";
import { createMentionMenu, mentionHTML, MENTION_CSS, type MentionResult } from "./mentionMenu";

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
  // people backs the composer's @-menu. Optional: a client that doesn't
  // implement it simply has no mention typeahead, and an address typed by hand
  // still notifies — the server parses the body either way.
  people?(artifactId: string, q: string): Promise<MentionResult>;
  // grantRead widens the doc's access by one address, for the "you mentioned
  // someone who can't read this" step. Absent on the embed surface, where an
  // ACL change is not something a served page may make on its viewer's behalf.
  grantRead?(artifactId: string, email: string): Promise<void>;
}

const cookieApi: CommentsApi = {
  list: listComments,
  create: createThread,
  reply: replyComment,
  resolve: resolveThread,
  reopen: reopenThread,
  del: apiDeleteComment,
  edit: apiEditComment,
  people: mentionPeople,
  grantRead: grantReadAccess,
};

export interface OverlayOpts {
  container: HTMLElement | null; // the rendered doc body to anchor into (null → nothing to anchor)
  artifactId: string;
  me: { email: string; name?: string; picture?: string; is_admin: boolean } | null;
  allowPin?: boolean; // point/pin comments are HTML-only
  api?: CommentsApi; // defaults to the cookie-based client
}

const AV = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c] as string));

// Comment bodies are rendered through this rather than AV directly, so an
// @-mention reads as one in the thread and not as a raw address. Display only:
// the stored body is the plain text the author typed, and the edit box still
// shows exactly that.
const BODY = (s: string) => mentionHTML(s, AV);

// One icon language for the card's top-right controls: paths drawn in a 24
// box, 1.6 stroke, round joins — so edit / delete / link / minimize match in
// weight and optical size instead of mixing emoji with text glyphs.
const ICON_PATHS = {
  edit: `<path d="M4.6 19.4h3.6L18.6 9a2.05 2.05 0 0 0-2.9-2.9L5.3 16.5v2.9Z"/><path d="M14.4 7.3l2.9 2.9"/>`,
  del: `<path d="M4.6 6.6h14.8"/><path d="M9.6 6.6V4.9h4.8v1.7"/><path d="M6.9 6.6l.75 12.1a1.25 1.25 0 0 0 1.25 1.15h6.2a1.25 1.25 0 0 0 1.25-1.15L18.1 6.6"/>`,
  link: `<path d="M10.3 13.7a3.75 3.75 0 0 0 5.3 0l2.4-2.4a3.75 3.75 0 0 0-5.3-5.3l-1.2 1.2"/><path d="M13.7 10.3a3.75 3.75 0 0 0-5.3 0L6 12.7a3.75 3.75 0 0 0 5.3 5.3l1.2-1.2"/>`,
  min: `<path d="M5.6 12h12.8"/>`,
  ok: `<path d="M5.4 12.6l4.3 4.3 8.9-9.5"/>`,
  // Shared by the rail's comments toggle, the minimized-thread marker and the
  // selection's floating Comment button — one bubble, drawn once. Its y values
  // are the drawn shape's, lifted 0.75 units from the obvious ones (12.5 /
  // 20.5): the tail hangs below the balloon, so the ink of the naive path
  // straddles y=12.75 rather than the box's own center, and every site that
  // pairs this glyph with text rendered it a fraction low.
  bubble: `<path d="M20 11.75a7.5 7.5 0 0 1-10.6 6.8L4.5 19.75l1.3-4.6A7.5 7.5 0 1 1 20 11.75Z"/>`,
  pin: `<path d="M12 21s6.2-6 6.2-10.2A6.2 6.2 0 0 0 5.8 10.8C5.8 15 12 21 12 21Z"/><circle cx="12" cy="10.6" r="2.1"/>`,
  list: `<path d="M5 7h14M5 12h14M5 17h9"/>`,
  // Six dots — the rail's drag handle. Drawn as zero-length round-capped
  // strokes so it inherits the same 1.6 stroke language as the rest.
  grip: `<path d="M9.5 7h.01M14.5 7h.01M9.5 12h.01M14.5 12h.01M9.5 17h.01M14.5 17h.01" stroke-width="2.4"/>`,
};
const icon = (k: keyof typeof ICON_PATHS) =>
  `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${ICON_PATHS[k]}</svg>`;

const CSS = `
/* The overlay is injected into WHATEVER page is being commented on — the Next
   app (Tailwind preflight → border-box) but also a served HTML artifact, which
   is arbitrary author markup with the CSS default of content-box. Every fixed
   width in here is written as a border-box total: .ac-card/.ac-panel are
   CARD_WIDTH *including* their padding and border, which is the width
   commentsGeometry.ts computes the doc shift from, and .ac-edit-input is
   width:100% *plus* padding, which under content-box grew 18px wider than the
   card's text column and hung its border out over the card's own. So the reset
   is not cosmetic — without it the card renders 26px wider than the shift
   compensates for and the composer spills past the border. Scoped to our own
   roots: the document's .ac-hl marks live in the host's DOM and are left alone. */
.ac-layer,.ac-fabs,.ac-float,.ac-lb,.ac-toast,
.ac-layer *,.ac-fabs *,.ac-float *,.ac-lb *,.ac-toast *{box-sizing:border-box}
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
/* One control language for every card chrome button (edit / delete / link /
   minimize): a 22px square hit target holding a 15px stroked icon, dim at
   rest and lit on hover. No accent colors — the corner shouldn't compete
   with the comment. */
.ac-ico{display:grid;place-items:center;width:22px;height:22px;padding:0;border:none;border-radius:6px;background:transparent;color:#b9b8b3;cursor:pointer;transition:color .12s,background-color .12s,opacity .12s}
.ac-ico svg{display:block;width:15px;height:15px}
.ac-ico:hover{color:#33332e;background:rgba(40,40,30,.07)}
.ac-ico:focus-visible{outline:2px solid rgba(40,40,30,.22);outline-offset:0}
.ac-ico.ac-copied{color:#4f7a63}
/* Per-comment actions sit as one row in the row's top-right corner. The FIRST
   comment's row hosts the card-level controls too (.ac-lead — see the block
   near .ac-card-acts), so a card has exactly one control cluster rather than
   two that merely line up. */
.ac-cmt-acts{position:absolute;top:-2px;right:0;display:flex;align-items:center;gap:1px;opacity:0;transition:opacity .12s}
.ac-cmt:hover .ac-cmt-acts,.ac-cmt-acts:focus-within{opacity:1}
/* The lead cluster carries always-visible card-level controls, so the cluster
   itself never fades — only its comment-level .ac-grp does. */
.ac-cmt-acts.ac-lead{opacity:1}
/* Keep a cluster up while the "copied" tick is showing, even if the pointer
   has already left (Safari doesn't focus a button on click). */
.ac-cmt-acts:has(.ac-copied),.ac-grp:has(.ac-copied){opacity:1}
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
.ac-panel .ac-pmeta,.ac-prow .ac-pmeta{display:flex;align-items:center;gap:4px}
.ac-pmeta svg{display:block;width:11px;height:11px;flex:0 0 11px}
.ac-prow .ac-psnip{font-size:12px;color:#33332e;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
.ac-prow .ac-pacts{display:flex;gap:4px;flex-shrink:0}
.ac-card.ac-active{border-color:#2563eb;box-shadow:0 0 0 3px #eff4ff,0 10px 30px -12px rgba(40,40,30,.35)}
.ac-card.ac-resolved{opacity:.6}
.ac-card.ac-draft{border-color:#2563eb;box-shadow:0 0 0 3px #eff4ff;cursor:default}
.ac-chip{display:inline-flex;align-items:center;gap:5px;font-size:10.5px;line-height:1.4;max-height:22px;font-weight:600;color:#6b6b66;background:#f6f5f2;border:1px solid #efeeea;border-radius:6px;padding:2px 7px;margin-bottom:8px;max-width:100%;overflow:hidden}
.ac-chip svg{display:block;width:12px;height:12px;flex:0 0 12px;color:#9b9b95}
.ac-chip .ac-q{color:#9b9b95;font-weight:500;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:170px}
/* Vertical rhythm inside a thread: each row gets air above its name and below
   its body, and consecutive messages get a little more so a multi-message
   thread reads as separate messages rather than one block. */
.ac-cmt{display:flex;gap:9px;margin:10px 0}
.ac-cmt+.ac-cmt{margin-top:12px}
.ac-av{position:relative;overflow:hidden;flex:0 0 24px;width:24px;height:24px;border-radius:50%;display:grid;place-items:center;font-size:10px;font-weight:700;color:#fff;background:#2563eb}
.ac-av img{position:absolute;inset:0;width:100%;height:100%;object-fit:cover}
.ac-who{font-size:12px;font-weight:600;color:#1c1c1a}
.ac-when{font-size:10.5px;color:#9b9b95;margin-left:6px}
.ac-text{font-size:12.5px;color:#33332e;margin-top:3px;white-space:pre-wrap;word-wrap:break-word}
/* The composer is a separate object from the discussion above it, so it sits
   further away than two messages sit from each other. */
.ac-reply{display:flex;margin-top:16px}
.ac-reply textarea{flex:1;min-width:0;font:inherit;font-size:12.5px;line-height:1.45;border:1px solid #e6e5e1;border-radius:8px;padding:7px 10px;background:#f6f5f2;resize:none;box-sizing:border-box;min-height:34px;max-height:160px;overflow-y:auto}
.ac-reply textarea:focus{outline:none;border-color:#2563eb;background:#fff}
/* No rule above the buttons — spacing alone separates them from the composer. */
.ac-acts{display:flex;gap:6px;margin-top:10px}
/* A reply/comment composer's button row is revealed only once the composer is
   engaged (focused, or holding text): see wireComposer. */
.ac-acts.ac-onfocus{display:none}
.ac-composing>.ac-acts.ac-onfocus{display:flex}
.ac-mini{font:inherit;font-size:11.5px;font-weight:600;border-radius:7px;padding:5px 9px;cursor:pointer;border:1px solid #e6e5e1;background:#fff;color:#6b6b66}
.ac-mini.ac-primary{background:#2563eb;border-color:#2563eb;color:#fff}
/* Nothing typed yet → the send button is inert AND looks it. */
.ac-mini:disabled{cursor:not-allowed}
.ac-mini.ac-primary:disabled{background:#e8e7e3;border-color:#e2e1dd;color:#a9a8a3;box-shadow:none}
.ac-empty{font-size:12px;color:#9b9b95;font-style:italic}
/* The comment rail: one 26px-wide segmented capsule pinned to the right edge,
   vertically centered. It replaces the old stack of 46px circles — same
   controls at a third of the mass, and it never collides with the card column
   (CARD_RIGHT=78) or with a served page's own bottom-right chrome. Segments
   are divided by hairlines so it reads as ONE object; the count is a quiet
   footer segment instead of a floating badge. */
.ac-fabs{position:fixed;right:${RAIL_RIGHT}px;top:50%;transform:translateY(-50%);z-index:70;display:flex;flex-direction:column;width:${RAIL_WIDTH}px;background:rgba(255,255,255,.72);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(230,229,225,.9);border-radius:13px;box-shadow:0 2px 10px -6px rgba(40,40,30,.4);overflow:hidden;font-family:Inter,system-ui,sans-serif}
.ac-fab{position:relative;width:100%;height:28px;padding:0;border:none;background:transparent;color:#9b9b95;cursor:pointer;display:grid;place-items:center;transition:color .12s,background-color .12s}
.ac-fab+.ac-fab{border-top:1px solid #efeeea}
.ac-fab svg{display:block;width:15px;height:15px}
.ac-fab:hover{color:#33332e;background:#f6f5f2}
.ac-fab:focus-visible{outline:2px solid rgba(37,99,235,.5);outline-offset:-2px}
.ac-fab[aria-pressed=true]{background:#2563eb;color:#fff}
/* Drag handle. The rail sits vertically centered by default, which is exactly
   where a comment chip or the page's own chrome may want to be — so the whole
   capsule can be pulled up or down and remembers where you left it (globally,
   across artifacts). touch-action:none so a touch drag moves the rail instead
   of scrolling the page. */
.ac-grip{width:100%;height:15px;flex:0 0 15px;padding:0;border:none;border-bottom:1px solid #efeeea;background:transparent;color:#cfcec9;display:grid;place-items:center;cursor:grab;touch-action:none;transition:color .12s,background-color .12s}
.ac-grip svg{display:block;width:13px;height:13px}
.ac-grip:hover{color:#6b6b66;background:#f6f5f2}
.ac-grip:focus-visible{outline:2px solid rgba(37,99,235,.5);outline-offset:-2px}
.ac-fabs.ac-dragging{user-select:none}
.ac-fabs.ac-dragging .ac-grip{cursor:grabbing;color:#33332e}
/* Thread count, shown only when there is one (JS toggles ac-has). */
.ac-badge{display:none;height:22px;font-size:9.5px;font-weight:700;font-style:normal;color:#9b9b95;background:#f6f5f2;border-top:1px solid #efeeea;place-items:center}
.ac-fabs.ac-has .ac-badge{display:grid}
.ac-float{position:fixed;z-index:80;transform:translateX(-100%);display:none;font-family:Inter,system-ui,sans-serif}
.ac-float.ac-show{display:block}
.ac-float button{font:inherit;font-size:12px;font-weight:600;display:inline-flex;align-items:center;gap:6px;color:#fff;background:rgba(37,99,235,.86);backdrop-filter:blur(6px);-webkit-backdrop-filter:blur(6px);border:none;border-radius:8px;padding:7px 12px;cursor:pointer;box-shadow:0 4px 14px -4px rgba(37,99,235,.5)}
/* Size the bubble like every other icon site — an inline \`icon()\` SVG carries
   no intrinsic width, so without this it collapses (Chromium) or renders at the
   300x150 replaced-element default (Firefox). 13px matches .ac-min, the other
   bubble-beside-text control. */
.ac-float svg{display:block;width:13px;height:13px}
.ac-zoomable{cursor:zoom-in}
.ac-zoomable:hover{outline:2px solid rgba(37,99,235,.45);outline-offset:2px}
.ac-lb{position:fixed;inset:0;z-index:90;background:rgba(22,22,20,.74);backdrop-filter:blur(4px);-webkit-backdrop-filter:blur(4px);display:grid;place-items:center;pointer-events:auto;font-family:Inter,system-ui,sans-serif}
.ac-lb-stage{background:#fff;border-radius:12px;box-shadow:0 24px 70px -24px rgba(0,0,0,.6);padding:18px;max-width:min(1040px,72vw);max-height:88vh;overflow:auto}
.ac-lb-vwrap{position:relative;display:inline-block;line-height:0}
.ac-lb.ac-pinning .ac-lb-vwrap,.ac-lb.ac-pinning .ac-lb-vwrap *{cursor:crosshair}
.ac-lb-close{position:fixed;top:16px;right:18px;width:38px;height:38px;border-radius:50%;border:none;background:rgba(255,255,255,.92);color:#33332e;font-size:17px;cursor:pointer;display:grid;place-items:center;box-shadow:0 6px 18px -6px rgba(0,0,0,.5)}
.ac-lb-cmt{position:fixed;top:19px;right:66px;height:34px;padding:0 13px;display:inline-flex;align-items:center;gap:6px;border-radius:17px;border:none;background:rgba(255,255,255,.92);color:#33332e;font:600 12px Inter,system-ui,sans-serif;cursor:pointer;box-shadow:0 6px 18px -6px rgba(0,0,0,.5)}
.ac-lb-cmt.ac-on{background:#2563eb;color:#fff}
.ac-lb-cmt svg{display:block;width:14px;height:14px}
.ac-lb-hint{position:fixed;bottom:18px;left:50%;transform:translateX(-50%);background:rgba(0,0,0,.55);color:#fff;font-size:11.5px;padding:6px 12px;border-radius:8px;pointer-events:none}
.ac-lb-card{position:fixed;width:300px;max-height:76vh;overflow:auto;background:#fff;border-radius:12px;box-shadow:0 16px 44px -16px rgba(0,0,0,.5);padding:12px}
.ac-lb-pin{position:absolute;transform:translate(-50%,-100%);width:22px;height:28px;display:grid;place-items:center;pointer-events:auto;cursor:pointer}
.ac-lb-pin .ac-bub{width:22px;height:22px;border-radius:50% 50% 50% 2px;transform:rotate(45deg);background:#2563eb;display:grid;place-items:center;box-shadow:0 2px 6px rgba(37,99,235,.5)}
.ac-lb-pin.ac-draft .ac-bub{background:#1d4ed8;outline:3px solid #eff4ff}
.ac-lb-pin .ac-n{transform:rotate(-45deg);color:#fff;font-size:10px;font-weight:700}
.ac-toast{position:fixed;left:50%;bottom:24px;transform:translateX(-50%);z-index:95;background:rgba(22,22,20,.9);color:#fff;font:500 12.5px Inter,system-ui,sans-serif;padding:9px 15px;border-radius:9px;box-shadow:0 8px 24px -8px rgba(0,0,0,.5);opacity:0;transition:opacity .25s}
.ac-toast.ac-show{opacity:1}
.ac-min{position:fixed;pointer-events:auto;cursor:pointer;display:inline-flex;align-items:center;gap:5px;height:26px;padding:0 8px;border-radius:13px;background:rgba(255,255,255,.6);backdrop-filter:blur(12px);-webkit-backdrop-filter:blur(12px);border:1px solid rgba(230,229,225,.8);box-shadow:0 4px 16px -8px rgba(40,40,30,.3);font-size:12px;line-height:1;color:#6b6b66;font-weight:600;white-space:nowrap}
.ac-min:hover{border-color:#c9c8c3;color:#33332e;box-shadow:0 7px 20px -8px rgba(40,40,30,.42)}
/* Optical centering, not geometric: a digit's ink is cap height only — all of
   its slack sits below the baseline — so centering the count's em box leaves
   the number riding ~1px above the bubble's ink. margin-top pushes it back
   (flex centering centers the MARGIN box, so N moves the content down by N/2);
   3px is the value to keep, not 1.6px: text paints on whole CSS pixels, so
   sub-pixel nudges round away to nothing, and 3px is the one in its landing
   band that puts the box on an integer offset (7.5 + 1.5 = 9) — which is why
   it holds at 1x, 2x and 3x instead of only at the DPR it was tuned on. */
.ac-min .ac-min-n{font-size:11px;color:#9b9b95;font-weight:700;margin-top:3px}
/* Same bubble as the rail's comments toggle, at the card-chrome icon size —
   the minimized thread reads as "this is a comment", in the same hand. */
.ac-min svg{display:block;width:13px;height:13px}
/* The card's ONE control cluster: the first comment's own actions (edit /
   delete / link), a hairline, then the card-level ones (resolve ✓, minimize –).
   Everything is a child of the same flex line, so the controls are aligned and
   evenly spaced by construction — two separately positioned clusters could only
   ever be *nearly* aligned, and read as two groups of chrome instead of one.
   Card-level controls are always visible (resolve is one click from any open
   state of the card); the comment-level .ac-grp fades in on card hover, like
   every other row's does.
   The cluster rides the FIRST COMMENT'S ROW (.ac-cmt-acts.ac-lead), not the
   card — anchoring it to the card meant hardcoding an offset to that row, which
   only held for text cards: a pin card renders an .ac-chip band first, so the
   controls landed on the chip instead of the comment they act on. On the row it
   aligns itself on every card shape.
   .ac-card-acts is the fallback for the two cards with no first-row cluster to
   ride: one whose first comment is mid-edit, and the lightbox pin card (whose
   comments carry no per-comment actions at all). */
.ac-card-acts{position:absolute;top:19px;right:12px;z-index:2;display:flex;align-items:center;gap:1px}
.ac-lb-card .ac-card-acts{top:14px}
.ac-grp{display:flex;align-items:center;gap:1px;opacity:0;transition:opacity .12s}
.ac-card:hover .ac-grp,.ac-grp:focus-within{opacity:1}
/* Hairline between the two groups — the same device the rail capsule uses to
   read as one segmented object. It belongs to the comment group, so it fades
   with it instead of floating beside the ✓ on its own. */
.ac-sep{flex:0 0 1px;width:1px;height:13px;margin:0 4px;background:#e6e5e1}
/* Resolve is the one chrome icon that carries a color — green on hover, so it
   reads as the affirmative action without shouting at rest. */
.ac-ico.ac-resolve:hover{color:#15803d;background:#ecfdf3}
/* Left-bias the document + toolbar content so the fixed comment column clears
   the text (see applyDocShift). The header BAR stays full-bleed; only its inner
   content shifts. --ac-shift is computed per doc/viewport. */
html.ac-doc-shift [data-arti-doc]{transform:translateX(calc(-1 * var(--ac-shift,0px)))}
html.ac-doc-shift [data-arti-topbar]>*:first-child{transform:translateX(calc(-1 * var(--ac-shift-tb,0px)))}
/* ── Dark host pages ────────────────────────────────────────────────────
   Everything above is drawn for a light page: translucent WHITE surfaces
   (.6 alpha over a backdrop blur) carrying near-black text. Injected into a
   dark served artifact, that card doesn't read as white — it reads as a flat
   mid-gray slab, because what the blur mixes into the white is the dark page
   behind it, and the ink on top loses most of its contrast.
   So the surfaces flip. Only color changes here: no geometry, opacity or
   transition is touched, so the two themes can't drift in layout — which
   matters because commentsGeometry.ts computes the document shift from
   CARD_WIDTH and would be wrong if a dark rule resized a box.
   The switch is the ac-dark class on <html>, set at mount from the HOST
   page's own background (see hostIsDark) rather than from
   prefers-color-scheme: the in-app viewer is light-only, so keying the
   overlay off the OS preference would darken these cards on a white page. A
   served page that honours prefers-color-scheme paints its own dark body,
   which hostIsDark reads directly. */
html.ac-dark .ac-card,html.ac-dark .ac-panel,html.ac-dark .ac-min{background:rgba(38,38,36,.72);border-color:rgba(255,255,255,.14)}
html.ac-dark .ac-card{box-shadow:0 6px 24px -10px rgba(0,0,0,.7)}
html.ac-dark .ac-panel{box-shadow:0 10px 34px -12px rgba(0,0,0,.75)}
html.ac-dark .ac-min{color:#bdbcb6;box-shadow:0 4px 16px -8px rgba(0,0,0,.6)}
html.ac-dark .ac-min:hover{border-color:rgba(255,255,255,.3);color:#f2f1ee;box-shadow:0 7px 20px -8px rgba(0,0,0,.75)}
html.ac-dark .ac-card.ac-active{border-color:#3b82f6;box-shadow:0 0 0 3px rgba(37,99,235,.4),0 10px 30px -12px rgba(0,0,0,.75)}
html.ac-dark .ac-card.ac-draft{border-color:#3b82f6;box-shadow:0 0 0 3px rgba(37,99,235,.4)}
html.ac-dark .ac-who{color:#f2f1ee}
html.ac-dark .ac-text,html.ac-dark .ac-prow .ac-psnip{color:#dcdbd6}
html.ac-dark .ac-prow:hover{background:rgba(255,255,255,.07)}
html.ac-dark .ac-chip{color:#bdbcb6;background:rgba(255,255,255,.07);border-color:rgba(255,255,255,.1)}
html.ac-dark .ac-reply textarea,html.ac-dark .ac-edit-input{color:#ececea;background:rgba(255,255,255,.06);border-color:rgba(255,255,255,.14)}
html.ac-dark .ac-reply textarea::placeholder{color:rgba(236,236,234,.42)}
html.ac-dark .ac-reply textarea:focus{border-color:#3b82f6;background:rgba(255,255,255,.1)}
html.ac-dark .ac-edit-input{border-color:#3b82f6}
html.ac-dark .ac-mini{color:#dcdbd6;background:rgba(255,255,255,.08);border-color:rgba(255,255,255,.14)}
html.ac-dark .ac-mini.ac-primary{background:#2563eb;border-color:#2563eb;color:#fff}
html.ac-dark .ac-mini.ac-primary:disabled{background:rgba(255,255,255,.09);border-color:rgba(255,255,255,.12);color:#75746f}
html.ac-dark .ac-ico{color:#8b8a85}
html.ac-dark .ac-ico:hover{color:#f2f1ee;background:rgba(255,255,255,.12)}
html.ac-dark .ac-ico:focus-visible{outline-color:rgba(255,255,255,.3)}
html.ac-dark .ac-ico.ac-copied{color:#6ee7a8}
html.ac-dark .ac-ico.ac-resolve:hover{color:#4ade80;background:rgba(34,197,94,.16)}
html.ac-dark .ac-sep{background:rgba(255,255,255,.16)}
html.ac-dark .ac-fabs{background:rgba(32,32,30,.78);border-color:rgba(255,255,255,.14)}
html.ac-dark .ac-fab+.ac-fab{border-top-color:rgba(255,255,255,.1)}
html.ac-dark .ac-fab:hover{color:#f2f1ee;background:rgba(255,255,255,.08)}
html.ac-dark .ac-grip{color:#6b6a66;border-bottom-color:rgba(255,255,255,.1)}
html.ac-dark .ac-grip:hover{color:#dcdbd6;background:rgba(255,255,255,.08)}
html.ac-dark .ac-fabs.ac-dragging .ac-grip{color:#f2f1ee}
html.ac-dark .ac-badge{background:rgba(255,255,255,.06);border-top-color:rgba(255,255,255,.1)}
html.ac-dark .ac-lb-stage{background:#1f1f1d}
html.ac-dark .ac-lb-card{background:#232321;box-shadow:0 16px 44px -16px rgba(0,0,0,.75)}
html.ac-dark .ac-lb-close,html.ac-dark .ac-lb-cmt{background:rgba(40,40,38,.92);color:#ececea}
html.ac-dark .ac-lb-cmt.ac-on{background:#2563eb;color:#fff}
/* The quote highlights live in the HOST page's DOM and inherit ITS text color,
   which on a dark page is near-white — unreadable once the active mark paints
   solid yellow behind it. Pin the ink instead of the background: the yellow is
   the overlay's one fixed signal color and reads the same on either theme. */
html.ac-dark .ac-hl.ac-active{color:#1c1c1a}
html.ac-dark .ac-cmt.ac-flash{animation-name:ac-flash-dark}
@keyframes ac-flash-dark{0%,25%{background:rgba(250,204,21,.24)}100%{background:transparent}}
`;

// Perceived brightness of an rgb()/rgba() computed color, 0 (black) to 1
// (white). Returns null when the color is absent or effectively transparent —
// i.e. when it tells us nothing about what will actually be painted there.
function brightness(color: string): number | null {
  const m = /^rgba?\(([^)]+)\)/.exec(color || "");
  if (!m) return null;
  const p = m[1].split(/[,/]+/).map((s) => parseFloat(s.trim()));
  if (p.length < 3 || p.slice(0, 3).some((n) => Number.isNaN(n))) return null;
  if (p.length > 3 && !Number.isNaN(p[3]) && p[3] < 0.5) return null; // see-through: not the real backdrop
  return (0.2126 * p[0] + 0.7152 * p[1] + 0.0722 * p[2]) / 255;
}

// Is the page this overlay was injected into a dark one? Asked of the page
// itself, in descending order of directness, because the overlay serves both
// arti's light-only viewer and arbitrary served HTML:
//   1. <body>'s own background — what a served markdown page and the app both set;
//   2. <html>'s, for a page that paints the canvas there instead;
//   3. <body>'s text color, for an app that backgrounds a full-bleed wrapper
//      but still sets light ink on the body;
//   4. the UA canvas, when nothing on the page has painted anything at all.
// Known gap: a page that leaves body and html fully transparent, inherits its
// text color, and paints dark only on an inner element stays on the light
// palette.
function hostIsDark(): boolean {
  for (const node of [document.body, document.documentElement]) {
    if (!node) continue;
    const bg = brightness(getComputedStyle(node).backgroundColor);
    if (bg !== null) return bg < 0.5;
  }
  if (document.body) {
    const ink = brightness(getComputedStyle(document.body).color);
    if (ink !== null && ink > 0.6) return true;
  }
  // What shows through an unpainted page is the UA canvas, and the OS
  // preference does NOT decide that on its own: a page that never declared
  // color-scheme keeps a white canvas on a dark machine. So the preference
  // only gets a vote once the page has asked for one, and a dark-only
  // declaration is already the answer whatever the machine prefers.
  const declared = (getComputedStyle(document.documentElement).colorScheme || "").toLowerCase();
  if (!/\bdark\b/.test(declared)) return false;
  if (!/\blight\b/.test(declared)) return true;
  try {
    return !!window.matchMedia?.("(prefers-color-scheme: dark)").matches;
  } catch {
    return false;
  }
}

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

  // The @-menu is one instance shared by every composer on the page: only one
  // box has the caret at a time, so a menu per textarea would be several
  // hidden dropdowns and several debounce timers for one visible thing.
  const mentions = api.people
    ? createMentionMenu({
        search: (q) => api.people!(artifactId, q),
        grantRead: api.grantRead ? (email) => api.grantRead!(artifactId, email) : undefined,
      })
    : null;

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
    st.textContent = CSS + MENTION_CSS;
    document.head.appendChild(st);
  }

  // Light or dark chrome, decided from the page we were injected into (see the
  // dark block in CSS). The flag rides <html> like ac-doc-shift does, so it
  // reaches every root we append there — the layer, the rail, the selection
  // button — plus the lightbox and toast, which are created later.
  const applyTheme = () => document.documentElement.classList.toggle("ac-dark", hostIsDark());
  applyTheme();
  // A served page's dark styles usually come from its own prefers-color-scheme
  // rules, so its background flips under us when the OS theme is switched.
  const scheme = typeof window.matchMedia === "function" ? window.matchMedia("(prefers-color-scheme: dark)") : null;
  scheme?.addEventListener?.("change", applyTheme);

  // DOM roots
  const layer = el("div", "ac-layer");
  const fabs = el("div", "ac-fabs");
  const floatEl = el("div", "ac-float");
  floatEl.innerHTML = `<button>${icon("bubble")}<span>Comment</span></button>`;
  fabs.innerHTML =
    `<button class="ac-grip" data-rail-grip type="button" title="Drag to move · double-click to recenter" aria-label="Move comment controls: drag, or focus and use the arrow keys; Enter recenters">${icon("grip")}</button>` +
    `<button class="ac-fab" data-fab="comments" aria-pressed="false" title="Show / hide comments" aria-label="Show or hide comments">${icon("bubble")}</button>` +
    (allowPin ? `<button class="ac-fab" data-fab="pin" title="Pin a spot (HTML only)" aria-label="Pin a spot">${icon("pin")}</button>` : "") +
    `<button class="ac-fab" data-fab="panel" title="All comments" aria-label="All comments">${icon("list")}</button>` +
    `<i class="ac-badge" data-badge title="Open threads">0</i>`;
  // Append the floating chrome to <html>, NOT <body>: a served page whose
  // <body> carries a transform/filter/animation (e.g. `body{animation:…}` with
  // a translateY keyframe) makes <body> the containing block for our
  // position:fixed elements, so they'd anchor to the tall document instead of
  // the viewport. <html> escapes that.
  document.documentElement.append(layer, fabs, floatEl);

  let threads: ThreadDTO[] = [];
  let mode: "pin" | null = null;
  let active: string | null = null;
  let commentsOn = false; // start with comments hidden; the rail's bubble toggles them on
  let panelOpen = false;
  // Remembered vertical position of the control rail (see applyRailY); null =
  // the CSS default of vertically centered. Declared with the rest of the
  // state because place() reads it on every scroll, which can run before the
  // rail's own setup block below.
  let railY: number | null = null;
  let draft: Draft | null = null;
  let pendingRange: Range | null = null;
  let floatArmed = false; // a selection has a live Comment button that must track scroll
  let dead = false;
  const mediaCleanups: Array<() => void> = []; // undo the per-media zoom wiring on teardown
  let closeLightbox: (() => void) | null = null; // close + unbind an open lightbox (used by teardown)
  // The open lightbox's Escape handler. It listens at document capture, which
  // the key shield (see shieldKey) short-circuits, so the shield re-delivers to
  // it through this handle. null whenever no lightbox is open.
  let lightboxKeydown: ((e: KeyboardEvent) => void) | null = null;

  // ── per-thread "minimize" ───────────────────────────────────────────
  // A text thread can be tucked from its full margin card down to a tiny
  // marker (bubble icon + n) in the same column, independent of the global 💬 show/hide.
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
  // The buttons a single comment offers, as a bare list — the caller decides
  // whether they live on the row (any comment but the first) or hoisted into
  // the card's corner strip (the first one). You can only delete or edit your
  // own comments — never anyone else's. The permalink, by contrast, is
  // available to anyone who can see the comment.
  function cmtActs(c: CommentDTO, tid: string): string {
    const own = me && c.author === me.email;
    const del = own ? `<button class="ac-ico ac-del" data-delc="${tid}|${c.id}" title="Delete your comment" aria-label="Delete comment">${icon("del")}</button>` : "";
    const edit = own ? `<button class="ac-ico ac-edit" data-editc="${tid}|${c.id}" title="Edit your comment" aria-label="Edit comment">${icon("edit")}</button>` : "";
    const link = `<button class="ac-ico ac-link" data-permalink="${c.id}" title="Copy link to this comment" aria-label="Copy link to comment">${icon("link")}</button>`;
    return `${edit}${del}${link}`;
  }
  // `lead` = this is the card's FIRST row, so its cluster also hosts the
  // card-level controls passed in (resolve / minimize): comment actions, a
  // hairline, then those. One flex line, so everything aligns by construction.
  // null = an ordinary row, which draws only its own actions.
  function cmtHTML(c: CommentDTO, tid: string, lead: string | null = null) {
    const name = displayName(c.author, c.author_name);
    const edited = c.edited_at ? ` <span class="ac-when">(edited)</span>` : "";
    if (editing && editing.tid === tid && editing.cid === c.id) {
      return `<div class="ac-cmt">${avatar(c.author, c.author_name, c.author_picture)}<div style="flex:1;min-width:0"><div><span class="ac-who">${AV(name)}</span></div><textarea class="ac-edit-input" data-editinput="${tid}|${c.id}">${AV(c.body)}</textarea><div class="ac-edit-acts"><button class="ac-mini ac-primary" data-editsave="${tid}|${c.id}">Save</button><button class="ac-mini" data-editcancel="1">Cancel</button></div></div></div>`;
    }
    const acts =
      lead === null
        ? `<div class="ac-cmt-acts">${cmtActs(c, tid)}</div>`
        : `<div class="ac-cmt-acts ac-lead"><div class="ac-grp">${cmtActs(c, tid)}${lead ? `<span class="ac-sep"></span>` : ""}</div>${lead}</div>`;
    return `<div class="ac-cmt" data-cid="${c.id}">${avatar(c.author, c.author_name, c.author_picture)}<div><div><span class="ac-who">${AV(name)}</span>${edited}</div><div class="ac-text">${BODY(c.body)}</div></div>${acts}</div>`;
  }
  // A thread's card has two rendered forms, and they differ by ONE thing: the
  // open ("full") form carries a reply composer, the collapsed ("concise") form
  // doesn't. Both show the WHOLE thread — a collapsed card is not a preview, so
  // there's no snippet clamp and no "+n more" to click through. The third state
  // of the cycle is the margin bubble (minMarker), which is the only form that
  // reduces the thread to a count.
  //
  // Text threads carry no excerpt/quote header — the highlight in the prose
  // (brighter when this thread is active) already shows what's being discussed.
  // Pins keep a tiny label since they have no highlight.
  function anchoredCard(t: ThreadDTO): string {
    const expanded = active === t.id;
    const canMin = t.anchor.type === "text" && t.status !== "resolved";
    // One control cluster per card, riding the first comment's row: that
    // comment's own actions, a hairline, then the card-level ones (resolve ✓,
    // then minimize –). Resolve lives here rather than in the composer's button
    // row so it's reachable from both the concise and the full card. A resolved
    // card offers neither — it has ↩ Reopen in its action row instead.
    const cardCtrls = [
      t.status !== "resolved"
        ? `<button class="ac-ico ac-resolve" data-resolve="${t.id}" title="Resolve thread" aria-label="Resolve thread">${icon("ok")}</button>`
        : "",
      canMin
        ? `<button class="ac-ico" data-min="${t.id}" title="Minimize to margin" aria-label="Minimize comment">${icon("min")}</button>`
        : "",
    ].join("");
    // The first row can't host the cluster while it's being edited — it shows
    // Save/Cancel instead of its actions — so that card falls back to a
    // card-anchored strip holding the card-level controls alone.
    const first = t.comments[0];
    const hoistFirst = !!first && !!cardCtrls && !(editing && editing.tid === t.id && editing.cid === first.id);
    // Rendered LAST so it doesn't become the card's `:first-of-type` div, which
    // the row-level rules key off.
    const strip = !hoistFirst && cardCtrls ? `<div class="ac-card-acts">${cardCtrls}</div>` : "";
    const resolvedCls = t.status === "resolved" ? " ac-resolved" : "";
    const header = t.anchor.type === "pin" ? `<span class="ac-chip">${icon("pin")} pin</span>` : "";
    const cmts = t.comments.length
      ? t.comments.map((c, i) => cmtHTML(c, t.id, i === 0 && hoistFirst ? cardCtrls : null)).join("")
      : `<div class="ac-empty">No comments</div>`;
    if (!expanded) return `<div class="ac-card${resolvedCls}" data-tid="${t.id}">${header}${cmts}${strip}</div>`;
    // ac-onfocus keeps the button row out of the card until the composer is
    // engaged (wireComposer), so an untouched full card is the concise card
    // plus an empty box — nothing to press yet.
    const acts =
      t.status === "resolved"
        ? `<div class="ac-acts"><button class="ac-mini" data-reopen="${t.id}">↩ Reopen</button></div>`
        : `<div class="ac-reply"><textarea rows="1" placeholder="Reply…" data-reply="${t.id}"></textarea></div><div class="ac-acts ac-onfocus"><button class="ac-mini ac-primary" data-send="${t.id}" disabled>Reply</button></div>`;
    return `<div class="ac-card ac-active${resolvedCls}" data-tid="${t.id}">${header}${cmts}${acts}${strip}</div>`;
  }
  // Minimized text thread: a compact pill in the column; click restores the card.
  function minMarker(t: ThreadDTO): string {
    // Truncate BEFORE escaping: slicing after AV() could cut inside an entity
    // (e.g. `&quot;` → `&quot`), which the browser decodes back to `"` and that
    // would terminate the title attribute early and corrupt the element.
    const tip = t.comments[0] ? AV(t.comments[0].body.slice(0, 140)) : "";
    return `<div class="ac-min" data-mintid="${t.id}" title="${tip}">${icon("bubble")}<span class="ac-min-n">${t.comments.length}</span></div>`;
  }
  function draftCard(): string {
    // Show the quoted snippet from the start (same chip as a saved card), not a
    // generic "new comment" — so you can see exactly what you're commenting on.
    const chip = draft!.type === "text"
      ? `<span class="ac-chip">${icon("bubble")}<span class="ac-q">“${AV(String(draft!.anchor.quote || ""))}”</span></span>`
      : `<span class="ac-chip">${icon("pin")} new pin</span>`;
    // The draft card is born focused, so its buttons show from the start — but
    // Comment stays disabled until there's something to post (wireComposer).
    const empty = !(draft!.text || "").trim();
    return `<div class="ac-card ac-draft ac-composing" data-draft="1">${chip}<div class="ac-reply"><textarea rows="1" placeholder="Write a comment…" data-draftinput autofocus>${AV(draft!.text || "")}</textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-draftsend${empty ? " disabled" : ""}>Comment</button><button class="ac-mini" data-draftcancel>Cancel</button></div></div>`;
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
    fabs.classList.toggle("ac-has", openCount > 0); // no threads → no count segment
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
      const label = t.anchor.type === "pin" ? `${icon("pin")} pin` : `${icon("bubble")} “${AV(String(t.anchor.quote || "")).slice(0, 28)}”`;
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
      if (m.matches(".mermaid[data-mermaid-placeholder]:not([data-mermaid-rendered])")) return;
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
    if (allowPin) { cmtBtn = el("button", "ac-lb-cmt"); cmtBtn.innerHTML = `${icon("pin")}<span>Comment on a spot</span>`; lb.appendChild(cmtBtn); }
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
    function closeLb() { lb.remove(); stage.removeEventListener("scroll", repositionCard); document.removeEventListener("keydown", onEsc, true); window.removeEventListener("resize", repositionCard); if (closeLightbox === closeLb) closeLightbox = null; if (lightboxKeydown === onEsc) lightboxKeydown = null; }
    closeLightbox = closeLb; // teardown can detach this if a lightbox is open
    lightboxKeydown = onEsc; // the key shield re-delivers Escape to it
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
      const cmts = t.comments.map((c) => `<div class="ac-cmt">${avatar(c.author, c.author_name, c.author_picture)}<div style="flex:1;min-width:0"><div><span class="ac-who">${AV(displayName(c.author, c.author_name))}</span></div><div class="ac-text">${BODY(c.body)}</div></div></div>`).join("");
      // Same control language as the margin card: resolve is a ✓ in the corner,
      // and Reply only appears (enabled) once the composer holds text.
      card.innerHTML = `<span class="ac-chip">${icon("pin")} pin ${num}</span>${cmts}<div class="ac-reply"><textarea rows="1" placeholder="Reply…" data-r></textarea></div><div class="ac-acts ac-onfocus"><button class="ac-mini ac-primary" data-rs disabled>Reply</button></div><div class="ac-card-acts"><button class="ac-ico ac-resolve" data-rv title="Resolve thread" aria-label="Resolve thread">${icon("ok")}</button></div>`;
      const inp = card.querySelector<HTMLTextAreaElement>("[data-r]")!;
      autosize(inp); inp.addEventListener("input", () => { if (autosize(inp)) repositionCard(); });
      // This card is clamped to its pin, not stacked in the margin — so the
      // button row's reveal must re-clamp it, not restack the hidden layer.
      wireComposer(inp, card, card.querySelector<HTMLButtonElement>("[data-rs]"), repositionCard);
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
      card.innerHTML = `<span class="ac-chip">${icon("pin")} new pin</span><div class="ac-reply"><textarea rows="1" placeholder="Write a comment…" data-i></textarea></div><div class="ac-acts"><button class="ac-mini ac-primary" data-c disabled>Comment</button><button class="ac-mini" data-x>Cancel</button></div>`;
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
      // Card is null: this draft's buttons stay put (Cancel must remain
      // reachable) — only Comment's disabled state tracks the text.
      wireComposer(inp, null, card.querySelector<HTMLButtonElement>("[data-c]"));
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
    applyRailY(); // re-clamp the (draggable) rail against the current viewport / header
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
          // gutter — instead of way out at the card column, and never further
          // right than the cards themselves (which would put them off-screen
          // under the rail on a full-width doc). See minMarkerLeft.
          c.style.left = minMarkerLeft({ containerRight, viewportWidth: vw, markerWidth: c.offsetWidth || 44 }) + "px";
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
    cancelCollapse(); // re-focusing a thread outranks a pending collapse of it
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
      // Swap the icon's markup, not textContent — the button holds an <svg>.
      const prev = btn.innerHTML;
      btn.innerHTML = icon("ok");
      btn.classList.add("ac-copied");
      setTimeout(() => { btn.innerHTML = prev; btn.classList.remove("ac-copied"); }, 1200);
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

  // How long a click on comment TEXT waits before collapsing the card, so a
  // double-click can claim it as a word selection instead. This must cover a
  // real OS double-click interval, not a snappy-feeling guess: macOS and
  // Windows both default to 500ms, and a shorter window collapses the card out
  // from under a deliberate, slightly slow double-click. (A previous overlay
  // change shipped 250ms here and that was exactly the bug.) Only clicks on
  // text pay this wait; anywhere else on the card collapses at once.
  const COLLAPSE_DELAY = 500;
  let collapseTimer: number | null = null;
  function cancelCollapse() {
    if (collapseTimer != null) { clearTimeout(collapseTimer); collapseTimer = null; }
  }
  // full → icon: tuck a text thread into the margin (pins and resolved threads
  // have no marker, so for them this just closes the card).
  function collapseThread(id: string) {
    const t = find(id);
    if (t && t.anchor.type === "text" && t.status !== "resolved") setMinimized(id, true);
    active = null;
    reseed();
    render();
  }

  // True when there's a live (non-empty) text selection sitting inside `el` —
  // used to tell "the user clicked the card" apart from "the user just finished
  // selecting some of its text".
  function selectionInside(el: HTMLElement): boolean {
    const sel = typeof window.getSelection === "function" ? window.getSelection() : null;
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) return false;
    const node = sel.getRangeAt(0).commonAncestorContainer;
    const host = node.nodeType === Node.ELEMENT_NODE ? (node as HTMLElement) : node.parentElement;
    return !!host && el.contains(host);
  }

  // Progressive disclosure for a composer: its button row appears only once the
  // box is engaged (focused, or already holding text), and the send button stays
  // disabled — and visibly gray — until there is something to send. `card` is
  // the element carrying `.ac-composing` (the card, or the lightbox pin card).
  // Blur with text keeps the row up, which is what lets the click that blurred
  // the box land on the button.
  // `reflow` re-lays-out whatever positions `card`, since showing/hiding the
  // button row changes its height: margin cards are stacked by place(), but the
  // lightbox pin card is clamped to its pin by repositionCard — calling place()
  // for that one would restack the margin layer (hidden behind the lightbox) and
  // leave the pin card itself unclamped, so it can grow off the viewport.
  function wireComposer(
    ta: HTMLTextAreaElement,
    card: HTMLElement | null,
    send: HTMLButtonElement | null,
    reflow: () => void = place,
  ) {
    const sync = () => {
      const has = ta.value.trim().length > 0;
      if (send) send.disabled = !has;
      const shown = has || document.activeElement === ta;
      if (card && card.classList.contains("ac-composing") !== shown) {
        card.classList.toggle("ac-composing", shown);
        reflow(); // the row appearing/vanishing changes the card's height
      }
    };
    ta.addEventListener("input", sync);
    ta.addEventListener("focus", sync);
    ta.addEventListener("blur", sync);
    sync();
    // Every composer goes through here — margin reply, draft card, and both
    // lightbox pin composers — so this is the one place the @-menu has to be
    // wired. The edit box is deliberately NOT a composer: editing a comment
    // fires no notification at all, so a mention added by editing would notify
    // nobody and an @-menu there would promise otherwise.
    mentions?.attach(ta);
  }

  function wire() {
    layer.querySelectorAll<HTMLTextAreaElement>(".ac-reply textarea, .ac-edit-input").forEach((ta) => {
      autosize(ta);
      // As the card grows taller, restack the margin cards so they don't overlap.
      ta.addEventListener("input", () => { if (autosize(ta)) place(); });
    });
    // Reply composers: buttons on engage, send disabled while empty.
    layer.querySelectorAll<HTMLTextAreaElement>("[data-reply]").forEach((ta) => {
      const card = ta.closest<HTMLElement>(".ac-card");
      wireComposer(ta, card, card?.querySelector<HTMLButtonElement>("[data-send]") ?? null);
    });
    // Clicking the body of a card (anywhere that isn't a control) walks the
    // thread around its three states:
    //   lean (collapsed card, no reply box) → full (open card) → icon (margin
    //   marker) → lean …
    // This handler supplies the first two steps; the [data-mintid] handler
    // below closes the cycle by restoring the lean card from the icon. Same
    // rule as clicking the highlight in the prose (see wireMarks). Threads
    // with no margin marker — pins, and resolved cards — just close, since
    // their pin/highlight already plays the "icon" role.
    layer.querySelectorAll<HTMLElement>(".ac-card").forEach((c) => {
      c.onclick = (e) => {
        // FIRST, before any early return: any further click inside this card —
        // on a control, mid-edit, or the second click of a double-click —
        // abandons a collapse that an earlier text click armed. Otherwise the
        // card could tuck itself away half a second into typing a reply.
        cancelCollapse();
        if ((e.target as HTMLElement).closest("input,button,textarea,a")) return;
        if (editing) return;
        // Don't cycle on the click that ends a text selection inside the card —
        // people copy comment text, and collapsing it mid-drag is maddening.
        if (selectionInside(c)) return;
        const id = c.dataset.tid;
        if (!id) return;
        if (active !== id) { openThread(id); return; }
        // Collapsing is the one step that takes the card away, and it competes
        // with double-click-to-select-a-word: the FIRST click of a double-click
        // carries no selection yet, so collapsing on it would eat the word you
        // were trying to select. A click on selectable comment text therefore
        // waits one double-click interval and re-checks; anywhere else on the
        // card (padding, chip, avatar, the "+n more" line) collapses at once.
        if ((e.target as HTMLElement).closest(".ac-text,.ac-who,.ac-when,.ac-q")) {
          collapseTimer = window.setTimeout(() => {
            collapseTimer = null;
            // Bail if the wait changed the answer: the thread moved on, the
            // overlay was torn down, or a word got selected. Re-query the card
            // rather than trusting the captured node — a re-render during the
            // wait replaces it, and a selection would then be inside the NEW
            // element while the old one is detached.
            if (dead || active !== id) return;
            const cur = layer.querySelector<HTMLElement>(`.ac-card[data-tid="${id}"]`);
            if (!cur || selectionInside(cur)) return;
            collapseThread(id);
          }, COLLAPSE_DELAY);
          return;
        }
        collapseThread(id);
      };
    });
    // "–" minimizes a text card down to its margin marker.
    layer.querySelectorAll<HTMLElement>("[data-min]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      const id = b.dataset.min!;
      setMinimized(id, true);
      if (active === id) active = null;
      render();
    }));
    // Clicking a margin marker restores its lean card (does not open it) —
    // the icon → lean step of the click cycle above.
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
    // ✓ in the card's corner strip — reachable from the concise and the full
    // card alike. stopPropagation so it doesn't also cycle the card's state.
    layer.querySelectorAll<HTMLElement>("[data-resolve]").forEach((b) => (b.onclick = (e) => {
      e.stopPropagation();
      doResolve(b.dataset.resolve!, true);
    }));
    layer.querySelectorAll<HTMLElement>("[data-reopen]").forEach((b) => (b.onclick = () => doResolve(b.dataset.reopen!, false)));
    const ds = layer.querySelector<HTMLElement>("[data-draftsend]");
    if (ds) ds.onclick = () => commitDraft();
    const dc = layer.querySelector<HTMLElement>("[data-draftcancel]");
    if (dc) dc.onclick = () => cancelDraft();
    const dInput = layer.querySelector<HTMLTextAreaElement>("[data-draftinput]");
    if (dInput) {
      // Card is null: a draft's buttons stay put (Cancel has to remain
      // reachable) — only Comment's disabled state tracks the text.
      wireComposer(dInput, null, layer.querySelector<HTMLButtonElement>("[data-draftsend]"));
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
    // what made the pin control and the draft's Cancel button drop stray pins.
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

  // ── the rail's vertical position ────────────────────────────────────
  // Remembered GLOBALLY (one key, not per artifact): where you like the
  // controls is a preference about your screen, not about a document. Stored
  // as the capsule's center as a share of viewport height so it survives a
  // resize or a different monitor; railTop() re-clamps it on every place().
  // null = the CSS default (vertically centered).
  const RAIL_KEY = "arti-cmt-rail-y";
  railY = loadRailY();
  function loadRailY(): number | null {
    try {
      const v = Number(localStorage.getItem(RAIL_KEY));
      return Number.isFinite(v) && v > 0 && v < 1 ? v : null;
    } catch {
      return null; // storage unavailable (opaque origin in the served-page embed)
    }
  }
  function saveRailY() {
    try {
      if (railY == null) localStorage.removeItem(RAIL_KEY);
      else localStorage.setItem(RAIL_KEY, String(railY));
    } catch { /* stay in-memory */ }
  }
  function applyRailY() {
    if (railY == null) { fabs.style.top = ""; fabs.style.transform = ""; return; }
    const top = railTop({
      viewportHeight: window.innerHeight,
      railHeight: fabs.offsetHeight || 100,
      minTop: topGuard(),
      fraction: railY,
    });
    fabs.style.top = top + "px";
    fabs.style.transform = "none"; // the default rule centers with translateY(-50%)
  }
  // Move the rail so its top lands at `top` (viewport px).
  //
  // The fraction is derived from the CLAMPED top, not the raw one: a drag can
  // run past the header or below the bottom edge, and storing that raw position
  // yields a fraction outside (0,1) — which loadRailY rejects, so the rail
  // would silently snap back to center on the next mount. Clamping here means
  // what you see after releasing is exactly what gets remembered.
  function setRailTop(top: number) {
    const h = fabs.offsetHeight || 100;
    const vh = window.innerHeight;
    const clamped = railTop({ viewportHeight: vh, railHeight: h, minTop: topGuard(), fraction: (top + h / 2) / vh });
    // Final belt-and-braces clamp: on a viewport shorter than the rail itself
    // even the clamped top can sit past the bottom, and a stored 0 or 1 is
    // indistinguishable from "never set".
    railY = Math.min(0.999, Math.max(0.001, (clamped + h / 2) / vh));
    applyRailY();
  }
  const grip = fabs.querySelector<HTMLElement>("[data-rail-grip]")!;
  let dragAbort: AbortController | null = null;
  grip.addEventListener("pointerdown", (e) => {
    e.preventDefault(); // don't start a text selection while dragging
    const startY = e.clientY;
    const startTop = fabs.getBoundingClientRect().top;
    dragAbort?.abort();
    const ac = new AbortController();
    dragAbort = ac;
    fabs.classList.add("ac-dragging");
    grip.setPointerCapture?.(e.pointerId);
    window.addEventListener("pointermove", (m: PointerEvent) => setRailTop(startTop + (m.clientY - startY)), { signal: ac.signal });
    const end = () => { fabs.classList.remove("ac-dragging"); saveRailY(); ac.abort(); if (dragAbort === ac) dragAbort = null; };
    window.addEventListener("pointerup", end, { signal: ac.signal });
    window.addEventListener("pointercancel", end, { signal: ac.signal });
  });
  const recenter = () => { railY = null; saveRailY(); applyRailY(); };
  // Double-click the grip → back to the vertically centered default.
  grip.addEventListener("dblclick", recenter);
  // Same, from the keyboard: Enter/Space on the focused handle. A pointer click
  // carries detail>=1, so this fires ONLY for keyboard activation — the click
  // that ends a mouse drag can't recenter the rail you just placed.
  grip.addEventListener("click", (e) => { if (e.detail === 0) recenter(); });
  // Arrow-key nudge — the keyboard equivalent of the drag. The listener is on
  // the HANDLE, so it only runs while the handle itself has focus (reachable by
  // Tab); anywhere else Up/Down scroll the page exactly as before. The
  // preventDefault suppresses the focused-element default (page scroll) for
  // those two keys only while the handle holds focus, which is the point — the
  // arrows are driving the rail then, not the page. Home/PageUp/etc. are left
  // alone so the usual scroll shortcuts still work even with the handle focused.
  // Assigned as an `onkeydown` PROPERTY, not via addEventListener: the key
  // shield (see shieldKey) stops overlay keystrokes before they reach their
  // target and re-delivers them through that property.
  grip.onkeydown = (e) => {
    if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
    e.preventDefault();
    const step = (e.shiftKey ? 64 : 16) * (e.key === "ArrowUp" ? -1 : 1);
    setRailTop(fabs.getBoundingClientRect().top + step);
    saveRailY();
  };
  applyRailY();

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
    reseedTimer = setTimeout(() => {
      reseedPending = false;
      reseedTimer = null;
      if (dead) return;
      maybeReseed();
      wireMedia();
      // Mermaid replaces its source <pre> asynchronously; highlights may stay
      // intact, but their cards and pins still need a fresh geometry pass.
      place();
    }, 250);
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
    if ((e.target as HTMLElement).closest(".ac-card,.ac-pin,.ac-fabs,.ac-float,.ac-panel,.ac-lb,.ac-mm")) return;
    active = null;
    render();
  };
  const onScrollResize = () => { place(); positionFloat(); };

  // ── keyboard isolation while the keyboard is aimed at the overlay ───
  // On a served HTML page the overlay is injected INTO the document it
  // comments on, so a keystroke typed into a comment box still travels through
  // whatever that page bound to document/window: a slide deck pages on Space,
  // an editor moves its cursor on ArrowUp/Down, and any page handler that
  // calls preventDefault eats the character before the textarea can insert it.
  // Whenever the event's target is overlay chrome, the page must not see the
  // key at all — the reader is commenting, not driving the page.
  //
  // The block runs at window CAPTURE because that is the first node in the
  // event path: a page handler can sit on window, document, body or an
  // element, in either phase, and stopping here precedes all of them. It uses
  // stopImmediatePropagation, not stopPropagation — the plain form only skips
  // the REST of the path, leaving any other listener on window/capture to run,
  // so a page that binds one after the overlay mounts would still page on
  // Space. The catch is that it also skips the overlay's OWN listeners further
  // down the path, so this handler re-delivers the event to each of them:
  //   - the open lightbox's Escape handler (document capture, consumes Esc),
  //   - the focused element's `on<type>` property — every overlay key handler
  //     is assigned that way (composer, reply, edit, pin-card inputs, and the
  //     rail grip) precisely so this re-delivery reaches it,
  //   - onKeyDown, the overlay's own document-level Escape handling.
  // preventDefault still works from a re-delivered handler: the event is mid
  // dispatch, so Enter-to-send suppresses its newline as before.
  //
  // Not covered: a page handler on `window` in capture registered BEFORE the
  // overlay mounts — it has already run by the time this one is called, and
  // nothing can pre-empt an earlier listener on the same node and phase. The
  // embed is a deferred script at the end of <body>, so it cannot get in front
  // of one. Everything else — window/capture bound later, and any handler on
  // document/body/element in either phase, which is the normal case and every
  // deck library we've seen — is fully covered.
  const CHROME_SEL = ".ac-layer,.ac-fabs,.ac-float,.ac-lb,.ac-mm";
  const inChrome = (n: EventTarget | null): boolean => {
    const el = n as Element | null;
    return !!el && typeof el.closest === "function" && !!el.closest(CHROME_SEL);
  };
  const shieldKey = (e: KeyboardEvent) => {
    if (dead || !inChrome(e.target)) return;
    e.stopImmediatePropagation();
    // The @-menu gets first refusal, HERE rather than in each composer's own
    // handler, because this is the only point every composer keystroke is
    // guaranteed to pass through — and the only one that precedes the two
    // handlers the shield re-delivers to. Consulted lower down, Escape closed
    // the menu and then reached `onKeyDown` (throwing away the draft behind it)
    // or, in the lightbox, never reached the menu at all because the Escape
    // branch below claims the key first and closes the card.
    if (e.type === "keydown" && mentions?.key(e)) return;
    if (e.type === "keydown" && lightboxKeydown && e.key === "Escape") {
      lightboxKeydown(e); // closes the open card / lightbox, and consumes the key
      return;
    }
    const t = e.target as HTMLElement & Record<string, unknown>;
    const own = t[`on${e.type}`];
    if (typeof own === "function") (own as (ev: KeyboardEvent) => void).call(t, e);
    if (e.type === "keydown") onKeyDown(e);
  };
  window.addEventListener("keydown", shieldKey, true);
  window.addEventListener("keypress", shieldKey, true);
  window.addEventListener("keyup", shieldKey, true);

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
    cancelCollapse();
    mentions?.destroy(); // the menu lives on document.body, outside the layer
    dragAbort?.abort(); // drop a rail drag still holding window listeners
    reseedObserver.disconnect();
    sizeObserver.disconnect();
    if (reseedTimer) clearTimeout(reseedTimer);
    document.removeEventListener("mouseup", onMouseUp);
    document.removeEventListener("click", onDocClick);
    document.removeEventListener("keydown", onKeyDown);
    window.removeEventListener("keydown", shieldKey, true);
    window.removeEventListener("keypress", shieldKey, true);
    window.removeEventListener("keyup", shieldKey, true);
    if (container) container.removeEventListener("click", onContainerClickCapture, true);
    window.removeEventListener("resize", onScrollResize, true);
    window.removeEventListener("scroll", onScrollResize, true);
    mediaCleanups.forEach((fn) => fn());
    if (closeLightbox) closeLightbox(); // remove an open lightbox AND its Escape listener
    padEl?.remove(); // drop the scroll-area spacer we appended to the doc body
    scheme?.removeEventListener?.("change", applyTheme);
    document.documentElement.classList.remove("ac-dark"); // undo the theme flag
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
