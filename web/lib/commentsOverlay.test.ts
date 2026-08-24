// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { mountCommentsOverlay, type CommentsApi } from "./commentsOverlay";
import type { ThreadDTO } from "./arti";

class TestResizeObserver {
  observe() {}
  disconnect() {}
}

const thread = (anchor: ThreadDTO["anchor"], id: string): ThreadDTO => ({
  id,
  anchor,
  status: "open",
  created_by: "test@example.com",
  created_at: "2026-01-01T00:00:00Z",
  comments: [
    {
    id: `${id}-comment`,
      author: "test@example.com",
      author_name: "Test",
      body: "Test comment",
      created_at: "2026-01-01T00:00:00Z",
    },
  ],
});

describe("comments overlay reflow", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("repositions text cards and wires rendered Mermaid media after asynchronous replacement", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    let markTop = 120;
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML =
      '<p>Before diagram</p><div class="mermaid" data-arti-zoom data-mermaid-placeholder><pre>flowchart TD</pre></div><img src="image.png" alt="Later image"><p>Below diagram</p>';
    document.body.append(doc);
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
      if (this.matches("article")) return new DOMRect(0, 0, 800, 1000);
      if (this.matches("mark")) return new DOMRect(0, markTop, 100, 20);
      if (this.matches(".mermaid")) {
        const rendered = this.hasAttribute("data-mermaid-rendered");
        return new DOMRect(100, 300, 400, rendered ? 200 : 20);
      }
      if (this.matches("img")) return new DOMRect(100, 700, 400, 200);
      return new DOMRect(0, 0, 100, 20);
    });

    const api: CommentsApi = {
      list: async () => ({
        threads: [
          thread({ type: "text", quote: "Below diagram" }, "text-thread"),
          thread({ type: "pin", media: 1, rx: 0.5, ry: 0.5 }, "pin-thread"),
        ],
      }),
      create: vi.fn(),
      reply: vi.fn(),
      resolve: vi.fn(),
      reopen: vi.fn(),
      del: vi.fn(),
      edit: vi.fn(),
    };
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      allowPin: true,
      api,
    });
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();
    const card = document.querySelector<HTMLElement>("[data-tid='text-thread']")!;
    const before = card.style.top;
    const pin = document.querySelector<HTMLElement>("[data-pintid='pin-thread']")!;
    const pinBefore = `${pin.style.left},${pin.style.top}`;

    markTop = 420;
    const media = doc.querySelector<HTMLElement>(".mermaid")!;
    media.innerHTML = "<svg><text>Rendered</text></svg>";
    media.dataset.mermaidRendered = "true";
    await new Promise((resolve) => setTimeout(resolve, 300));

    expect(card.style.top).not.toBe(before);
    expect(`${pin.style.left},${pin.style.top}`).toBe(pinBefore);
    media.click();
    expect(document.querySelector(".ac-lb svg text")?.textContent).toBe("Rendered");
    dispose();
  });

  it("drags the control rail vertically and remembers the position globally", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    localStorage.removeItem("arti-cmt-rail-y");
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML = "<p>Some prose</p>";
    document.body.append(doc);
    const api: CommentsApi = {
      list: async () => ({ threads: [] }),
      create: vi.fn(),
      reply: vi.fn(),
      resolve: vi.fn(),
      reopen: vi.fn(),
      del: vi.fn(),
      edit: vi.fn(),
    };
    const mount = () =>
      mountCommentsOverlay({
        container: doc,
        artifactId: "artifact",
        me: { email: "test@example.com", name: "Test", is_admin: false },
        allowPin: true,
        api,
      });

    let dispose = mount();
    await Promise.resolve();
    const rail = () => document.querySelector<HTMLElement>(".ac-fabs")!;
    // Default: centered by the stylesheet, no inline top.
    expect(rail().style.top).toBe("");

    const grip = document.querySelector<HTMLElement>("[data-rail-grip]")!;
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 300, 26, 100));
    grip.dispatchEvent(new MouseEvent("pointerdown", { clientY: 300, bubbles: true }));
    window.dispatchEvent(new MouseEvent("pointermove", { clientY: 380 }));
    window.dispatchEvent(new MouseEvent("pointerup", { clientY: 380 }));
    const moved = rail().style.top;
    expect(moved).not.toBe("");
    expect(rail().style.transform).toBe("none");
    expect(localStorage.getItem("arti-cmt-rail-y")).toBeTruthy();

    // Remount (a different artifact) — the position is a screen preference, so
    // it comes back.
    dispose();
    dispose = mount();
    await Promise.resolve();
    expect(rail().style.top).toBe(moved);

    // The click that ends a mouse drag must NOT recenter the rail (detail>=1).
    document.querySelector<HTMLElement>("[data-rail-grip]")!.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 1 }));
    expect(rail().style.top).toBe(moved);

    // Arrow keys nudge, and only while the handle itself has focus.
    const withGrip = document.querySelector<HTMLElement>("[data-rail-grip]")!;
    const nudge = new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true });
    withGrip.dispatchEvent(nudge);
    expect(nudge.defaultPrevented).toBe(true); // page scroll suppressed, but only here
    expect(rail().style.top).not.toBe(moved);
    const scroll = new KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true, cancelable: true });
    rail().querySelector<HTMLElement>('[data-fab="comments"]')!.dispatchEvent(scroll);
    expect(scroll.defaultPrevented).toBe(false); // the other rail buttons still scroll

    // Dragging far past the top edge stores the CLAMPED position, not the raw
    // one: an out-of-range fraction is rejected on load, which used to snap the
    // rail back to center on the next mount.
    withGrip.dispatchEvent(new MouseEvent("pointerdown", { clientY: 300, bubbles: true }));
    window.dispatchEvent(new MouseEvent("pointermove", { clientY: -4000 }));
    window.dispatchEvent(new MouseEvent("pointerup", { clientY: -4000 }));
    const parked = rail().style.top;
    const stored = Number(localStorage.getItem("arti-cmt-rail-y"));
    expect(stored).toBeGreaterThan(0);
    expect(stored).toBeLessThan(1);
    dispose();
    dispose = mount();
    await Promise.resolve();
    expect(rail().style.top).toBe(parked); // survived the remount

    // Keyboard activation (Enter/Space → click with detail 0) recenters, and so
    // does a double-click.
    document.querySelector<HTMLElement>("[data-rail-grip]")!.dispatchEvent(new MouseEvent("click", { bubbles: true, detail: 0 }));
    expect(rail().style.top).toBe("");
    expect(localStorage.getItem("arti-cmt-rail-y")).toBeNull();
    dispose();
  });

  it("cycles a text card lean → full → icon → lean when its body is clicked", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      callback(0);
      return 0;
    });
    // jsdom has no layout, so scrollIntoView (called when a thread opens) is unimplemented.
    HTMLElement.prototype.scrollIntoView = vi.fn();
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML = "<p>Below diagram</p>";
    document.body.append(doc);

    const api: CommentsApi = {
      list: async () => ({ threads: [thread({ type: "text", quote: "Below diagram" }, "text-thread")] }),
      create: vi.fn(),
      reply: vi.fn(),
      resolve: vi.fn(),
      reopen: vi.fn(),
      del: vi.fn(),
      edit: vi.fn(),
    };
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      allowPin: true,
      api,
    });
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();

    const cardBody = () => document.querySelector<HTMLElement>("[data-tid='text-thread']");
    // lean: a collapsed card with no reply box.
    expect(cardBody()!.classList.contains("ac-active")).toBe(false);
    expect(cardBody()!.querySelector(".ac-reply")).toBeNull();

    // lean → full
    cardBody()!.click();
    expect(cardBody()!.classList.contains("ac-active")).toBe(true);
    expect(cardBody()!.querySelector(".ac-reply")).not.toBeNull();

    // A click on the comment TEXT does not collapse straight away — it waits a
    // double-click interval so word-select still works.
    vi.useFakeTimers();
    const selection = vi.spyOn(window, "getSelection").mockReturnValue(null);
    cardBody()!.querySelector<HTMLElement>(".ac-text")!.click();
    expect(cardBody()!.classList.contains("ac-active")).toBe(true); // still open

    // …and if a selection lands during that wait (i.e. it WAS a double-click),
    // the collapse is abandoned and the card stays open with the word selected.
    const text = cardBody()!.querySelector<HTMLElement>(".ac-text")!;
    selection.mockReturnValue({
      isCollapsed: false,
      rangeCount: 1,
      getRangeAt: () => ({ commonAncestorContainer: text }),
    } as unknown as Selection);
    vi.advanceTimersByTime(500);
    expect(cardBody()!.classList.contains("ac-active")).toBe(true);

    // A pending collapse must not survive a click on a control: clicking into
    // the reply box after a text click has to cancel it, or the card tucks
    // itself away mid-reply.
    selection.mockReturnValue(null);
    cardBody()!.querySelector<HTMLElement>(".ac-text")!.click();
    cardBody()!.querySelector<HTMLTextAreaElement>(".ac-reply textarea")!.click();
    vi.advanceTimersByTime(2000);
    expect(cardBody()!.classList.contains("ac-active")).toBe(true);

    // A plain single click on the text, with no selection following, collapses
    // once the wait is over — but not before.
    cardBody()!.querySelector<HTMLElement>(".ac-text")!.click();
    vi.advanceTimersByTime(400);
    expect(cardBody()).not.toBeNull(); // still there inside a real double-click window
    vi.advanceTimersByTime(200);
    expect(cardBody()).toBeNull();
    vi.useRealTimers();

    // icon → lean, then full again, to check the non-text path collapses with
    // no wait at all.
    document.querySelector<HTMLElement>("[data-mintid='text-thread']")!.click();
    expect(cardBody()!.classList.contains("ac-active")).toBe(false);
    expect(cardBody()!.querySelector(".ac-reply")).toBeNull();
    cardBody()!.click();
    expect(cardBody()!.classList.contains("ac-active")).toBe(true);

    // full → icon, immediately: this click lands on the card itself, not on
    // selectable text.
    cardBody()!.click();
    expect(cardBody()).toBeNull();
    const marker = document.querySelector<HTMLElement>("[data-mintid='text-thread']")!;
    expect(marker).not.toBeNull();

    // icon → lean
    marker.click();
    expect(cardBody()!.classList.contains("ac-active")).toBe(false);
    expect(cardBody()!.querySelector(".ac-reply")).toBeNull();
    dispose();
  });
});

