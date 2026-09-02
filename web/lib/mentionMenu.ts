// mentionMenu.ts — the "@" typeahead inside a comment composer.
//
// It is written against a bare <textarea> rather than as a React component
// because the comments overlay is deliberately outside React: it mounts into
// the Next app AND into sandboxed served-HTML pages, where there is no React
// runtime to render into. Everything here is DOM.
//
// Two rules the rest of the file exists to keep:
//
//   - The SERVER decides who may be mentioned. This menu renders what
//     GET /api/artifacts/{id}/comments/people returns and nothing else: which
//     addresses are offered, whether one is flagged as unable to read the doc,
//     and whether this caller may grant access at all (`can_grant`). It never
//     re-derives "am I the owner" from anything it happens to know locally.
//   - A mention is plain text. Selecting a suggestion inserts
//     "@address@example.com " into the body — no hidden markup, no parallel
//     recipient list. The body is what the server parses, so what the thread
//     reads is exactly who gets told.

export interface MentionCandidate {
  email: string;
  can_read: boolean;
}

export interface MentionResult {
  people: MentionCandidate[];
  can_grant: boolean;
  min_query: number;
}

export interface MentionSource {
  search(q: string): Promise<MentionResult>;
  // grantRead is absent on surfaces where widening access isn't offered (the
  // embed bundle). When absent, the menu never shows a grant affordance even
  // if the server were to report can_grant.
  grantRead?: (email: string) => Promise<void>;
}

// Longest "@…" run still treated as a mention in progress. Past this the user
// is writing prose, not picking a person, and firing a directory query on every
// keystroke of it is waste.
const MAX_QUERY = 64;

// Delay between the last keystroke and the lookup. Matches usePeopleSearch's
// debounce so the two typeaheads feel the same and hit the directory at the
// same rate.
const DEBOUNCE_MS = 160;

// mentionQueryAt finds the mention being typed at `caret`, if any.
//
// The word under the caret must contain an "@" whose preceding character can't
// be part of an address — that boundary is what tells a mention apart from an
// address someone is simply typing or quoting ("forwarded from bob@x.com" is
// not a mention of anybody), and it mirrors exactly the rule the server applies
// when it parses the stored body. The two must agree: a menu that fires where
// the server won't parse produces a mention that silently notifies nobody.
export function mentionQueryAt(value: string, caret: number): { start: number; query: string } | null {
  if (caret < 0 || caret > value.length) return null;
  let i = caret;
  while (i > 0 && !/\s/.test(value[i - 1])) i--;
  const at = value.slice(i, caret).indexOf("@");
  if (at < 0) return null;
  const start = i + at;
  const prev = start > 0 ? value[start - 1] : "";
  // Anything that could belong to a local part disqualifies it — including a
  // second "@", so the domain half of "@alice@example.com" never starts a
  // nested mention of its own.
  if (prev && /[A-Za-z0-9._%+\-@]/.test(prev)) return null;
  const query = value.slice(start + 1, caret);
  if (query.length > MAX_QUERY) return null;
  return { start, query };
}

// insertMention replaces the in-progress mention with the chosen address and
// returns the new value plus where the caret belongs. The trailing space is
// what ends the mention: without it the next keystroke extends the address and
// the token stops matching the person who was picked.
export function insertMention(value: string, start: number, caret: number, email: string): { value: string; caret: number } {
  const head = value.slice(0, start) + "@" + email + " ";
  return { value: head + value.slice(caret), caret: head.length };
}

// MENTION_RE matches an @-prefixed address in a posted body. It is the twin of
// mentionRe in internal/comments/mentions.go and must stay in step with it: the
// server's copy decides who is notified, this one decides who LOOKS notified.
// If the two disagree, a comment shows a highlighted mention that DM'd nobody.
const MENTION_RE = /@([A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)*\.[A-Za-z]{2,})/g;

