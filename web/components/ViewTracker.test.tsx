// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ViewTracker from "./ViewTracker";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("ViewTracker", () => {
  let root: Root;
  let container: HTMLDivElement;

  beforeEach(() => {
    sessionStorage.clear();
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  it("records an artifact once per browser session", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      if (init?.method === "POST") return new Response(null, { status: 204 });
      return new Response(JSON.stringify({
        view_key: "x", total: 1, last_7d: 1, last_30d: 1,
        unique_viewers: 1, last_viewed_at: null, can_see_viewers: false, recent: null,
      }), { status: 200, headers: { "Content-Type": "application/json" } });
    });
    const onCount = vi.fn();
    await act(async () => {
      root.render(<ViewTracker artifactID="id-1" onCount={onCount} />);
    });
    await act(async () => Promise.resolve());
    act(() => {
      root.render(<ViewTracker artifactID="id-1" onCount={onCount} />);
    });
    await act(async () => Promise.resolve());
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1);
    expect(onCount).toHaveBeenCalledWith(1, 1);
  });
});
