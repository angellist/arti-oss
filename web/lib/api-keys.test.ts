import { describe, it, expect, vi, afterEach } from "vitest";
import { listApiKeys, createApiKey, revokeApiKey } from "./arti";

// Tests run in Node (vitest environment: "node"), so arti.ts treats every call
// as server-side (typeof window === "undefined") and prepends SERVER_BASE
// ("http://localhost:8095"). Assertions use endsWith to be robust against the
// base URL while still verifying path + query string.

function jsonResp(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function noBodyResp(status = 204): Response {
  return {
    ok: true,
    status,
    // Real Response.json() rejects on an empty body — mirror that so this test
    // actually guards http()'s 204 short-circuit (a lenient mock hid the bug).
    json: async () => {
      throw new SyntaxError("Unexpected end of JSON input");
    },
    text: async () => "",
  } as unknown as Response;
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("listApiKeys", () => {
  it("GETs /api/keys without ?all by default", async () => {
    const keys = [{ id: "abc" }];
    const spy = vi.spyOn(global, "fetch").mockResolvedValue(jsonResp(keys));

    const result = await listApiKeys();

    expect(spy).toHaveBeenCalledOnce();
    const [url, init] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/keys$/);
    expect(url).not.toContain("all=true");
    // default (no explicit init) — method not set means GET
    expect((init as RequestInit)?.method).toBeUndefined();
    expect(result).toEqual(keys);
  });

  it("GETs /api/keys?all=true when all=true", async () => {
    const keys = [{ id: "abc" }, { id: "xyz" }];
    const spy = vi.spyOn(global, "fetch").mockResolvedValue(jsonResp(keys));

    await listApiKeys(true);

    const [url] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/keys\?all=true$/);
  });
});

describe("createApiKey", () => {
  it("POSTs /api/keys with name, scopes, ttl_days and returns CreatedApiKey", async () => {
    const created = { id: "new-id", key: "arti_upload_abc123", name: "ci" };
    const spy = vi.spyOn(global, "fetch").mockResolvedValue(jsonResp(created));

    const result = await createApiKey("ci", ["upload"], 90);

    expect(spy).toHaveBeenCalledOnce();
    const [url, init] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/keys$/);
    expect((init as RequestInit).method).toBe("POST");
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({
      name: "ci",
      scopes: ["upload"],
      ttl_days: 90,
    });
    expect(result).toEqual(created);
  });
});

describe("revokeApiKey", () => {
  it("DELETEs /api/keys/{id} for the given id", async () => {
    const spy = vi.spyOn(global, "fetch").mockResolvedValue(noBodyResp());

    await revokeApiKey("key-id-1");

    expect(spy).toHaveBeenCalledOnce();
    const [url, init] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/keys\/key-id-1$/);
    expect((init as RequestInit).method).toBe("DELETE");
  });

  it("encodes special characters in the id", async () => {
    const spy = vi.spyOn(global, "fetch").mockResolvedValue(noBodyResp());

    await revokeApiKey("id with/slashes");

    const [url] = spy.mock.calls[0] as [string, RequestInit];
    expect(url).toMatch(/\/api\/keys\/id%20with%2Fslashes$/);
  });
});
