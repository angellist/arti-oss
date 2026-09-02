// @vitest-environment jsdom

import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import UploadModal from "./UploadModal";
import type { UploadTarget } from "@/lib/upload-target";

(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const push = vi.fn();
const createArtifact = vi.fn();
const getBySlug = vi.fn();

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push, refresh: vi.fn() }),
}));

vi.mock("@/lib/arti", async () => {
  // ArtiError is a real class the component instanceof-checks, so keep it.
  const actual = await vi.importActual<typeof import("@/lib/arti")>("@/lib/arti");
  return {
    ArtiError: actual.ArtiError,
    createArtifact: (...args: unknown[]) => createArtifact(...args),
    getBySlug: (...args: unknown[]) => getBySlug(...args),
    latestVersionForSlug: () => Promise.resolve(null),
    suggestMetadata: () => Promise.resolve({ title: "", slug: "", labels: [] }),
    getAggregates: () => Promise.resolve({ scopes: [], labels: [] }),
  };
});

const target: UploadTarget = {
  slug: "quarterly-report",
  viewedVersion: 7,
  artifactType: "TEXT",
  contentType: "text/markdown",
  title: "Quarterly report",
  description: "How the quarter went",
  labels: ["report"],
  scopes: ["a:reports"],
};

// The slug's latest, as the modal re-reads it on open — deliberately AHEAD of
// the version the viewer is showing, which is the case the base-version pin
// exists for.
function latest(over: Record<string, unknown> = {}) {
  return {
    artifact_id: "id",
    artifact_type: "TEXT",
    named_slug: "quarterly-report",
    version: 9,
    title: "Quarterly report (renamed)",
    description: "How the quarter went",
    content_type: "text/markdown",
    sha256: "deadbeef",
    labels: ["report", "arti"],
    scopes: ["a:reports"],
    ...over,
  };
}

function md(name = "q3.md", type = "text/markdown") {
  return new File(["# Q3"], name, { type });
}

function buttons(c: HTMLElement) {
  return Array.from(c.querySelectorAll("button"));
}
function byText(c: HTMLElement, startsWith: string) {
  return buttons(c).find((b) => (b.textContent ?? "").trim().startsWith(startsWith));
}
function body(): HTMLElement {
  return document.body;
}

describe("UploadModal — version mode", () => {
  let container: HTMLDivElement;
  let root: Root;

  const mount = async (file: File, t: UploadTarget | null = target) => {
    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(<UploadModal initialFile={file} target={t} onClose={() => {}} />);
      await Promise.resolve();
    });
    // Settle the file read and the slug re-read.
    for (let i = 0; i < 4; i++) {
      await act(async () => {
        await Promise.resolve();
      });
    }
  };

  beforeEach(() => {
    push.mockReset();
    createArtifact.mockReset();
    createArtifact.mockResolvedValue({ artifact_id: "new", named_slug: "quarterly-report", version: 10 });
    getBySlug.mockReset();
    getBySlug.mockResolvedValue(latest());
  });

  afterEach(() => {
    act(() => root.unmount());
    container.remove();
  });

  it("locks the slug and inherits the document's title, not the filename's", async () => {
    await mount(md());
    const inputs = Array.from(body().querySelectorAll("input"));
    const titleInput = inputs.find((i) => i.placeholder === "A human-readable title")!;
    expect(titleInput.value).toBe("Quarterly report (renamed)");
    expect(inputs.some((i) => i.placeholder === "kebab-case-name")).toBe(false);
    expect(body().textContent).toContain("s/quarterly-report");
  });

  // The version published is latest + 1, never viewed + 1 — the viewer is
  // showing v7 while the slug is at v9.
  it("publishes ahead of the slug's latest, pinned to it", async () => {
    await mount(md());
    expect(byText(body(), "Publish v10")).toBeTruthy();
    await act(async () => {
      byText(body(), "Publish v10")!.click();
    });
    const sent = createArtifact.mock.calls[0][0];
    expect(sent.named_slug).toBe("quarterly-report");
    expect(sent.expected_latest_version).toBe(9);
    expect(sent.title).toBe("Quarterly report (renamed)");
    expect(sent.description).toBe("How the quarter went");
    // Untouched: omitted so the server inherits them from the fresh prior
    // version. Sending [] here is what used to wipe a slug's labels.
    expect(sent.labels).toBeUndefined();
    expect(sent.scopes).toBeUndefined();
    expect(sent.allow_type_change).toBeUndefined();
  });

  it("sends labels once they are edited", async () => {
    await mount(md());
    const chipInput = body().querySelector<HTMLInputElement>('input[aria-label="add a label"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(
        chipInput,
        "quarterly",
      );
      chipInput.dispatchEvent(new Event("input", { bubbles: true }));
      chipInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    });
    await act(async () => {
      byText(body(), "Publish v10")!.click();
    });
    expect(createArtifact.mock.calls[0][0].labels).toContain("quarterly");
  });

  it("blocks a type change until it is acknowledged, then declares it", async () => {
    await mount(md("q3.html", "text/html"));
    const publish = byText(body(), "Publish v10")!;
    expect(publish.textContent).toContain("text/html");
    expect(publish.disabled).toBe(true);
    const ack = body().querySelector('input[type="checkbox"]') as HTMLInputElement;
    await act(async () => {
      ack.click();
    });
    expect(byText(body(), "Publish v10")!.disabled).toBe(false);
    await act(async () => {
      byText(body(), "Publish v10")!.click();
    });
    expect(createArtifact.mock.calls[0][0].allow_type_change).toBe(true);
  });

  // Someone published between opening the modal and hitting Publish. The
  // upload must not land: the modal re-reads the slug, re-aims at the new
  // latest, and waits for a second, informed click.
  it("recovers from a lost publish race by re-reading and re-aiming", async () => {
    const { ArtiError } = await import("@/lib/arti");
    await mount(md());
    createArtifact.mockRejectedValueOnce(
      new ArtiError(409, 'slug "quarterly-report" is now at v11', "stale-base-version"),
    );
    getBySlug.mockResolvedValue(latest({ version: 11 }));
    await act(async () => {
      byText(body(), "Publish v10")!.click();
    });
    expect(push).not.toHaveBeenCalled();
    expect(body().textContent).toContain("v11");
    const retry = byText(body(), "Publish v12")!;
    expect(retry).toBeTruthy();
    await act(async () => {
      retry.click();
    });
    expect(createArtifact.mock.calls[1][0].expected_latest_version).toBe(11);
  });

  // Publishing before the slug read lands would send no
  // expected_latest_version — the unpinned publish the pin exists to prevent —
  // and would skip any type-change warning the read would have produced.
  it("will not publish while the slug read is still in flight", async () => {
    let release: (v: unknown) => void = () => {};
    getBySlug.mockReturnValue(new Promise((r) => (release = r)));
    await mount(md());
    const waiting = byText(body(), "Reading s/quarterly-report")!;
    expect(waiting.disabled).toBe(true);
    await act(async () => {
      release(latest());
      await Promise.resolve();
    });
    expect(byText(body(), "Publish v10")!.disabled).toBe(false);
    expect(createArtifact).not.toHaveBeenCalled();
  });

  // A dropped file with no target is still a new document, exactly as before.
  it("keeps the plain upload form when there is no target", async () => {
    await mount(md(), null);
    const inputs = Array.from(body().querySelectorAll("input"));
    expect(inputs.some((i) => i.placeholder === "kebab-case-name")).toBe(true);
    expect(byText(body(), "Upload")).toBeTruthy();
    expect(getBySlug).not.toHaveBeenCalled();
  });
});
