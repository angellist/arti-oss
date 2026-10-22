// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import RolesManager from "./RolesManager";

const api = vi.hoisted(() => ({
  assigned: [] as unknown[],
  people: [] as string[],
}));

vi.mock("@/lib/arti", () => ({
  MIN_PEOPLE_QUERY: 2,
  searchPeople: async () => api.people,
  listAssignments: async () => [],
  listGroups: async () => [],
  assignRole: async (input: unknown) => {
    api.assigned.push(input);
  },
  unassignRole: async () => {},
  listRoles: async () => [],
  createRole: async () => {},
  deleteRole: async () => {},
  updateRole: async () => {},
}));

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  api.assigned = [];
  api.people = [];
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
  vi.useRealTimers();
});

function type(el: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")?.set?.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("RolesManager", () => {
  it("suggests people by email and assigns the one picked", async () => {
    vi.useFakeTimers();
    api.people = ["alice@example.com"];
    act(() => {
      root.render(
        <RolesManager
          initialRoles={[{ name: "EDITOR", description: "", permissions: [], builtin: false } as never]}
          permissions={["USE_ARTIFACTS"]}
        />,
      );
    });
    const input = container.querySelector('input[placeholder="search by email"]') as HTMLInputElement;
    act(() => {
      input.dispatchEvent(new FocusEvent("focusin", { bubbles: true }));
      type(input, "ali");
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(200);
    });
    const pick = Array.from(container.querySelectorAll("li button")).find(
      (b) => b.textContent === "alice@example.com",
    ) as HTMLButtonElement;
    expect(pick).toBeTruthy();
    await act(async () => {
      pick.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
    });
    expect(api.assigned).toEqual([{ principal_type: "user", principal_id: "alice@example.com", role_name: "EDITOR" }]);
  });
});
