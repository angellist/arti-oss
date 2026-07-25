// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import CatalogTable, { headerShadow } from "./CatalogTable";
import { COLUMN_COOKIE, defaultColumnPrefs, parseColumnPrefs, toggleColumn } from "@/lib/columns";
import type { ArtifactInfo } from "@/lib/types";

// Mutable so a test can put the component in a different view (e.g. the
// single-slug drill-in, which renders an extra `actions` header cell).
// vi.hoisted because the mock factory runs at import time, before any plain
// module-level `let` has been initialized.
const nav = vi.hoisted(() => ({ search: "" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams(nav.search),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (over: Partial<ArtifactInfo> = {}): ArtifactInfo => ({
  artifact_id: "11111111-2222-3333-4444-555555555555",
  artifact_type: "TEXT",
  named_slug: "weekly-report",
  version: 3,
  title: "Weekly report",
  description: "how the week went",
  content_type: "text/markdown",
  size_bytes: 2048,
  sha256: null,
  creator: "sam@example.com",
  scopes: ["topic:platform"],
  labels: ["report"],
  allowed_access: ["*"],
  allowed_write: null,
  metadata: {},
  created_at: "2026-07-30T10:00:00Z",
  modified_at: "2026-07-31T10:00:00Z",
  deleted_at: null,
  url: "http://arti/s/weekly-report/3",
  comment_count: 4,
  open_thread_count: 1,
  ...over,
});

describe("CatalogTable columns", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    document.cookie = `${COLUMN_COOKIE}=; path=/; max-age=0`;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  const render = (initialColumns = defaultColumnPrefs(), rows = [row()]) => {
    act(() => {
      root.render(
        <CatalogTable rows={rows} total={rows.length} page={1} me={null} initialColumns={initialColumns} />,
      );
    });
  };

  const headers = () =>
    Array.from(container.querySelectorAll("thead th")).map((th) =>
      (th.textContent ?? "").replace(/[↑↓⋮]/g, "").trim(),
    );

  const menuItems = () =>
    Array.from(document.querySelectorAll('[role="menuitemcheckbox"]')) as HTMLButtonElement[];

  it("renders the default columns and no opt-in ones", () => {
    render();
    expect(headers()).toContain("title");
    expect(headers()).toContain("scope · labels");
    expect(headers()).not.toContain("comments");
    expect(headers()).not.toContain("id");
  });

  it("renders a column layout supplied by the server without a client re-shuffle", () => {
    // This is the SSR path: page.tsx parses the cookie and passes prefs in, so
    // the very first render must already honor them.
    render(toggleColumn(defaultColumnPrefs(), "comments"));
    expect(headers()).toContain("comments");
    // ● marks the unresolved thread; 4 is the comment count.
    const cells = Array.from(container.querySelectorAll("tbody td")).map((td) => td.textContent);
    expect(cells.join("|")).toContain("4");
  });

  it("opens the column menu on right-click and toggles a column on, persisting to the cookie", () => {
    render();
    const thead = container.querySelector("thead") as HTMLElement;
    act(() => {
      thead.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 40, clientY: 40 }));
    });

    const item = menuItems().find((b) => b.title.startsWith("comments on this version"));
    expect(item, "menu lists every registry column").toBeTruthy();
    expect(item!.getAttribute("aria-checked")).toBe("false");

    act(() => item!.click());

    expect(headers()).toContain("comments");
    // Persistence: the cookie the next SSR render will read back.
    const restored = parseColumnPrefs(
      document.cookie.split(";").map((c) => c.trim()).find((c) => c.startsWith(`${COLUMN_COOKIE}=`))?.slice(COLUMN_COOKIE.length + 1),
    );
    expect(restored.hidden).not.toContain("comments");
  });

  it("hides the MIME subline under `type` once the content type has its own column", () => {
    render();
    const typeCellText = Array.from(container.querySelectorAll("tbody td"))
      .map((td) => td.textContent ?? "")
      .join("|");
    expect(typeCellText).toContain("text/markdown"); // subline present by default

    render(toggleColumn(defaultColumnPrefs(), "content_type"));
    const occurrences = Array.from(container.querySelectorAll("tbody td")).filter((td) =>
      (td.textContent ?? "").includes("text/markdown"),
    );
    expect(occurrences.length, "MIME type must not be shown twice in one row").toBe(1);
  });

  it("shows an em dash, not 0, when the server did not compute comment counts", () => {
    render(toggleColumn(defaultColumnPrefs(), "comments"), [
      row({ comment_count: undefined, open_thread_count: undefined }),
    ]);
    const cells = Array.from(container.querySelectorAll("tbody td")).map((td) => td.textContent);
    expect(cells).toContain("—");
  });

  it("gives every column a resize grip and a <col> to size", () => {
    render();
    const grips = container.querySelectorAll('thead [role="separator"]');
    const colCount = container.querySelectorAll("colgroup col").length;
    expect(grips.length).toBe(headers().length - 1); // every column but the ⋮ cell
    expect(colCount).toBe(grips.length + 1); // + the ⋮ column
  });
});

