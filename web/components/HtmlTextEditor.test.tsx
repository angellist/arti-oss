import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import HtmlTextEditor from "./HtmlTextEditor";
import type { ArtifactInfo } from "@/lib/types";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
}));

const info = {
  artifact_id: "921bad68-9612-420a-8ce9-e8609ff17950",
  artifact_type: "TEXT",
  named_slug: "deck",
  version: 17,
  title: "Deck",
  description: null,
  content_type: "text/html",
  deleted_at: null,
} as unknown as ArtifactInfo;

describe("HtmlTextEditor", () => {
  it("loads the edit-mode page in a sandbox without same-origin", () => {
    const html = renderToStaticMarkup(<HtmlTextEditor info={info} onClose={() => {}} onEditSource={() => {}} />);
    expect(html).toContain(`src="/api/artifacts/${info.artifact_id}?edit=1"`);
    expect(html).toContain(`sandbox="allow-scripts allow-downloads"`);
    expect(html).not.toContain("allow-same-origin");
  });

  it("starts in edit mode with Save disabled until something changes", () => {
    const html = renderToStaticMarkup(<HtmlTextEditor info={info} onClose={() => {}} onEditSource={() => {}} />);
    expect(html).toMatch(/aria-pressed="true"[^>]*>Edit text</);
    expect(html).toMatch(/<button[^>]*disabled=""[^>]*>Save new version<\/button>/);
  });
});
