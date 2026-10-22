import { describe, it, expect } from "vitest";
import { filterKeys, keyStatus, ownersOf, scopeLabel, sortKeys } from "./apiKeyTable";
import type { ApiKey } from "./types";

const now = new Date("2026-10-05T12:00:00Z");

function key(over: Partial<ApiKey>): ApiKey {
  return {
    id: over.name ?? "id",
    name: "k",
    key_prefix: "arti_upload_x",
    owner_email: "a@example.com",
    scopes: ["upload"],
    created_at: "2026-09-01T00:00:00Z",
    expires_at: "2027-09-01T00:00:00Z",
    last_used_at: null,
    revoked_at: null,
    ...over,
  };
}

const active = key({ name: "active", owner_email: "b@example.com", last_used_at: "2026-10-01T00:00:00Z" });
const expired = key({ name: "expired", expires_at: "2026-09-02T00:00:00Z" });
const revoked = key({ name: "revoked", revoked_at: "2026-09-03T00:00:00Z", expires_at: "2026-09-02T00:00:00Z" });
const all = [active, expired, revoked];
const noMetrics = () => ({ sources: 0, docs: 0 });

describe("keyStatus", () => {
  it("prefers Revoked over Expired", () => {
    expect(keyStatus(revoked, now)).toBe("Revoked");
    expect(keyStatus(expired, now)).toBe("Expired");
    expect(keyStatus(active, now)).toBe("Active");
  });
});

describe("scopeLabel", () => {
  it("names the read access an upload key carries", () => {
    expect(scopeLabel("upload")).toBe("Read + Upload");
  });
  it("passes unknown and prototype names through", () => {
    expect(scopeLabel("future")).toBe("future");
    expect(scopeLabel("constructor")).toBe("constructor");
  });
});

describe("filterKeys", () => {
  it("filters by status", () => {
    expect(filterKeys(all, { owner: "", status: "active" }, now)).toEqual([active]);
    expect(filterKeys(all, { owner: "", status: "expired" }, now)).toEqual([expired]);
    expect(filterKeys(all, { owner: "", status: "revoked" }, now)).toEqual([revoked]);
    expect(filterKeys(all, { owner: "", status: "all" }, now)).toEqual(all);
  });
  it("filters by owner and combines with status", () => {
    expect(filterKeys(all, { owner: "a@example.com", status: "all" }, now)).toEqual([expired, revoked]);
    expect(filterKeys(all, { owner: "a@example.com", status: "active" }, now)).toEqual([]);
  });
});

describe("filterKeys query", () => {
  it("matches name or owner, case-insensitive, trimmed", () => {
    expect(filterKeys(all, { owner: "", status: "all", query: " EXP" }, now)).toEqual([expired]);
    expect(filterKeys(all, { owner: "", status: "all", query: "b@exa" }, now)).toEqual([active]);
    expect(filterKeys(all, { owner: "", status: "all", query: "" }, now)).toEqual(all);
    expect(filterKeys(all, { owner: "", status: "active", query: "expired" }, now)).toEqual([]);
  });
});

describe("owner case", () => {
  it("folds case in ownersOf and the owner filter", () => {
    const upper = key({ name: "upper", owner_email: "A@Example.com" });
    expect(ownersOf([...all, upper])).toEqual(["a@example.com", "b@example.com"]);
    expect(filterKeys([upper], { owner: "a@example.com", status: "all" }, now)).toEqual([upper]);
  });
});

describe("ownersOf", () => {
  it("returns distinct sorted owners", () => {
    expect(ownersOf(all)).toEqual(["a@example.com", "b@example.com"]);
  });
});

describe("sortKeys", () => {
  it("sorts by name both ways", () => {
    expect(sortKeys(all, "name", "asc", noMetrics, now).map((k) => k.name)).toEqual(["active", "expired", "revoked"]);
    expect(sortKeys(all, "name", "desc", noMetrics, now).map((k) => k.name)).toEqual(["revoked", "expired", "active"]);
  });
  it("keeps never-used keys last in both directions", () => {
    const used = key({ name: "used2", last_used_at: "2026-09-15T00:00:00Z" });
    const keys = [expired, active, used];
    expect(sortKeys(keys, "last_used", "desc", noMetrics, now).map((k) => k.name)).toEqual(["active", "used2", "expired"]);
    expect(sortKeys(keys, "last_used", "asc", noMetrics, now).map((k) => k.name)).toEqual(["used2", "active", "expired"]);
  });
  it("sorts by usage metrics", () => {
    const docs: Record<string, number> = { active: 1, expired: 40, revoked: 5 };
    const m = (k: ApiKey) => ({ sources: 0, docs: docs[k.name] });
    expect(sortKeys(all, "docs", "desc", m, now).map((k) => k.name)).toEqual(["expired", "revoked", "active"]);
  });
  it("is stable on ties", () => {
    const a = key({ name: "x1" });
    const b = key({ name: "x2" });
    expect(sortKeys([b, a], "scope", "desc", noMetrics, now)).toEqual([b, a]);
  });
});