// mentionHTML renders a comment body with its mentions marked up.
//
// It splits the RAW body and escapes each piece, never the other way round:
// escaping first and then cutting the string can split an entity ("&quot;") and
// spill a stray quote into the surrounding markup.
export function mentionHTML(body: string, escape: (s: string) => string): string {
  let out = "";
  let last = 0;
  MENTION_RE.lastIndex = 0;
  for (let m = MENTION_RE.exec(body); m; m = MENTION_RE.exec(body)) {
    const at = m.index;
    // Same boundary rule as the server: an address that is merely quoted
    // ("forwarded from bob@x.com") is not a mention and must not be dressed as
    // one.
    if (at > 0 && /[A-Za-z0-9._%+\-@]/.test(body[at - 1])) continue;
    out += escape(body.slice(last, at)) + '<span class="ac-mention">' + escape(m[0]) + "</span>";
    last = at + m[0].length;
  }
  return out + escape(body.slice(last));
}

const MENU_CLASS = "ac-mm";

export interface MentionMenu {
  // attach wires one composer. Safe to call repeatedly on the same element.
  attach(ta: HTMLTextAreaElement): void;
  // key gives the menu first refusal on a keystroke. Returns true when the menu
  // consumed it, in which case NOTHING else may act on that key: Enter picking
  // a suggestion must not also send the comment, and Escape closing the menu
  // must not also cancel the draft behind it. Call it from the earliest handler
  // a composer keystroke reaches, so "consumed" can actually be honoured.
  key(e: KeyboardEvent): boolean;
  destroy(): void;
}