// The header pins to the top of the row area while the rows scroll under it.
// That only works because the catalog is an app shell: the table's wrapper is
// the one scrolling box (it has to be — it already scrolls horizontally, and a
// sticky cell sticks to its nearest scrollport, not to the document). These
// tests pin that structure, because it is invisible in a screenshot and easy
// to undo by "simplifying" a class list.
describe("CatalogTable sticky header", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    document.cookie = `${COLUMN_COOKIE}=; path=/; max-age=0`;
    nav.search = "";
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    nav.search = "";
  });

  const render = (page = 1, rows = [row()]) => {
    act(() => {
      root.render(
        <CatalogTable
          rows={rows}
          total={rows.length}
          page={page}
          me={null}
          initialColumns={defaultColumnPrefs()}
        />,
      );
    });
  };

  const scroller = () =>
    container.querySelector("div.overflow-auto") as HTMLDivElement | null;

  it("scrolls the rows in one box that the header can stick to", () => {
    render();
    const box = scroller();
    expect(box, "the rows need exactly one scrolling ancestor").toBeTruthy();
    // Both axes on the same element: vertical for the sticky header, and
    // horizontal because the columns can sum past the viewport.
    expect(box!.className).toContain("overflow-auto");
    // Bounded height, or it grows with the rows and never scrolls.
    expect(box!.className).toContain("flex-1");
    expect(box!.className, "min-height:auto would defeat flex-1").toContain("min-h-0");
    expect(box!.contains(container.querySelector("table"))).toBe(true);
    // Keyboard users must be able to scroll it (Safari won't focus it on its own).
    expect(box!.getAttribute("tabindex")).toBe("0");
  });

  it("keeps the shell a bounded column so the scroll box has room to be bounded", () => {
    render();
    const shell = container.firstElementChild as HTMLElement;
    expect(shell.className).toContain("flex-col");
    expect(shell.className).toContain("min-h-0");
    // The chrome above and below the scroll box must not be squeezed by it.
    expect((container.querySelector("nav") as HTMLElement).className).toContain("shrink-0");
  });

  it("pins every header cell, including the ⋮ and drill-in actions cells", () => {
    nav.search = "slug=weekly-report"; // drill-in adds the actions header
    render();
    const ths = Array.from(container.querySelectorAll("thead th"));
    expect(ths.map((t) => t.textContent).join("|")).toContain("actions");
    for (const th of ths) {
      expect(th.className, `${th.textContent} must be sticky`).toContain("sticky");
      expect(th.className).toContain("top-0");
      // Opaque, or the rows scroll through the header. The <thead>'s own
      // background is no help: it scrolls out from under a pinned cell.
      expect(th.className, `${th.textContent} must be opaque`).toContain("bg-neutral-50");
    }
  });

  it("draws the header rule as a shadow, since a collapsed border would stay behind", () => {
    render();
    const thead = container.querySelector("thead") as HTMLElement;
    expect(thead.className, "the rule moved to the cells").not.toContain("border-b");
    for (const th of Array.from(container.querySelectorAll("thead th"))) {
      expect((th as HTMLElement).style.boxShadow).toContain("inset 0 -1px 0 0");
    }
  });

  it("rewinds the scroll box when the rows are swapped out by a navigation", () => {
    render(1);
    const box = scroller()!;
    box.scrollTop = 420;
    // Paging is a URL navigation (?page=2), which is what the rewind keys off
    // — the prop follows the URL, never moves on its own.
    nav.search = "page=2";
    render(2);
    expect(box.scrollTop, "page 2 must open at the top of the list").toBe(0);
  });
});

describe("headerShadow", () => {
  it("always draws the header rule", () => {
    expect(headerShadow(false, false)).toBe("inset 0 -1px 0 0 rgb(229 229 229)");
  });

  it("layers the reorder drop mark on top of the rule instead of replacing it", () => {
    const before = headerShadow(true, false);
    expect(before).toContain("inset 0 -1px 0 0"); // rule survives the drag
    expect(before).toContain("inset 2px 0 0 0 rgb(37 99 235)");
    expect(headerShadow(false, true)).toContain("inset -2px 0 0 0 rgb(37 99 235)");
  });
});

describe("CatalogTable resize grip vs the sticky header", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    document.cookie = `${COLUMN_COOKIE}=; path=/; max-age=0`;
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    act(() => {
      root.render(
        <CatalogTable rows={[row()]} total={1} page={1} me={null} initialColumns={defaultColumnPrefs()} />,
      );
    });
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  // A sticky cell is always a stacking context, so nothing inside a header
  // cell can paint (or be hit) above the *next* header cell. The grip used to
  // hang 4px past the border and lost exactly those 4px to its neighbour —
  // grabbing them started a reorder drag instead of a resize.
  it("keeps the grip inside its own cell", () => {
    const grips = Array.from(
      container.querySelectorAll('thead [role="separator"]'),
    ) as HTMLElement[];
    expect(grips.length).toBeGreaterThan(0);
    for (const g of grips) {
      expect(g.className, "no overhang into the next sticky cell").not.toMatch(/right-\[-/);
      expect(g.className).toContain("right-0");
    }
  });
});