// The overlay is injected into pages it doesn't own — a served HTML artifact is
// arbitrary author markup with no reset, so box-sizing is the CSS default of
// content-box unless our own stylesheet says otherwise. Every fixed size in the
// overlay's CSS is written as a border-box total (CARD_WIDTH is the width
// commentsGeometry.ts computes the doc shift from, padding and border
// included), and the edit composer is `width:100%` inside the card's text
// column. Under content-box the card rendered 26px wider than the shift
// compensated for and the composer's border hung out over the card's own — so
// the reset is load-bearing, and jsdom (no preflight either) is exactly the
// content-box host that catches its removal.
describe("comments overlay box-sizing", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("sizes its own chrome as border-box on a host page that has no reset", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    // jsdom has no layout, so opening a thread would throw on its scroll-into-view.
    Element.prototype.scrollIntoView ??= () => {};
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML = "<p>Quoted text</p>";
    document.body.append(doc);
    // Sanity: the host page really is content-box, so a pass below is our CSS.
    expect(getComputedStyle(doc).boxSizing).toBe("content-box");

    const api: CommentsApi = {
      list: async () => ({ threads: [thread({ type: "text", quote: "Quoted text" }, "text-thread")] }),
      create: vi.fn(),
      reply: vi.fn(),
      resolve: vi.fn(),
      reopen: vi.fn(),
      del: vi.fn(),
      edit: vi.fn(),
    };
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      api,
    });
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();
    const card = document.querySelector<HTMLElement>("[data-tid='text-thread']")!;
    card.click(); // open the thread → full card with the reply composer
    document.querySelector<HTMLElement>("[data-editc]")!.click(); // → edit composer

    expect(getComputedStyle(document.querySelector(".ac-card")!).boxSizing).toBe("border-box");
    expect(getComputedStyle(document.querySelector(".ac-edit-input")!).boxSizing).toBe("border-box");
    expect(getComputedStyle(document.querySelector(".ac-fabs")!).boxSizing).toBe("border-box");
    dispose();
  });
});

