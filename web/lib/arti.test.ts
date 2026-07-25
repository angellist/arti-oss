import { describe, it, expect } from "vitest";
import { buildListQS } from "./arti";

describe("buildListQS all_versions", () => {
  it("sets all_versions=true when requested", () => {
    expect(buildListQS({ all_versions: true }).get("all_versions")).toBe("true");
  });

  it("omits all_versions by default so the query stays latest-per-slug", () => {
    expect(buildListQS({}).has("all_versions")).toBe(false);
  });

  it("forwards all_versions and include_archived together", () => {
    const qs = buildListQS({ all_versions: true, include_archived: true });
    expect(qs.get("all_versions")).toBe("true");
    expect(qs.get("include_archived")).toBe("true");
  });
});
