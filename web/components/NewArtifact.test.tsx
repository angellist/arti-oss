// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import NewArtifact from "./NewArtifact";

// Lets React run act() without warning that the environment isn't configured
// for it, so state flushed inside act is actually observable here.
(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const push = vi.fn();
const createArtifact = vi.fn();
const latestVersionForSlug = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, refresh: vi.fn() }),
}));

vi.mock("@/lib/arti", async () => {
  // ArtiError is a real class the component instanceof-checks, so keep it.
  const actual = await vi.importActual<typeof import("@/lib/arti")>("@/lib/arti");
  return {
    ArtiError: actual.ArtiError,
    createArtifact: (...args: unknown[]) => createArtifact(...args),
    latestVersionForSlug: (...args: unknown[]) => latestVersionForSlug(...args),
    getAggregates: () => Promise.resolve({ scopes: [], labels: [] }),
  };
});

function buttons(container: HTMLElement): HTMLButtonElement[] {
  return Array.from(container.querySelectorAll("button"));
}
function byText(container: HTMLElement, label: string): HTMLButtonElement | undefined {
  return buttons(container).find((b) => b.textContent?.trim() === label);
}
function field(container: HTMLElement, aria: string): HTMLInputElement {
  const el = container.querySelector<HTMLInputElement>(`[aria-label="${aria}"]`);
  if (!el) throw new Error(`no field labelled ${aria}`);
  return el;
}

// Typing into a controlled React input: set the value through the native
// setter, then dispatch input so React's onChange sees it.
function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement : HTMLInputElement;
  Object.getOwnPropertyDescriptor(proto.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
}

