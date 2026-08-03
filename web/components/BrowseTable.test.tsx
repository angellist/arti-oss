// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

import BrowseTable from "./BrowseTable";
import type { BrowseFacet, BrowseValueCount } from "@/lib/types";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams("facet=type"),
}));

// Without this, act() warns and does not install the test scheduler.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Assigning `el.value` directly is NOT enough to drive a controlled React
// input: React caches the last value it wrote on the node and treats an
// identical-looking assignment as a no-op, so onChange never fires and only the
// DOM moves — which silently turns an interaction test into a test of nothing.
// Go through the prototype's native setter so React's value tracker observes
// the change.
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

// Switching the browse facet must clear the instant-search query. Without it a
// term typed against one facet keeps filtering the next one's values, so the new
// facet looks nearly empty and the user cannot tell that a stale filter — not a
// lack of data — is hiding the rows.
//
// Scope of this test: it pins that the reset HAPPENS. It cannot distinguish
// clearing during render from clearing in an effect, because act() flushes
// effects before we can observe the DOM and the difference between the two is a
// single frame. That ordering fix is verified in a browser; this test is here to
// fail if the reset is ever dropped altogether, which is the regression the
// repo has actually seen (stateful filters surviving a change of the thing they
// filter).
describe("BrowseTable resets its query when the facet changes", () => {
  let container: HTMLDivElement;
  let root: Root;

  const values: Record<string, BrowseValueCount[]> = {
    type: [
      { value: "TEXT", count: 12 },
      { value: "PACKAGE", count: 3 },
    ],
    label: [
      { value: "report", count: 7 },
      { value: "design-doc", count: 2 },
    ],
  };

  const render = (facet: BrowseFacet) => {
    act(() => {
      root.render(
        <BrowseTable
          facet={facet}
          values={values[facet]}
          total={values[facet].length}
          page={1}
          sort="count"
          dir="desc"
        />,
      );
    });
  };

  const input = () => container.querySelector("input") as HTMLInputElement;
  const rowText = () =>
    Array.from(container.querySelectorAll("tbody tr")).map((r) => r.textContent ?? "");

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("drops a query typed against the previous facet, and shows the new facet's values unfiltered", () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    render("type");

    // Type a term that matches one Type value and none of the Label values —
    // so if it leaked across the switch, the Label rows would all disappear.
    act(() => type(input(), "PACK"));
    expect(input().value).toBe("PACK");
    expect(rowText().join(" ")).toContain("PACKAGE");
    expect(rowText().join(" ")).not.toContain("TEXT");

    render("label");

    expect(input().value).toBe("");
    const rows = rowText().join(" ");
    expect(rows).toContain("report");
    expect(rows).toContain("design-doc");
  });

  it("keeps the query when an unrelated prop changes, so re-sorting does not wipe the box", () => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);

    render("type");
    act(() => type(input(), "TEX"));
    expect(input().value).toBe("TEX");

    // Same facet, different sort: the reset must be keyed on the facet alone.
    act(() => {
      root.render(
        <BrowseTable
          facet="type"
          values={values.type}
          total={2}
          page={1}
          sort="name"
          dir="asc"
        />,
      );
    });

    expect(input().value).toBe("TEX");
  });
});