export function createMentionMenu(source: MentionSource): MentionMenu {
  const root = document.createElement("div");
  root.className = MENU_CLASS;
  root.style.display = "none";
  // Keep focus in the textarea: a click that blurs the composer would close the
  // menu (and, in a draft card, tear the card down) before the click resolved.
  root.addEventListener("mousedown", (e) => e.preventDefault());
  document.body.appendChild(root);

  let ta: HTMLTextAreaElement | null = null;
  let anchor: { start: number; query: string } | null = null;
  let items: MentionCandidate[] = [];
  let canGrant = false;
  let index = 0;
  let pending: { email: string } | null = null; // the grant confirmation, if showing
  let seq = 0;
  let timer: number | null = null;

  const open = () => root.style.display !== "none";

  function hide(): boolean {
    const was = open();
    root.style.display = "none";
    root.innerHTML = "";
    items = [];
    anchor = null;
    pending = null;
    index = 0;
    // Bump the sequence so a lookup already in flight can't reopen the menu
    // after the user has dismissed it.
    seq++;
    if (timer != null) {
      clearTimeout(timer);
      timer = null;
    }
    return was;
  }

  function position() {
    if (!ta) return;
    const r = ta.getBoundingClientRect();
    root.style.left = Math.round(r.left) + "px";
    // Below the box by default; above it when there isn't room, so the menu is
    // never clipped by the viewport edge on a composer near the bottom.
    const h = root.offsetHeight || 0;
    const below = r.bottom + 4;
    root.style.top = (below + h > window.innerHeight - 8 ? Math.max(8, r.top - h - 4) : below) + "px";
    root.style.width = Math.max(220, Math.round(r.width)) + "px";
  }

  function draw() {
    if (!ta || !ta.isConnected) {
      hide();
      return;
    }
    if (pending) {
      // One explicit step, never implicit: picking somebody who can't read the
      // doc offers to widen access, it does not widen it.
      root.innerHTML = "";
      const q = document.createElement("div");
      q.className = "ac-mm-ask";
      q.textContent = `${pending.email} can't read this doc. Give them read access?`;
      const acts = document.createElement("div");
      acts.className = "ac-mm-acts";
      const grant = document.createElement("button");
      grant.type = "button";
      grant.className = "ac-mm-btn ac-mm-primary";
      grant.textContent = "Grant & mention";
      grant.addEventListener("click", () => void confirmGrant());
      const cancel = document.createElement("button");
      cancel.type = "button";
      cancel.className = "ac-mm-btn";
      cancel.textContent = "Cancel";
      cancel.addEventListener("click", () => hide());
      acts.append(grant, cancel);
      root.append(q, acts);
      root.style.display = "block";
      position();
      return;
    }
    if (!items.length) {
      hide();
      return;
    }
    root.innerHTML = "";
    items.forEach((p, i) => {
      const row = document.createElement("div");
      row.className = "ac-mm-row" + (i === index ? " ac-mm-on" : "") + (p.can_read ? "" : " ac-mm-noaccess");
      row.setAttribute("role", "option");
      row.setAttribute("aria-selected", i === index ? "true" : "false");
      const who = document.createElement("span");
      who.className = "ac-mm-email";
      who.textContent = p.email;
      row.appendChild(who);
      if (!p.can_read) {
        const tag = document.createElement("span");
        tag.className = "ac-mm-tag";
        tag.textContent = "no access";
        row.appendChild(tag);
      }
      row.addEventListener("mouseenter", () => {
        index = i;
        draw();
      });
      row.addEventListener("click", () => choose(i));
      root.appendChild(row);
    });
    root.style.display = "block";
    position();
  }

  function choose(i: number) {
    const p = items[i];
    if (!p || !ta || !anchor) return;
    if (!p.can_read) {
      // Only reachable when the server said this caller may grant AND the
      // surface offers it; otherwise no-access candidates are never listed.
      if (!canGrant || !source.grantRead) return;
      pending = { email: p.email };
      draw();
      return;
    }
    commit(p.email);
  }

  function commit(email: string) {
    if (!ta || !anchor) return;
    const box = ta;
    const { value, caret } = insertMention(box.value, anchor.start, box.selectionStart ?? box.value.length, email);
    box.value = value;
    box.setSelectionRange(caret, caret);
    hide();
    // The composer's own listeners drive autosize and the send button's enabled
    // state off `input`; setting .value in script fires nothing on its own.
    box.dispatchEvent(new Event("input", { bubbles: true }));
    box.focus();
  }

  async function confirmGrant() {
    const p = pending;
    if (!p || !source.grantRead) return;
    try {
      await source.grantRead(p.email);
    } catch {
      // The grant is the server's call and it can refuse (someone else changed
      // the ACL, or authority was lost between the menu opening and the click).
      // Say so and leave the mention unwritten rather than inserting one that
      // will notify nobody.
      if (pending) {
        root.innerHTML = "";
        const err = document.createElement("div");
        err.className = "ac-mm-ask";
        err.textContent = "Couldn't grant access — ask the doc's owner to share it.";
        root.appendChild(err);
        root.style.display = "block";
        position();
        window.setTimeout(() => hide(), 2600);
      }
      return;
    }
    const email = p.email;
    pending = null;
    commit(email);
  }

  function refresh() {
    // A composer is replaced wholesale whenever its card re-renders (the
    // overlay rebuilds cards from innerHTML), and a detached textarea can't
    // receive the mention we are about to write into it.
    if (!ta || !ta.isConnected) return hide();
    const box = ta;
    const found = mentionQueryAt(box.value, box.selectionStart ?? box.value.length);
    if (!found) return hide();
    // A grant confirmation is a modal step: keep it up while the caret sits in
    // the mention it belongs to rather than redrawing the list under it.
    if (pending) return;
    anchor = found;
    const mine = ++seq;
    if (timer != null) clearTimeout(timer);
    timer = window.setTimeout(() => {
      source
        .search(found.query)
        .then((res) => {
          // Stale-response guard, same shape as usePeopleSearch: only the
          // newest lookup may write, and only while the caret is still in the
          // mention that asked for it. Landing late otherwise puts one query's
          // people under a different query's caret — one Enter away from
          // mentioning the wrong person.
          if (mine !== seq || !ta) return;
          const now = mentionQueryAt(ta.value, ta.selectionStart ?? ta.value.length);
          if (!now || now.start !== found.start || now.query !== found.query) return;
          canGrant = res.can_grant && !!source.grantRead;
          items = (res.people ?? []).filter((p) => p.can_read || canGrant);
          index = 0;
          draw();
        })
        .catch(() => {
          // A directory that fails must not break the box it decorates: the
          // composer keeps working with no suggestions, exactly as before the
          // typeahead existed.
          if (mine === seq) hide();
        });
    }, DEBOUNCE_MS);
  }

  return {
    attach(box: HTMLTextAreaElement) {
      if (box.dataset.acMm === "1") return;
      box.dataset.acMm = "1";
      const onEdit = () => {
        ta = box;
        refresh();
      };
      box.addEventListener("input", onEdit);
      box.addEventListener("click", onEdit); // caret moved by mouse
      box.addEventListener("blur", () => {
        if (ta === box) hide();
      });
    },
    key(e: KeyboardEvent) {
      if (!open()) return false;
      if (pending) {
        // While the confirmation is up the only keys it owns are the two that
        // answer it. Everything else falls through to the composer, so typing
        // simply carries on and dismisses it via the next refresh.
        if (e.key === "Escape") {
          e.preventDefault();
          hide();
          return true;
        }
        if (e.key === "Enter") {
          e.preventDefault();
          void confirmGrant();
          return true;
        }
        return false;
      }
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          index = (index + 1) % items.length;
          draw();
          return true;
        case "ArrowUp":
          e.preventDefault();
          index = (index - 1 + items.length) % items.length;
          draw();
          return true;
        case "Enter":
        case "Tab":
          e.preventDefault();
          choose(index);
          return true;
        case "Escape":
          e.preventDefault();
          hide();
          return true;
        default:
          return false;
      }
    },
    destroy() {
      hide();
      root.remove();
    },
  };
}