describe("NewArtifact", () => {
  let container: HTMLDivElement;
  let root: Root;

  const mount = async (kind: "text" | "diagram") => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<NewArtifact kind={kind} />);
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });
  };

  beforeEach(() => {
    // This jsdom exposes a localStorage whose property access throws, and the
    // tree reads persisted view prefs (useViewerPrefs). Install a working
    // in-memory Storage so the components under test behave as in a browser.
    const store = new Map<string, string>();
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: {
        getItem: (k: string) => store.get(k) ?? null,
        setItem: (k: string, v: string) => void store.set(k, v),
        removeItem: (k: string) => void store.delete(k),
        clear: () => store.clear(),
        key: (i: number) => Array.from(store.keys())[i] ?? null,
        get length() {
          return store.size;
        },
      },
    });
    latestVersionForSlug.mockResolvedValue(null);
    createArtifact.mockResolvedValue({ named_slug: "my-doc", version: 1, artifact_id: "id-1" });
  });

  afterEach(() => {
    act(() => root?.unmount());
    container?.remove();
    document.body.innerHTML = "";
    vi.clearAllMocks();
  });

  // Carried over from the old NewDiagram suite: leaving an untouched page must
  // not nag. The starter canvas is not the user's work.
  it("does not treat an untouched draft as dirty when leaving", async () => {
    await mount("diagram");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await act(async () => {
      byText(container, "Cancel")!.click();
    });

    expect(confirm).not.toHaveBeenCalled();
    expect(push).toHaveBeenCalledWith("/");
  });

  it("confirms before discarding a draft that has been typed in", async () => {
    await mount("text");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);

    await act(async () => {
      type(field(container, "title"), "Real work");
    });
    await act(async () => {
      byText(container, "Cancel")!.click();
    });

    expect(confirm).toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
  });

  // The stamp has to be VISIBLE, not applied at submit time — otherwise the box
  // shows one slug and the server stores another.
  it("shows the timestamped default slug before anything is typed", async () => {
    await mount("text");
    // Rendered as the placeholder: the box is empty because the slug is derived,
    // but the derived value is on screen.
    expect(field(container, "slug").placeholder).toMatch(/^untitled-text-\d{6}-\d{4}$/);
  });

  it("creates with ensure_new and no access fields when access is untouched", async () => {
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
    });

    expect(createArtifact).toHaveBeenCalledTimes(1);
    const input = createArtifact.mock.calls[0][0];
    expect(input.ensure_new).toBe(true);
    expect(input.named_slug).toBe("my-doc");
    expect(input.content_type).toBe("text/markdown");
    expect(input.artifact_type).toBe("TEXT");
    // Untouched access must be OMITTED so the server's own default applies.
    expect("allowed_access" in input).toBe(false);
    expect(push).toHaveBeenCalledWith("/s/my-doc/1");
  });

  it("sends the diagram content type and a serialized canvas", async () => {
    createArtifact.mockResolvedValue({ named_slug: "d", version: 1, artifact_id: "id-2" });
    await mount("diagram");

    await act(async () => {
      byText(container, "Create")!.click();
    });

    const input = createArtifact.mock.calls[0][0];
    expect(input.content_type).toBe("application/vnd.arti.diagram+json");
    expect(input.artifact_type).toBe("TEXT");
    // The starter canvas round-trips as parseable JSON, not an empty body.
    const body = Buffer.from(input.content_base64, "base64").toString("utf8");
    expect(() => JSON.parse(body)).not.toThrow();
  });

  // The advisory check fires on blur so a collision is reported while the field
  // is still in hand.
  it("opens the conflict dialog when the typed slug already exists", async () => {
    latestVersionForSlug.mockResolvedValue(4);
    await mount("text");

    const slug = field(container, "slug");
    await act(async () => {
      type(slug, "taken-slug");
    });
    await act(async () => {
      // React delegates onBlur from the bubbling `focusout`, not `blur`.
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      // The check awaits latestVersionForSlug before setting state.
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).toContain("That slug is taken");
    expect(document.body.textContent).toContain("latest is v4");
    // No create was attempted — the dialog replaces the collision, it doesn't
    // follow a failed one.
    expect(createArtifact).not.toHaveBeenCalled();
  });

  // The dialog is an overlay, so the field behind it can't be clicked — but
  // nothing traps focus, so a keyboard user can tab back out, fix the slug and
  // blur again. That re-check must retract the warning: the field no longer
  // names a taken slug, so a dialog saying it does is simply wrong.
  it("closes the conflict dialog when a later check finds the slug free", async () => {
    latestVersionForSlug.mockResolvedValueOnce(4).mockResolvedValueOnce(null);
    await mount("text");

    const slug = field(container, "slug");
    await act(async () => {
      type(slug, "taken-slug");
    });
    await act(async () => {
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(document.body.textContent).toContain("That slug is taken");

    await act(async () => {
      type(slug, "free-slug");
    });
    await act(async () => {
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).not.toContain("That slug is taken");
  });

  // Two quick blurs against different slugs race. The response for the slug the
  // user ABANDONED must not raise a dialog naming it — that dialog would be
  // about a slug no longer in the field.
  it("ignores a slow check that resolves after a newer one started", async () => {
    await mount("text");

    let resolveFirst: (v: number | null) => void = () => {};
    latestVersionForSlug
      // First blur ("abandoned"): held open, and it WILL report a collision.
      .mockImplementationOnce(() => new Promise((r) => (resolveFirst = r)))
      // Second blur ("kept"): resolves immediately, no collision.
      .mockResolvedValueOnce(null);

    const slug = field(container, "slug");
    await act(async () => {
      type(slug, "abandoned");
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
    await act(async () => {
      type(slug, "kept");
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });
    // The stale answer lands last, claiming "abandoned" is taken.
    await act(async () => {
      resolveFirst(7);
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).not.toContain("That slug is taken");
  });

  // A slug owned by someone else, not shared with you, comes back as 404 (not
  // 409): checkWriteAccess returns ErrNotFound before ensure_new is consulted.
  // It's also invisible to the advisory blur check, which is access-filtered —
  // so this is the one path where the dialog is the user's FIRST notice.
  it("routes a 404 from create into the conflict dialog", async () => {
    const { ArtiError } = await import("@/lib/arti");
    createArtifact.mockRejectedValue(new ArtiError(404, "not found"));
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).toContain("That slug is taken");
    // And it says the thing that's actually true: you can't go open it.
    expect(document.body.textContent).toContain("isn't shared with you");
  });

  // The advisory check is access-filtered: for an unreadable slug it reports
  // "free" while create correctly gets a 404. So a conflict the SERVER stated
  // must not be retracted by a later advisory "looks free" answer — that is the
  // one case where the two disagree and the server is right.
  it("keeps a create-raised conflict when a later advisory check finds the slug free", async () => {
    const { ArtiError } = await import("@/lib/arti");
    createArtifact.mockRejectedValue(new ArtiError(404, "not found"));
    latestVersionForSlug.mockResolvedValue(null);
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(document.body.textContent).toContain("That slug is taken");

    await act(async () => {
      const slug = field(container, "slug");
      type(slug, "my-doc");
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).toContain("That slug is taken");
  });

  // The server's verdict outranks the advisory check only for the SAME slug.
  // Once the user moves the field to a different, free slug, a dialog still
  // naming the old one is as stale as one we raised ourselves.
  it("drops a create-raised conflict once the field moves to a different free slug", async () => {
    const { ArtiError } = await import("@/lib/arti");
    createArtifact.mockRejectedValue(new ArtiError(404, "not found"));
    latestVersionForSlug.mockResolvedValue(null);
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(document.body.textContent).toContain("That slug is taken");

    await act(async () => {
      const slug = field(container, "slug");
      type(slug, "some-other-slug");
      slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).not.toContain("That slug is taken");
  });

  // A 403 is about the access being set, not about the slug — it must NOT be
  // dressed up as a slug collision.
  it("surfaces a 403 as an error rather than a slug conflict", async () => {
    const { ArtiError } = await import("@/lib/arti");
    createArtifact.mockRejectedValue(new ArtiError(403, "changing access requires being the owner"));
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
      await new Promise((r) => setTimeout(r, 0));
    });

    expect(document.body.textContent).not.toContain("That slug is taken");
    expect(container.textContent).toContain("changing access requires being the owner");
  });

  // Dismissing the dialog isn't fixing the slug. Blurring the same taken value
  // again is a fresh assertion of it and must warn again.
  it("re-warns after the conflict dialog is dismissed and the slug re-blurred", async () => {
    latestVersionForSlug.mockResolvedValue(4);
    await mount("text");
    const slug = field(container, "slug");

    const blur = async () => {
      await act(async () => {
        slug.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
        await new Promise((r) => setTimeout(r, 0));
      });
    };

    await act(async () => {
      type(slug, "taken-slug");
    });
    await blur();
    expect(document.body.textContent).toContain("That slug is taken");

    // Scoped to the dialog: the page header has its own Cancel (discard draft).
    await act(async () => {
      const dialog = document.querySelector('[role="dialog"]') as HTMLElement;
      byText(dialog, "Cancel")!.click();
    });
    expect(document.body.textContent).not.toContain("That slug is taken");

    await blur();
    expect(document.body.textContent).toContain("That slug is taken");
  });

  // ensure_new's 409 is the authoritative guard, and must land in the same
  // dialog as the advisory check rather than as a raw error string.
  it("routes a 409 from create into the conflict dialog", async () => {
    const { ArtiError } = await import("@/lib/arti");
    createArtifact.mockRejectedValue(new ArtiError(409, 'slug "my-doc" already exists'));
    await mount("text");

    await act(async () => {
      type(field(container, "title"), "My Doc");
    });
    await act(async () => {
      byText(container, "Create")!.click();
      await Promise.resolve();
    });

    expect(document.body.textContent).toContain("That slug is taken");
  });

  // Diagrams author at full width, so the control that would fight that is not
  // rendered at all.
  it("hides the width control for diagrams but shows it for markdown", async () => {
    await mount("diagram");
    expect(container.querySelector('[aria-label="reading width"]')).toBeNull();
    act(() => root.unmount());
    container.remove();

    await mount("text");
    expect(container.querySelector('[aria-label="reading width"]')).not.toBeNull();
  });

  // Split puts two panes side by side, which needs the whole page — so it takes
  // the width control away with it, and gives it back on the way out.
  it("hides the width control in Split and restores it in Write", async () => {
    await mount("text");
    const widthControl = () => container.querySelector('[aria-label="reading width"]');
    expect(widthControl()).not.toBeNull();

    await act(async () => {
      byText(container, "split")!.click();
    });
    expect(widthControl()).toBeNull();

    await act(async () => {
      byText(container, "write")!.click();
    });
    expect(widthControl()).not.toBeNull();
  });

  // The chips are the viewer's own control, so labels/scopes carry no
  // input-box chrome — just chips and a "+".
  it("offers labels and scopes as bare chips with a + affordance", async () => {
    await mount("text");
    expect(container.querySelector('button[aria-label="add label"]')).not.toBeNull();
    expect(container.querySelector('button[aria-label="add scope"]')).not.toBeNull();
    // No always-on text input for either (the "+" reveals one on demand).
    expect(container.querySelector('input[placeholder="add a label…"]')).toBeNull();
  });
});
