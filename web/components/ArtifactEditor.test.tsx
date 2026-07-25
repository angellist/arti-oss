import { describe, it, expect, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import ArtifactEditor from "./ArtifactEditor";
import type { ArtifactInfo } from "@/lib/types";

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: () => {}, refresh: () => {} }),
}));

const info: ArtifactInfo = {
  artifact_id: "921bad68-9612-420a-8ce9-e8609ff17950",
  artifact_type: "TEXT",
  named_slug: "my-doc",
  version: 2,
  title: "My Doc",
  description: null,
  content_type: "text/markdown",
  size_bytes: 10,
  sha256: null,
  creator: "a@b.com",
  scopes: [],
  labels: [],
  allowed_access: ["*"],
  metadata: {},
  created_at: "2026-01-01T00:00:00Z",
  modified_at: "2026-01-01T00:00:00Z",
  deleted_at: null,
  url: "",
};

describe("ArtifactEditor", () => {
  it("shows the stored source VERBATIM in the textarea — no rendering, no pretty-printing", () => {
    const body = "# Raw <b>source</b> & such";
    const html = renderToStaticMarkup(
      <ArtifactEditor info={info} body={body} onClose={() => {}} />,
    );
    expect(html).toContain("# Raw &lt;b&gt;source&lt;/b&gt; &amp; such");
  });

  it("disables Save until the text is actually changed, and says what saving does", () => {
    const html = renderToStaticMarkup(
      <ArtifactEditor info={info} body="x" onClose={() => {}} />,
    );
    // Pristine editor: the save button is disabled (nothing to version yet).
    expect(html).toMatch(/<button[^>]*title="no changes yet"[^>]*disabled/);
    // The bar states the version contract up front.
    expect(html).toContain("s/my-doc");
    expect(html).toContain("stays unchanged");
  });
});
