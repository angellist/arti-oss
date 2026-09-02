import { describe, it, expect, afterEach, vi } from "vitest";
import { ArtiError, buildListQS, fetchContent, shouldGzipUpload, createArtifact } from "./arti";

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

// An ArtiError's `message` is user-facing: a dozen components render it straight
// into the UI via `setErr(e instanceof Error ? e.message : String(e))`. So the
// server's `{detail, code}` envelope has to be unwrapped to its `detail` before
// it becomes a message. Most call sites in arti.ts used to throw
// `await resp.text()` instead, which put the whole JSON envelope on screen.
//
// fetchContent is one of those call sites and is exercised here as the stand-in
// for all of them, since they now share one `errorFrom` helper.
describe("failed requests surface the server's detail, never the raw envelope", () => {
  const respond = (body: string, init: { status: number; statusText: string }) => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(body, init)));
  };

  afterEach(() => vi.unstubAllGlobals());

  const failure = async (): Promise<ArtiError> => {
    try {
      await fetchContent("some-id");
    } catch (e) {
      return e as ArtiError;
    }
    throw new Error("expected fetchContent to reject");
  };

  it("unwraps {detail, code} to the detail alone", async () => {
    respond(JSON.stringify({ detail: "slug already exists", code: "slug_conflict" }), {
      status: 409,
      statusText: "Conflict",
    });
    const err = await failure();
    expect(err.status).toBe(409);
    expect(err.message).toBe("slug already exists");
    // The regression this guards: no JSON punctuation and no machine code
    // leaking into text a user reads.
    expect(err.message).not.toContain("{");
    expect(err.message).not.toContain("slug_conflict");
  });

  it("uses a short non-JSON body as-is (gateway/plain-text errors)", async () => {
    respond("request entity too large", { status: 413, statusText: "Payload Too Large" });
    expect((await failure()).message).toBe("request entity too large");
  });

  // A default nginx error page is ~142 characters, so it slips under any sane
  // length cutoff. Length is the wrong test — shape is. This also guards a
  // regression: the call sites that used to go through resp.json() already got
  // statusText for an HTML body, because parsing threw.
  it("falls back to the status text for a SHORT gateway error page", async () => {
    const nginx =
      "<html>\n<head><title>502 Bad Gateway</title></head>\n<body>\n" +
      "<center><h1>502 Bad Gateway</h1></center>\n<hr><center>nginx</center>\n</body>\n</html>";
    expect(nginx.length).toBeLessThan(200); // the point: it clears the cap
    respond(nginx, { status: 502, statusText: "Bad Gateway" });
    const err = await failure();
    expect(err.message).toBe("Bad Gateway");
    expect(err.message).not.toContain("<");
  });

  it("falls back to the status text for a truncated JSON body", async () => {
    respond('{"detail": "half a respons', { status: 500, statusText: "Internal Server Error" });
    expect((await failure()).message).toBe("Internal Server Error");
  });

  it("falls back to the status text rather than pasting a whole error page", async () => {
    respond(`<html><body>${"upstream connect error ".repeat(40)}</body></html>`, {
      status: 502,
      statusText: "Bad Gateway",
    });
    const err = await failure();
    expect(err.message).toBe("Bad Gateway");
    expect(err.message).not.toContain("<html>");
  });

  it("falls back to the status text on an empty body", async () => {
    respond("", { status: 403, statusText: "Forbidden" });
    expect((await failure()).message).toBe("Forbidden");
  });

  // HTTP/2 and HTTP/3 removed the reason phrase, so a browser reports an empty
  // statusText — which is how arti is served in production. Every test above
  // sets statusText explicitly because Node lets it, so they would all pass while
  // the real UI showed a blank error. These pin the fallback that matters.
  describe("over HTTP/2, where statusText is empty", () => {
    it("never produces a blank message when there is no usable detail", async () => {
      respond("<html><body>502</body></html>", { status: 502, statusText: "" });
      const err = await failure();
      expect(err.message).toBe("HTTP 502");
      expect(err.message).not.toBe("");
    });

    it("still prefers the server's detail when the envelope is present", async () => {
      respond(JSON.stringify({ detail: "not allowed to read this", code: "forbidden" }), {
        status: 403,
        statusText: "",
      });
      expect((await failure()).message).toBe("not allowed to read this");
    });

    it("never produces a blank message on an empty body", async () => {
      respond("", { status: 504, statusText: "" });
      expect((await failure()).message).toBe("HTTP 504");
    });
  });

  // A body that parses as JSON is machine output. Only our envelope's `detail`
  // is written for a human, so every other JSON shape has to fall through to the
  // status line — otherwise the fix just swaps one kind of raw payload on screen
  // for another, and the null/non-string cases surface as the literal "null" or
  // "[object Object]".
  it("does not paste a proxy's own JSON body on screen", async () => {
    respond(JSON.stringify({ message: "upstream connect failure" }), {
      status: 502,
      statusText: "Bad Gateway",
    });
    const err = await failure();
    expect(err.message).toBe("Bad Gateway");
    expect(err.message).not.toContain("{");
  });

  it("never shows the literal string null for a JSON null body", async () => {
    respond("null", { status: 500, statusText: "Internal Server Error" });
    expect((await failure()).message).toBe("Internal Server Error");
  });

  it("never shows [object Object] when detail is not a string", async () => {
    respond(JSON.stringify({ detail: { nested: "oops" }, code: "weird" }), {
      status: 500,
      statusText: "Internal Server Error",
    });
    const err = await failure();
    expect(err.message).toBe("Internal Server Error");
    expect(err.message).not.toContain("object");
  });
});

describe("shouldGzipUpload", () => {
  it("compresses textual content the WAF <script> rule can false-match", () => {
    for (const ct of [
      "text/html",
      "text/html; charset=utf-8",
      "application/javascript",
      "text/markdown",
      "application/json",
      "image/svg+xml",
    ]) {
      expect(shouldGzipUpload(ct)).toBe(true);
    }
  });

  it("leaves already-compressed or binary uploads uncompressed", () => {
    for (const ct of ["application/zip", "image/png", "application/pdf"]) {
      expect(shouldGzipUpload(ct)).toBe(false);
    }
  });
});

describe("createArtifact gzips textual uploads", () => {
  afterEach(() => vi.restoreAllMocks());

  it("sends Content-Encoding: gzip and a gzip-magic body for HTML", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ artifact_id: "x" }), { status: 201 }),
    );
    await createArtifact({
      title: "t",
      content_type: "text/html",
      artifact_type: "TEXT",
      content_base64: btoa("<script>alert(1)</script>"),
    });
    const init = spy.mock.calls[0][1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["Content-Encoding"]).toBe("gzip");
    const bytes = new Uint8Array(init.body as ArrayBuffer);
    // gzip magic number 0x1f 0x8b
    expect(bytes[0]).toBe(0x1f);
    expect(bytes[1]).toBe(0x8b);
  });

  it("sends a plain JSON body (no Content-Encoding) for a zip package", async () => {
    const spy = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ artifact_id: "x" }), { status: 201 }),
    );
    await createArtifact({
      title: "t",
      content_type: "application/zip",
      artifact_type: "PACKAGE",
      content_base64: "UEsDBA==",
    });
    const init = spy.mock.calls[0][1] as RequestInit;
    const headers = init.headers as Record<string, string>;
    expect(headers["Content-Encoding"]).toBeUndefined();
    expect(typeof init.body).toBe("string");
  });
});
