// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import UsersManager from "./UsersManager";
import type { RosterUser } from "@/lib/types";

const api = vi.hoisted(() => ({
  added: [] as unknown[],
  addResult: { email: "new@example.com" },
  toggled: [] as [string, boolean][],
  membership: [] as [string, string, boolean][],
  roster: [] as unknown[],
  lookupFails: false,
}));

vi.mock("@/lib/arti", () => ({
  lookupUserAccess: async (email: string) => {
    if (api.lookupFails) throw new Error("role lookup failed");
    return {
    email,
    roles: [
      { name: "USER", source: "baseline", permissions: ["USE_ARTIFACTS"] },
      { name: "ADMIN", source: "group:admins", permissions: ["MANAGE_ROLES", "USE_ARTIFACTS"] },
    ],
    effective_permissions: ["MANAGE_ROLES", "USE_ARTIFACTS"],
    };
  },
  getCredentialAudit: async () => ({ api_keys: [], device_tokens: [], connections: [], share_links: [], usage: [], usage_days: 30 }),
  listUsers: async () => ({ users: api.roster, sources: [] }),
  setGroupMember: async (group: string, email: string, member: boolean) => {
    api.membership.push([group, email, member]);
    return { name: group, members: member ? [email] : [] };
  },
  addUser: async (input: unknown) => {
    api.added.push(input);
    return api.addResult;
  },
  setUserActive: async (email: string, active: boolean) => {
    api.toggled.push([email, active]);
    return {
      email,
      kind: "human",
      note: "",
      roles: [],
      groups: [],
      idp_groups: [],
      idp_stale: false,
      last_seen_at: null,
      source: "admin",
      registered: false,
      added_by: "",
      added_at: null,
      deactivated_at: active ? null : new Date().toISOString(),
      deactivated_by: active ? "" : "admin@example.com",
    };
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
  source: "",
  registered: false,
  added_by: "",
  added_at: null,
  deactivated_at: null,
  deactivated_by: "",
  ...over,
});

// jsdom has no <dialog> modal API.
if (!HTMLDialogElement.prototype.showModal) {
  HTMLDialogElement.prototype.showModal = function () {
    this.setAttribute("open", "");
  };
  HTMLDialogElement.prototype.close = function () {
    this.removeAttribute("open");
  };
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  api.added = [];
  api.toggled = [];
  api.membership = [];
  api.roster = [];
  api.lookupFails = false;
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

  it("narrows by role, group and status dropdowns together", () => {
    render([
      user({ email: "a@example.com", groups: ["funds-team"] }),
      user({ email: "b@example.com", groups: ["funds-team"], deactivated_at: "2026-01-01T00:00:00Z" }),
      user({ email: "c@example.com", kind: "service", idp_groups: ["funds-team"], roles: [{ name: "ADMIN", source: "direct" }] }),
    ]);
    const pick = (label: string, value: string) => {
      const el = container.querySelector(`select[aria-label="filter by ${label}"]`) as HTMLSelectElement;
      act(() => {
        el.value = value;
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };

    pick("group", "group:funds-team");
    expect(text()).toContain("a@example.com");
    expect(text()).toContain("b@example.com");
    expect(text()).not.toContain("c@example.com");

    pick("status", "active");
    expect(text()).toContain("a@example.com");
    expect(text()).not.toContain("b@example.com");

    pick("group", "");
    pick("status", "");
    pick("type", "service");
    expect(text()).toContain("c@example.com");
    expect(text()).not.toContain("a@example.com");

    pick("type", "");
    pick("role", "ADMIN");
    expect(text()).toContain("c@example.com");
    expect(text()).not.toContain("a@example.com");
  });

  it("keeps a selected group in its dropdown after the roster drops it, so the filter stays visible", async () => {
    api.roster = [user({ email: "a@example.com" })];
    act(() => {
      root.render(
        <UsersManager
          initialUsers={[user({ email: "a@example.com", groups: ["solo"] })]}
          sources={[]}
          manageableGroups={[{ name: "solo", members: ["a@example.com"] }]}
        />,
      );
    });
    const sel = container.querySelector('select[aria-label="filter by group"]') as HTMLSelectElement;
    act(() => {
      sel.value = "group:solo";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    const remove = container.querySelector('button[aria-label="remove a@example.com from solo"]') as HTMLButtonElement;
    await act(async () => {
      remove.click();
    });
    expect(text()).not.toContain("group:solo×");
    expect(Array.from(sel.options).map((o) => o.value)).toContain("group:solo");
    expect(sel.value).toBe("group:solo");
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

  const button = (label: string) =>
    Array.from(container.querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;

  it("deactivates only after confirmation, then marks the row", async () => {
    render([user()]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);

    await act(async () => button("Deactivate")!.click());
    expect(api.toggled).toEqual([]);

    await act(async () => button("Deactivate")!.click());
    expect(api.toggled).toEqual([["sam@example.com", false]]);
    expect(text()).toContain("deactivated");
    expect(button("Reactivate")).toBeDefined();
    confirm.mockRestore();
  });

  it("reactivates without a confirmation", async () => {
    render([user({ deactivated_at: new Date().toISOString(), deactivated_by: "admin@example.com" })]);
    const confirm = vi.spyOn(window, "confirm");
    await act(async () => button("Reactivate")!.click());
    expect(confirm).not.toHaveBeenCalled();
    expect(api.toggled).toEqual([["sam@example.com", true]]);
    expect(button("Deactivate")).toBeDefined();
    confirm.mockRestore();
  });

  it("opens a user's permission matrix, with where each role came from, by clicking the email", async () => {
    act(() => {
      root.render(<UsersManager initialUsers={[user({ email: "sam@example.com" })]} sources={[]} permissions={["MANAGE_ROLES", "USE_ARTIFACTS"]} />);
    });
    await act(async () => button("sam@example.com")!.click());
    const dialog = document.querySelector("dialog")!;
    expect(dialog.textContent).toContain("Manage roles");
    expect(dialog.textContent).toContain("from group:admins");
    expect(dialog.textContent).toContain("Stored credentials");
  });

  it("reports a failed permission lookup in its own section and still shows credentials", async () => {
    api.lookupFails = true;
    act(() => {
      root.render(<UsersManager initialUsers={[user({ email: "sam@example.com" })]} sources={[]} permissions={["USE_ARTIFACTS"]} />);
    });
    await act(async () => button("sam@example.com")!.click());
    const dialog = document.querySelector("dialog")!;
    expect(dialog.textContent).toContain("Permissionsrole lookup failed");
    expect(dialog.textContent).toContain("Stored credentials");
  });

  it("sorts by last sign-in newest first on the first click, with unknown sign-ins last", () => {
    render([
      user({ email: "a@example.com", last_seen_at: null }),
      user({ email: "b@example.com", last_seen_at: "2026-01-01T00:00:00Z" }),
      user({ email: "c@example.com", last_seen_at: "2026-06-01T00:00:00Z" }),
    ]);
    act(() => button("Last sign-in")!.click());
    const order = () =>
      Array.from(container.querySelectorAll("tbody tr td:first-child button")).map((b) => b.textContent);
    expect(order()).toEqual(["c@example.com", "b@example.com", "a@example.com"]);
    act(() => button("Last sign-in▼")!.click());
    expect(order()).toEqual(["a@example.com", "b@example.com", "c@example.com"]);
  });

  it("pages 100 rows at a time and returns to page 1 when a filter changes", () => {
    render(Array.from({ length: 150 }, (_, i) => user({ email: `u${String(i).padStart(3, "0")}@example.com` })));
    const rows = () => container.querySelectorAll("tbody tr").length;
    expect(rows()).toBe(100);
    expect(text()).toContain("150 total · 0 deactivated");
    act(() => (container.querySelector('button[aria-label="next page"]') as HTMLButtonElement).click());
    expect(rows()).toBe(50);
    expect(text()).toContain("Showing 101–150 of 150");
    act(() => type(container.querySelector("input") as HTMLInputElement, "u0"));
    expect(text()).toContain("Showing 1–100 of 100 matching");
  });

  it("draws reactivate in neutral, not red", () => {
    render([user({ deactivated_at: new Date().toISOString(), deactivated_by: "admin@example.com" })]);
    expect(button("Reactivate")!.className).not.toContain("rose");
  });

  it("offers no deactivate button on the caller's own row", () => {
    act(() => {
      root.render(
        <UsersManager initialUsers={[user({ email: "me@example.com" }), user()]} sources={[]} currentEmail="Me@Example.com" />,
      );
    });
    expect(Array.from(container.querySelectorAll("button")).filter((b) => b.textContent === "Deactivate")).toHaveLength(1);
  });

  const renderWithGroups = (users: RosterUser[], manageableGroups: { name: string; members: string[] }[]) =>
    act(() => {
      root.render(<UsersManager initialUsers={users} sources={[]} manageableGroups={manageableGroups} />);
    });

  it("adds a user to a manageable group they are not in", async () => {
    api.roster = [user({ groups: ["eng", "ops"] })];
    renderWithGroups([user({ groups: ["eng"] })], [
      { name: "eng", members: ["sam@example.com"] },
      { name: "ops", members: [] },
    ]);
    const select = container.querySelector('select[aria-label="add sam@example.com to a group"]') as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.value)).toEqual(["", "ops"]);

    await act(async () => {
      select.value = "ops";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(api.membership).toEqual([["ops", "sam@example.com", true]]);
    expect(text()).toContain("group:ops");
    expect(text()).toContain("added to ops");
  });

  it("removes a group only where the caller manages it", async () => {
    api.roster = [user({ groups: ["finance"] })];
    renderWithGroups([user({ groups: ["eng", "finance"] })], [{ name: "eng", members: ["sam@example.com"] }]);
    expect(container.querySelector('button[aria-label="remove sam@example.com from finance"]')).toBeNull();

    const remove = container.querySelector('button[aria-label="remove sam@example.com from eng"]') as HTMLButtonElement;
    await act(async () => remove.click());
    expect(api.membership).toEqual([["eng", "sam@example.com", false]]);
    expect(text()).not.toContain("group:eng");
  });

  it("offers no remove control for membership that comes from a pattern", () => {
    renderWithGroups([user({ groups: ["everyone"] })], [{ name: "everyone", members: ["*@example.com"] }]);
    expect(container.querySelector('button[aria-label="remove sam@example.com from everyone"]')).toBeNull();
    const chip = Array.from(container.querySelectorAll("span")).find((s) => s.textContent === "group:everyone");
    expect(chip?.getAttribute("title")).toContain("pattern");
  });

  it("survives a group named after an Object.prototype key", () => {
    renderWithGroups([user({ groups: ["constructor"] })], [{ name: "eng", members: [] }]);
    expect(text()).toContain("group:constructor");
  });
});