// The card's three states differ by exactly what they add: the bubble is a
// count, the concise card is the WHOLE thread, and the full card is the concise
// card plus a composer — whose buttons stay out of the way until you engage it.
describe("comments overlay card states", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
  });

  const twoComments = (id: string): ThreadDTO => {
    const t = thread({ type: "text", quote: "Quoted text" }, id);
    t.comments.push({
      id: `${id}-comment-2`,
      author: "other@example.com",
      author_name: "Other",
      body: "Second comment",
      created_at: "2026-01-01T00:05:00Z",
    });
    return t;
  };

  const mount = (api: CommentsApi) => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => { cb(0); return 0; });
    HTMLElement.prototype.scrollIntoView = vi.fn();
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML = "<p>Quoted text</p>";
    document.body.append(doc);
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      api,
    });
    return dispose;
  };

  const api = (over: Partial<CommentsApi> = {}): CommentsApi => ({
    list: async () => ({ threads: [twoComments("text-thread")] }),
    create: vi.fn(),
    reply: vi.fn(),
    resolve: vi.fn(),
    reopen: vi.fn(),
    del: vi.fn(),
    edit: vi.fn(),
    ...over,
  });

  it("shows the whole thread in the concise card, with no composer and no buttons", async () => {
    const dispose = mount(api());
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();

    const card = document.querySelector<HTMLElement>("[data-tid='text-thread']")!;
    expect(card.classList.contains("ac-active")).toBe(false);
    // Every message, not a one-line preview with a "+n more" to click through.
    expect(card.querySelectorAll(".ac-cmt").length).toBe(2);
    expect(card.textContent).toContain("Second comment");
    expect(card.querySelector(".ac-reply")).toBeNull();
    expect(card.querySelector(".ac-acts")).toBeNull();
    // ONE control cluster, not two that merely line up: the first comment's own
    // actions, a hairline, then ✓ resolve and – minimize. Every control is a
    // direct member of that single flex line (bar the grouped comment actions),
    // so they can't drift out of alignment.
    const rows = card.querySelectorAll<HTMLElement>(".ac-cmt");
    // It rides the first comment's ROW, not the card — a card-anchored offset
    // only lands on the name line of a text card (a pin card puts a chip band
    // there first).
    const corner = rows[0].querySelector<HTMLElement>(".ac-cmt-acts.ac-lead")!;
    expect(corner).not.toBeNull();
    expect(card.querySelector(".ac-card-acts")).toBeNull();
    expect([...corner.children].map((c) => c.className)).toEqual(["ac-grp", "ac-ico ac-resolve", "ac-ico"]);
    const grp = corner.querySelector<HTMLElement>(".ac-grp")!;
    expect([...grp.querySelectorAll("button")].map((b) => b.className)).toEqual(["ac-ico ac-edit", "ac-ico ac-del", "ac-ico ac-link"]);
    expect(grp.lastElementChild!.className).toBe("ac-sep");
    expect(corner.querySelector("[data-resolve]")).not.toBeNull();
    expect(corner.querySelector("[data-min]")).not.toBeNull();
    // The second row keeps a plain cluster of its own (link only: it isn't mine
    // to edit or delete), with no card-level controls in it.
    expect(rows[1].querySelector(".ac-cmt-acts")!.className).toBe("ac-cmt-acts");
    expect(rows[1].querySelectorAll(".ac-cmt-acts button").length).toBe(1);
    dispose();
  });

  it("resolves from the concise card's corner without cycling its state", async () => {
    const resolve = vi.fn(async () => {});
    const dispose = mount(api({ resolve }));
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();

    document.querySelector<HTMLElement>("[data-resolve='text-thread']")!.click();
    await Promise.resolve();
    expect(resolve).toHaveBeenCalledWith("text-thread");
    // Resolved threads leave the margin entirely — the click did not merely
    // open the card (which is what a missing stopPropagation would have done).
    expect(document.querySelector("[data-tid='text-thread']")).toBeNull();
    dispose();
  });

  it("reveals the reply button only once the composer is engaged, and enables it only with text", async () => {
    const reply = vi.fn(async () => ({
      id: "new", author: "test@example.com", author_name: "Test", body: "Sent", created_at: "2026-01-01T00:10:00Z",
    }));
    const dispose = mount(api({ reply }));
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();

    const card = () => document.querySelector<HTMLElement>("[data-tid='text-thread']")!;
    // concise → full: the composer appears, its button row does not.
    card().querySelector<HTMLElement>(".ac-text")!.click();
    expect(card().classList.contains("ac-active")).toBe(true);
    const ta = card().querySelector<HTMLTextAreaElement>("[data-reply]")!;
    const send = () => card().querySelector<HTMLButtonElement>("[data-send]")!;
    expect(card().querySelectorAll(".ac-cmt").length).toBe(2); // same thread as concise
    expect(card().classList.contains("ac-composing")).toBe(false);
    expect(send().disabled).toBe(true);

    // focus → the row shows, still disabled because there's nothing to send.
    ta.focus();
    ta.dispatchEvent(new FocusEvent("focus"));
    expect(card().classList.contains("ac-composing")).toBe(true);
    expect(send().disabled).toBe(true);
    // Enter on an empty composer is a no-op, not an empty reply.
    ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    expect(reply).not.toHaveBeenCalled();

    // type → enabled; clear → disabled again.
    ta.value = "A reply";
    ta.dispatchEvent(new Event("input"));
    expect(send().disabled).toBe(false);
    ta.value = "   ";
    ta.dispatchEvent(new Event("input"));
    expect(send().disabled).toBe(true);

    // blur with an empty composer tucks the row away again.
    ta.value = "";
    ta.blur();
    ta.dispatchEvent(new FocusEvent("blur"));
    expect(card().classList.contains("ac-composing")).toBe(false);
    dispose();
  });
});