describe("CatalogTable review fixes", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    document.cookie = `${COLUMN_COOKIE}=; path=/; max-age=0`;
    nav.search = "";
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    nav.search = "";
  });

  // jsdom implements PointerEvent but not the pointer-capture methods the
  // resize grip calls, so they are stubbed per element rather than the
  // component being made defensive about a real browser API.
  const firstGrip = () => {
    const g = container.querySelector('thead [role="separator"]') as HTMLElement;
    Object.assign(g, {
      setPointerCapture: () => {},
      hasPointerCapture: () => false,
      releasePointerCapture: () => {},
    });
    return g;
  };

  const render = (rows = [row()], initialColumns = defaultColumnPrefs()) => {
    act(() => {
      root.render(
        <CatalogTable rows={rows} total={rows.length} page={1} me={null} initialColumns={initialColumns} />,
      );
    });
  };

  // A thread whose comments were all deleted is still open — pgstore counts it
  // that way on purpose — so zero comments can coexist with an open thread.
  it("marks an open thread even when every comment in it was deleted", () => {
    render([row({ comment_count: 0, open_thread_count: 1 })], toggleColumn(defaultColumnPrefs(), "comments"));
    const cell = Array.from(container.querySelectorAll("tbody td")).find((td) =>
      (td.textContent ?? "").includes("●"),
    );
    expect(cell, "the amber unresolved marker must still show at 0 comments").toBeTruthy();
    expect(cell!.querySelector("[title]")?.getAttribute("title")).toContain("1 unresolved thread");
  });

  it("still shows a plain 0 when there is nothing outstanding", () => {
    render([row({ comment_count: 0, open_thread_count: 0 })], toggleColumn(defaultColumnPrefs(), "comments"));
    const texts = Array.from(container.querySelectorAll("tbody td")).map((td) => td.textContent);
    expect(texts).toContain("0");
    expect(texts.join("|")).not.toContain("●");
  });

  // The browser fires pointercancel when it takes the gesture over (a touch
  // that becomes a pan of the scroll box). Without a listener the drag never
  // ends: the column stays dimmed and the next pointerup anywhere completes a
  // reorder the user abandoned.
  it("abandons a reorder on pointercancel instead of leaving it armed", () => {
    render();
    const th = container.querySelector('thead th[data-col="title"]') as HTMLElement;
    const order = () =>
      Array.from(container.querySelectorAll("thead th[data-col]")).map((e) =>
        e.getAttribute("data-col"),
      );
    const before = order();

    act(() => {
      th.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, clientX: 100 }));
      window.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, clientX: 400 }));
    });
    expect(th.className, "the dragged column dims while the drag is live").toContain("opacity-40");

    act(() => {
      window.dispatchEvent(new PointerEvent("pointercancel", { bubbles: true, clientX: 400 }));
    });
    expect(th.className, "cancel must clear the drag state").not.toContain("opacity-40");
    expect(order(), "a cancelled drag must not reorder").toEqual(before);

    // Listeners are gone: a stray pointerup no longer finishes the drag.
    act(() => {
      window.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, clientX: 400 }));
    });
    expect(order()).toEqual(before);
    expect(document.cookie).not.toContain(`${COLUMN_COOKIE}=`);
  });


  // A click is not a resize. beginResize seeds the width from what is on
  // screen, which is usually wider than the stored value (the table is
  // min-w-full and stretches columns to fill), so committing on a bare click
  // pinned the column at its stretched width and stopped it flexing.
  it("does not persist a width when the grip is clicked but never dragged", () => {
    render();
    const grip = firstGrip();
    act(() => {
      grip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, clientX: 200 }));
      grip.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, clientX: 200 }));
    });
    expect(document.cookie, "a click must leave no width preference").not.toContain(
      `${COLUMN_COOKIE}=`,
    );
  });

  it("still persists a width when the grip is actually dragged", () => {
    render();
    const grip = firstGrip();
    act(() => {
      grip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, clientX: 200 }));
      // A wide drag on purpose: jsdom reports every rect as 0×0, so the seed
      // width clamps to the column minimum and a small delta would clamp
      // straight back to it — indistinguishable from not having moved.
      grip.dispatchEvent(new PointerEvent("pointermove", { bubbles: true, clientX: 900 }));
      grip.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, clientX: 900 }));
    });
    expect(document.cookie).toContain(`${COLUMN_COOKIE}=`);
  });

  it("rewinds the scroll box when a filter toggle swaps the rows out", () => {
    render();
    const box = container.querySelector("div.overflow-auto") as HTMLDivElement;
    box.scrollTop = 300;
    nav.search = "arch=1"; // show-archived toggle: same page, different rows
    render();
    expect(box.scrollTop, "toggling a filter must return to the top").toBe(0);
  });
});
