// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import MapTable from "./MapTable";

const browseMap = vi.fn();
const getMapEntry = vi.fn();
vi.mock("@/lib/arti", () => ({
  browseMap: (...a: unknown[]) => browseMap(...a),
  getMapEntry: (...a: unknown[]) => getMapEntry(...a),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// A controlled React input ignores a direct `el.value =`; go through the
// prototype setter so React's value tracker sees the change. Same reason as
// BrowseTable.test.tsx.
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set;
  setter?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

function page(over: Partial<Record<string, unknown>> = {}) {
  return {
    rows: [
      { key: "cfg:a", value: '{"a":1}', truncated: false, size_bytes: 7, rev: 1, updated_at: "2026-09-14T10:00:00.000Z", updated_by: "a@x" },
      { key: "seen:b", value: "x".repeat(512), truncated: true, size_bytes: 40000, rev: 3, updated_at: "2026-09-14T11:00:00.000Z", updated_by: "b@x" },
    ],
    total: 120,
    limit: 50,
    offset: 0,
    sort: "key",
    dir: "asc",
    q: "",
    stats: { keys: 120, bytes: 40007, max_keys: 10000, max_bytes: 8388608 },
    preview_bytes: 512,
    ...over,
  };
}

describe("MapTable", () => {
  let container: HTMLDivElement;
  let root: Root;

  beforeEach(() => {
    browseMap.mockReset();
    getMapEntry.mockReset();
    browseMap.mockResolvedValue(page());
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });
  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  let snapshotBytes: number | null = 1024;
  const render = async () => {
    await act(async () => {
      root.render(<MapTable slug="my-map" snapshotBytes={snapshotBytes} />);
    });
  };

  it("reads live head, not the artifact body, and says so", async () => {
    await render();
    expect(browseMap).toHaveBeenCalledWith("my-map", expect.objectContaining({ limit: 50, offset: 0 }));
    expect(container.textContent).toContain("live head");
    expect(container.textContent).toContain("cfg:a");
  });

  // A search must reset to the first page. Without it a reader on page 3 who
  // types a term lands on an offset the filtered set does not reach and sees an
  // empty table, which reads as "no results" rather than "wrong page".
  it("returns to the first page when the search changes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await render();
    const next = container.querySelector("nav button:last-of-type") as HTMLButtonElement;
    await act(async () => next.click());
    expect(browseMap).toHaveBeenLastCalledWith("my-map", expect.objectContaining({ offset: 50 }));

    const input = container.querySelector("input") as HTMLInputElement;
    await act(async () => {
      type(input, "needle");
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(browseMap).toHaveBeenLastCalledWith("my-map", expect.objectContaining({ q: "needle", offset: 0 }));
  });

  // Sorting is server-side: clicking a header must re-ask, not reorder the
  // page it already has, or the order is only ever correct within 50 rows.
  it("asks the server to sort, and toggles direction on a repeat click", async () => {
    await render();
    const header = Array.from(container.querySelectorAll("th button")).find((b) => b.textContent?.startsWith("Rev")) as HTMLButtonElement;
    await act(async () => header.click());
    expect(browseMap).toHaveBeenLastCalledWith("my-map", expect.objectContaining({ sort: "rev", dir: "desc" }));
    await act(async () => header.click());
    expect(browseMap).toHaveBeenLastCalledWith("my-map", expect.objectContaining({ sort: "rev", dir: "asc" }));
  });

  // The table only ever receives a preview, so clicking a value has to fetch
  // the entry whole — otherwise the big values are exactly the ones you
  // cannot read — and show it formatted, not as the clipped preview string.
  it("opens the whole value, formatted, when a value is clicked", async () => {
    getMapEntry.mockResolvedValue({ key: "seen:b", value: { full: "value" }, rev: 3 });
    await render();
    const cell = Array.from(container.querySelectorAll("button")).find((b) =>
      b.textContent?.startsWith("x".repeat(20)),
    ) as HTMLButtonElement;
    expect(cell).toBeTruthy();
    await act(async () => cell.click());
    expect(getMapEntry).toHaveBeenCalledWith("my-map", "seen:b");
    expect(container.querySelector('[role="dialog"]')?.textContent).toContain('"full": "value"');
  });

  // Head and the artifact body are different things: before the first
  // snapshot the body is empty, and a reader who clicks Raw Source and sees
  // nothing concludes the data is gone.
  it("warns when nothing has been snapshotted yet", async () => {
    snapshotBytes = 0;
    await render();
    expect(container.textContent).toContain("not snapshotted yet");
    snapshotBytes = 1024;
  });

  // P-002: this subtree stays mounted across a client-side navigation, so
  // without a reset map B opens showing map A's search term, sort, page offset
  // and expanded rows — and reads as "map B is nearly empty" when it is really
  // a stale filter. Same regression BrowseTable already carries a guard for.
  it("drops its search, sort and page when the slug changes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    await render();
    const input = container.querySelector("input") as HTMLInputElement;
    await act(async () => {
      type(input, "needle");
      await vi.advanceTimersByTimeAsync(300);
    });
    const header = Array.from(container.querySelectorAll("th button")).find((b) => b.textContent?.startsWith("Rev")) as HTMLButtonElement;
    await act(async () => header.click());
    const next = container.querySelector("nav button:last-of-type") as HTMLButtonElement;
    await act(async () => next.click());
    expect(browseMap).toHaveBeenLastCalledWith("my-map", expect.objectContaining({ q: "needle", sort: "rev", offset: 50 }));

    browseMap.mockClear();
    await act(async () => {
      root.render(<MapTable slug="other-map" snapshotBytes={snapshotBytes} />);
      await vi.advanceTimersByTimeAsync(300);
    });
    expect(browseMap).toHaveBeenLastCalledWith("other-map", expect.objectContaining({ q: "", sort: "key", dir: "asc", offset: 0 }));
    expect((container.querySelector("input") as HTMLInputElement).value).toBe("");
  });

  it("says when a search matched nothing, rather than looking broken", async () => {
    browseMap.mockResolvedValue(page({ rows: [], total: 0, q: "zzz" }));
    await render();
    expect(container.textContent).toContain("0 of 0");
  });
});
