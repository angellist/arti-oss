// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { usePeopleSearch } from "./usePeopleSearch";

// Resolved by the test, one query at a time, so the window between "the user
// typed something new" and "the answer arrived" can be held open and inspected
// — which is the only window in which this hook can be wrong.
let pending: { q: string; resolve: (v: string[]) => void }[] = [];

vi.mock("@/lib/arti", () => ({
  MIN_PEOPLE_QUERY: 2,
  searchPeople: (q: string) =>
    new Promise<string[]>((resolve) => {
      pending.push({ q, resolve });
    }),
}));

// Without this, act() warns and does not install the test scheduler — the
// assertions below would then be checking a render that never flushed.
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

function Probe({ query }: { query: string }) {
  const people = usePeopleSearch(query);
  return <span data-testid="out">{people.join(",")}</span>;
}

// A suggestion list that answers a query the user has already typed past is not
// a cosmetic lag: the rows stay clickable and Enter stays armed, so the wrong
// address is one keystroke from being granted access.
describe("usePeopleSearch", () => {
  let container: HTMLDivElement;
  let root: Root;

  const shown = () => container.querySelector('[data-testid="out"]')!.textContent;
  const render = (query: string) => {
    act(() => {
      root.render(<Probe query={query} />);
    });
  };
  // The hook debounces before it calls searchPeople; the timer is faked so the
  // test drives that boundary instead of racing it.
  const flushDebounce = () => act(() => void vi.advanceTimersByTime(200));
  const settle = async (q: string, people: string[]) => {
    const hit = pending.find((p) => p.q === q);
    if (!hit) throw new Error(`no in-flight search for ${q}; have ${pending.map((p) => p.q)}`);
    await act(async () => {
      hit.resolve(people);
    });
  };

  beforeEach(() => {
    vi.useFakeTimers();
    pending = [];
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
    vi.useRealTimers();
  });

  it("never offers one query's people as the answer to another", async () => {
    render("al");
    flushDebounce();
    await settle("al", ["alice@example.com"]);
    expect(shown()).toBe("alice@example.com");

    // The user clears the field and types something unrelated. Until the new
    // search answers, there is nothing to suggest — showing alice here is the
    // bug this pins.
    render("bo");
    expect(shown()).toBe("");

    flushDebounce();
    expect(shown()).toBe("");

    await settle("bo", ["bob@example.com"]);
    expect(shown()).toBe("bob@example.com");
  });

  it("drops a response that lands after the query moved on", async () => {
    render("al");
    flushDebounce();
    render("ali");
    flushDebounce();

    // The first request answers last. It is not merely out of order — it
    // describes text the field no longer holds.
    await settle("ali", ["alice@example.com"]);
    await settle("al", ["albert@example.com", "alice@example.com"]);
    expect(shown()).toBe("alice@example.com");
  });

  it("returns nothing below the length floor, and never searches for it", () => {
    render("a");
    flushDebounce();
    expect(pending).toHaveLength(0);
    expect(shown()).toBe("");
  });

  it("clearing the field cannot be repopulated by a search already in flight", async () => {
    render("al");
    flushDebounce();
    render("");

    // The response is for a query the user abandoned. Landing it must not put a
    // dropdown back under a cursor that is no longer asking for one.
    await settle("al", ["alice@example.com"]);
    expect(shown()).toBe("");
  });
});
