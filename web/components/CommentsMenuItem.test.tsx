import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import CommentsMenuItem from "./CommentsMenuItem";

const markup = (props: Parameters<typeof CommentsMenuItem>[0]) =>
  renderToStaticMarkup(<CommentsMenuItem {...props} />);

describe("CommentsMenuItem", () => {
  it("shows a green dot and 'Allowed' when comments are on", () => {
    const html = markup({ on: true, canManage: true, onToggle: () => {} });
    expect(html).toContain("bg-emerald-500");
    expect(html).not.toContain("bg-neutral-300");
    expect(html).toContain("Allowed");
    expect(html).toContain('aria-pressed="true"');
  });

  it("shows a grey dot and 'Off' when the owner turned comments off", () => {
    const html = markup({ on: false, canManage: true, onToggle: () => {} });
    expect(html).toContain("bg-neutral-300");
    expect(html).not.toContain("bg-emerald-500");
    expect(html).toContain(">Off<");
    expect(html).toContain('aria-pressed="false"');
  });

  // Non-owners still see the state — an absent comment affordance with no
  // explanation reads as a bug — but the control must be inert for them.
  it("is disabled for a non-owner while still reporting the state", () => {
    const off = markup({ on: false, canManage: false, onToggle: () => {} });
    expect(off).toContain("disabled");
    expect(off).toContain("bg-neutral-300");
    expect(off).toContain("the owner turned comments off for this doc");

    const on = markup({ on: true, canManage: false, onToggle: () => {} });
    expect(on).toContain("disabled");
    expect(on).toContain("bg-emerald-500");
    expect(on).toContain("only the doc");
  });

  // The row shares the menu's icon-slot geometry so "Comments" lines up with
  // "Edit" / "Download", and carries no top border — the menu is a single
  // undivided list.
  it("uses the shared menu row geometry and no divider", () => {
    const html = markup({ on: true, canManage: true, onToggle: () => {} });
    expect(html).toContain("h-3.5 w-3.5");
    expect(html).not.toContain("border-t");
  });

  it("stays clickable-looking but disabled mid-save", () => {
    const html = markup({ on: true, canManage: true, busy: true, onToggle: () => {} });
    expect(html).toContain("disabled");
    expect(html).toContain("saving");
  });
});