// A pin thread's card opens with an .ac-chip band, so anything positioned off
// the CARD at a fixed offset lands on the chip rather than on the comment it
// acts on. The cluster therefore rides the first comment's row.
describe("comments overlay pin card chrome", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("aligns the pin card's controls on its first comment, not on the chip band", async () => {
    vi.stubGlobal("ResizeObserver", TestResizeObserver);
    vi.stubGlobal("requestAnimationFrame", (cb: FrameRequestCallback) => { cb(0); return 0; });
    HTMLElement.prototype.scrollIntoView = vi.fn();
    const doc = document.createElement("article");
    doc.dataset.artiDoc = "";
    doc.innerHTML = '<img src="image.png" alt="An image">';
    document.body.append(doc);
    const dispose = mountCommentsOverlay({
      container: doc,
      artifactId: "artifact",
      me: { email: "test@example.com", name: "Test", is_admin: false },
      allowPin: true,
      api: {
        list: async () => ({ threads: [thread({ type: "pin", media: 0, rx: 0.5, ry: 0.5 }, "pin-thread")] }),
        create: vi.fn(), reply: vi.fn(), resolve: vi.fn(), reopen: vi.fn(), del: vi.fn(), edit: vi.fn(),
      },
    });
    await Promise.resolve();
    document.querySelector<HTMLElement>("[data-fab='comments']")!.click();
    // A pin thread's card shows only while its pin is open (it floats by the
    // pin rather than sitting in the margin column).
    document.querySelector<HTMLElement>("[data-pintid='pin-thread']")!.click();

    const card = document.querySelector<HTMLElement>("[data-tid='pin-thread']")!;
    expect(card.querySelector(".ac-chip")).not.toBeNull();
    const lead = card.querySelector<HTMLElement>(".ac-cmt-acts.ac-lead")!;
    expect(lead).not.toBeNull();
    // Inside the comment row — so it self-aligns on the name line whatever the
    // card renders above it — and nothing is anchored to the card itself.
    expect(lead.parentElement!.className).toContain("ac-cmt");
    expect(card.querySelector(".ac-card-acts")).toBeNull();
    // Resolve is available; minimize is text-only (a pin has no margin bubble).
    expect(lead.querySelector("[data-resolve]")).not.toBeNull();
    expect(lead.querySelector("[data-min]")).toBeNull();
    dispose();
  });
});