// MENTION_CSS is appended to the overlay's own stylesheet so the menu is styled
// on every surface the overlay mounts on — including a served HTML page, which
// has none of the app's CSS.
export const MENTION_CSS = `
.ac-mm{position:fixed;z-index:2147483000;max-height:224px;overflow-y:auto;background:#fff;border:1px solid #e6e5e1;border-radius:10px;box-shadow:0 8px 28px -10px rgba(40,40,30,.3);padding:4px;font-family:Inter,system-ui,sans-serif;font-size:12.5px;color:#33332e}
.ac-mm-row{display:flex;align-items:center;gap:8px;padding:6px 8px;border-radius:7px;cursor:pointer;white-space:nowrap;overflow:hidden}
.ac-mm-row.ac-mm-on{background:rgba(37,99,235,.1)}
.ac-mm-email{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis}
.ac-mm-noaccess .ac-mm-email{color:#a3a29c}
.ac-mm-tag{flex:none;font-size:10.5px;letter-spacing:.02em;color:#a3a29c;border:1px solid #e6e5e1;border-radius:5px;padding:1px 5px}
.ac-mm-ask{padding:8px;line-height:1.45}
.ac-mm-acts{display:flex;gap:6px;padding:0 8px 8px}
.ac-mm-btn{font:inherit;font-size:12px;padding:4px 9px;border-radius:7px;border:1px solid #e6e5e1;background:#fff;color:#33332e;cursor:pointer}
.ac-mm-btn.ac-mm-primary{background:#2563eb;border-color:#2563eb;color:#fff}
/* A posted mention reads as a name, not as an address in the middle of a
   sentence — same weight as the surrounding text, tinted and lightly bedded. */
.ac-mention{color:#2563eb;background:rgba(37,99,235,.08);border-radius:4px;padding:0 2px}
html.ac-dark .ac-mm{background:#1e1e1c;border-color:rgba(255,255,255,.14);color:#ececea}
html.ac-dark .ac-mm-row.ac-mm-on{background:rgba(59,130,246,.22)}
html.ac-dark .ac-mm-noaccess .ac-mm-email,html.ac-dark .ac-mm-tag{color:rgba(236,236,234,.5)}
html.ac-dark .ac-mm-tag,html.ac-dark .ac-mm-btn{border-color:rgba(255,255,255,.14)}
html.ac-dark .ac-mm-btn{background:transparent;color:#ececea}
html.ac-dark .ac-mm-btn.ac-mm-primary{background:#3b82f6;border-color:#3b82f6;color:#fff}
html.ac-dark .ac-mention{color:#93b8fb;background:rgba(59,130,246,.16)}
`;
