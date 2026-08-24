// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import UsersManager from "./UsersManager";
import type { RosterUser } from "@/lib/types";

const api = vi.hoisted(() => ({
  added: [] as unknown[],
  addResult: { email: "new@example.com" },
}));

vi.mock("@/lib/arti", () => ({
  listUsers: async () => ({ users: [], sources: [] }),
  addUser: async (input: unknown) => {
    api.added.push(input);
    return api.addResult;
  },
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const user = (over: Partial<RosterUser> = {}): RosterUser => ({
  email: "sam@example.com",
  kind: "human",
  note: "",
  roles: [{ name: "USER", source: "baseline" }],
  groups: [],
  idp_groups: [],
  idp_stale: false,
  last_seen_at: new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString(),
  registered: false,
  added_by: "",
  added_at: null,
  ...over,
});

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  api.added = [];
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  document.body.innerHTML = "";
});

const render = (users: RosterUser[], sources: string[] = ["user_idp_groups"]) =>
  act(() => {
    root.render(<UsersManager initialUsers={users} sources={sources} />);
  });

const text = () => document.body.textContent ?? "";

// Assigning .value directly leaves React's value tracker thinking nothing
// changed, so onChange never fires and the test silently asserts on an
// un-typed field. Go through the prototype's native setter, as BrowseTable's
// test does.
function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    "value",
  )?.set;
  setter?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("UsersManager", () => {
  it("distinguishes a stale SSO group from a live one, because a stale one grants nothing", () => {
    render([
      user({ email: "live@example.com", idp_groups: ["eng"], idp_stale: false }),
      user({ email: "aged@example.com", idp_groups: ["eng"], idp_stale: true }),
    ]);
    const chips = Array.from(document.querySelectorAll("span")).filter(
      (s) => s.textContent === "idp:eng",
    );
    expect(chips).toHaveLength(2);
    const struck = chips.filter((c) => c.className.includes("line-through"));
    // Exactly the aged row is struck through. Rendering a stale grant like a
    // live one would overstate the person's access.
    expect(struck).toHaveLength(1);
    expect(struck[0].getAttribute("title")).toContain("aged out");
  });

  it("says a missing sign-in is unknown rather than never, since logins predate the record", () => {
    render([user({ last_seen_at: null })]);
    expect(text()).toContain("no sign-in recorded");
    expect(text()).not.toContain("never signed in");
  });

  it("shows one row per role source so it is clear how a role was obtained", () => {
    render([
      user({
        roles: [
          { name: "USER", source: "baseline" },
          { name: "ADMIN", source: "direct" },
          { name: "ADMIN", source: "group:platform" },
        ],
      }),
    ]);
    expect(text()).toContain("baseline");
    expect(text()).toContain("direct");
    expect(text()).toContain("via group:platform");
  });

  it("filters on roles and groups, not just the email", () => {
    render([
      user({ email: "a@example.com", groups: ["funds-team"] }),
      user({ email: "b@example.com", roles: [{ name: "ADMIN", source: "direct" }] }),
    ]);
    const input = container.querySelector("input") as HTMLInputElement;

    act(() => type(input, "funds"));
    expect(text()).toContain("a@example.com");
    expect(text()).not.toContain("b@example.com");

    act(() => type(input, "ADMIN"));
    expect(text()).toContain("b@example.com");
    expect(text()).not.toContain("a@example.com");
  });

  it("names the sources it read, so a missing principal class is diagnosable", () => {
    render([user()], ["user_idp_groups", "api_keys.owner_email"]);
    expect(text()).toContain("api_keys.owner_email");
  });

  it("marks a service principal and who recorded it", () => {
    render([
      user({
        email: "devin.sa@example.com",
        kind: "service",
        registered: true,
        added_by: "tian@example.com",
        note: "platform team",
      }),
    ]);
    expect(text()).toContain("service");
    expect(text()).toContain("added by tian@example.com");
    expect(text()).toContain("platform team");
  });

  it("submits the add-user form and states that it granted nothing", async () => {
    render([user()]);
    const open = Array.from(container.querySelectorAll("button")).find((b) =>
      (b.textContent ?? "").includes("Add user"),
    ) as HTMLButtonElement;
    act(() => open.click());

    const email = document.querySelector(
      'input[placeholder="service-account@example.com"]',
    ) as HTMLInputElement;
    act(() => type(email, "devin.sa@example.com"));

    const submit = Array.from(document.querySelectorAll("button")).find(
      (b) => b.textContent === "Add user",
    ) as HTMLButtonElement;
    await act(async () => {
      submit.click();
    });

    expect(api.added).toEqual([
      { email: "devin.sa@example.com", kind: "service", note: "" },
    ]);
    // The confirmation must not imply the principal was granted anything.
    expect(text()).toContain("baseline USER role only");
  });

  it("marks the modal aria-modal so global shortcut handlers stand down", () => {
    render([user()]);
    const open = Array.from(container.querySelectorAll("button")).find((b) =>
      (b.textContent ?? "").includes("Add user"),
    ) as HTMLButtonElement;
    act(() => open.click());
    // CatalogTable and DiagramEditor both return early when this selector
    // matches. Without it, `/` typed with the Kind select focused reaches the
    // catalog search instead of the dialog.
    const dialog = document.querySelector('[aria-modal="true"]');
    expect(dialog).not.toBeNull();
    expect(dialog?.getAttribute("role")).toBe("dialog");
  });

  it("keeps the submit button inert until an address is typed", () => {
    render([user()]);
    const open = Array.from(container.querySelectorAll("button")).find((b) =>
      (b.textContent ?? "").includes("Add user"),
    ) as HTMLButtonElement;
    act(() => open.click());
    const submit = Array.from(document.querySelectorAll("button")).find(
      (b) => b.textContent === "Add user",
    ) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
  });
});
