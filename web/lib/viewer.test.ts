import { describe, it, expect } from "vitest";
import {
  fileParamSearch,
  fullPageKind,
  isTextualContentType,
  isJSONContentType,
  prettyPrintJSON,
  resolvePackageEntry,
  textScaleFromParam,
} from "./viewer";
import { DIAGRAM_CONTENT_TYPE } from "./diagram";

describe("fullPageKind", () => {
  it("classifies images as image (so the full-page view renders <img>, not bytes)", () => {
    expect(fullPageKind("image/png")).toBe("image");
    expect(fullPageKind("image/jpeg")).toBe("image");
    expect(fullPageKind("image/svg+xml")).toBe("image");
  });
  it("classifies pdf as pdf", () => {
    expect(fullPageKind("application/pdf")).toBe("pdf");
  });
  it("classifies html and markdown as their text-rendered kinds", () => {
    expect(fullPageKind("text/html; charset=utf-8")).toBe("html");
    expect(fullPageKind("text/markdown")).toBe("markdown");
  });
  it("classifies plain text / json / yaml as text", () => {
    expect(fullPageKind("text/plain")).toBe("text");
    expect(fullPageKind("application/json")).toBe("text");
    expect(fullPageKind("application/yaml")).toBe("text");
  });
  it("classifies opaque bytes as binary (a download card, not a <pre> of bytes)", () => {
    expect(fullPageKind("application/zip")).toBe("binary");
    expect(fullPageKind("application/octet-stream")).toBe("binary");
  });
  it("classifies a diagram as diagram — full page shows the canvas, not its JSON", () => {
    expect(fullPageKind(DIAGRAM_CONTENT_TYPE)).toBe("diagram");
    expect(fullPageKind(`${DIAGRAM_CONTENT_TYPE}; charset=utf-8`)).toBe("diagram");
    // Other +json types have no special renderer and stay plain text.
    expect(fullPageKind("application/ld+json")).toBe("text");
  });
  it("treats +json structured-suffix types as textual, so they can be TEXT artifacts", () => {
    expect(isTextualContentType(DIAGRAM_CONTENT_TYPE)).toBe(true);
    expect(isTextualContentType("application/ld+json")).toBe(true);
    // The suffix rule must not leak into other structured suffixes: SVG is
    // +xml and stays an image.
    expect(isTextualContentType("image/svg+xml")).toBe(false);
  });
  it("never classifies image/pdf/binary as textual — the body must not be fetched as a string", () => {
    for (const ct of ["image/png", "application/pdf", "application/zip"]) {
      expect(isTextualContentType(ct)).toBe(false);
    }
  });
});

describe("isJSONContentType", () => {
  it("matches application/json, ignoring charset params", () => {
    expect(isJSONContentType("application/json")).toBe(true);
    expect(isJSONContentType("application/json; charset=utf-8")).toBe(true);
  });
  it("does not match other textual types", () => {
    expect(isJSONContentType("application/yaml")).toBe(false);
    expect(isJSONContentType("text/plain")).toBe(false);
  });
});

// prettyPrintJSON is the rendered (default) view for a JSON artifact: parsed
// and re-serialized with a 2-space indent. "Raw Source" bypasses this
// entirely and shows the artifact's stored body verbatim, so this function
// is only ever used for the rendered path.
describe("prettyPrintJSON", () => {
  it("reformats compact JSON with a 2-space indent", () => {
    expect(prettyPrintJSON('{"a":1,"b":[2,3]}')).toBe(
      '{\n  "a": 1,\n  "b": [\n    2,\n    3\n  ]\n}',
    );
  });
  it("falls back to the original text when the body isn't valid JSON", () => {
    const bad = "{not valid json";
    expect(prettyPrintJSON(bad)).toBe(bad);
  });
});

// resolvePackageEntry must resolve a ?file= path the SAME way the server's
// pkgzip.resolveCandidates does — otherwise a shared/hand-written extensionless
// or directory URL renders the wrong content type (octet-stream download instead
// of the HTML the server actually serves). This is the exact class of bug the
// helper closes.
describe("resolvePackageEntry", () => {
  const entries = [
    { path: "index.html", content_type: "text/html" },
    { path: "docs/guide.html", content_type: "text/html" },
    { path: "docs/index.html", content_type: "text/html" },
    { path: "notes.md", content_type: "text/markdown" },
    { path: "raw", content_type: "application/octet-stream" },
  ];
  it("exact match wins", () => {
    expect(resolvePackageEntry(entries, "notes.md")?.path).toBe("notes.md");
    expect(resolvePackageEntry(entries, "docs/guide.html")?.content_type).toBe("text/html");
  });
  it("resolves an extensionless path to <path>.html (the reported bug)", () => {
    const e = resolvePackageEntry(entries, "docs/guide");
    expect(e?.path).toBe("docs/guide.html");
    expect(e?.content_type).toBe("text/html");
  });
  it("resolves a directory path to <path>/index.html", () => {
    expect(resolvePackageEntry(entries, "docs/")?.path).toBe("docs/index.html");
  });
  it("resolves empty / undefined to index.html", () => {
    expect(resolvePackageEntry(entries, "")?.path).toBe("index.html");
    expect(resolvePackageEntry(entries, undefined)?.path).toBe("index.html");
  });
  it("strips a leading slash before matching", () => {
    expect(resolvePackageEntry(entries, "/notes.md")?.path).toBe("notes.md");
  });
  it("prefers the exact entry over its .html sibling", () => {
    const e = resolvePackageEntry(entries, "raw");
    expect(e?.path).toBe("raw");
    expect(e?.content_type).toBe("application/octet-stream");
  });
  it("returns undefined when nothing matches (genuinely missing)", () => {
    expect(resolvePackageEntry(entries, "nope/missing")).toBeUndefined();
  });
});

// fileParamSearch produces the shareable URL query for the shown package file.
// The intent it encodes: the address bar must reflect the current file WITHOUT
// losing other params (e.g. v=full), and the entry point stays a clean base URL
// (no redundant ?file=) so shared links look canonical. It's the single source
// of truth shared by the sidebar (normal view) and the in-content listener
// (full-page), so both yield identical URLs for the same file.
describe("fileParamSearch", () => {
  it("sets ?file= for a non-entry file, from a param-less URL", () => {
    expect(fileParamSearch("", "docs/guide.html", "index.html")).toBe("?file=docs%2Fguide.html");
  });
  it("preserves other params (v=full) and adds file", () => {
    expect(fileParamSearch("?v=full", "b.html", "index.html")).toBe("?v=full&file=b.html");
  });
  it("drops ?file= when the file IS the entry point (clean base URL)", () => {
    expect(fileParamSearch("?file=b.html", "index.html", "index.html")).toBe("");
    // ...while keeping any sibling params
    expect(fileParamSearch("?v=full&file=b.html", "index.html", "index.html")).toBe("?v=full");
  });
  it("drops ?file= for an empty or null path (never writes ?file=)", () => {
    expect(fileParamSearch("?file=b.html", "", "index.html")).toBe("");
    expect(fileParamSearch("?v=full&file=b.html", null, "index.html")).toBe("?v=full");
  });
  it("replaces an existing file param rather than duplicating it", () => {
    expect(fileParamSearch("?file=a.html", "b.html", "index.html")).toBe("?file=b.html");
  });
  it("still sets ?file= for the entry point when there is no entry point to compare", () => {
    // entryPoint null ⇒ nothing is 'the clean base', so every file is explicit.
    expect(fileParamSearch("", "index.html", null)).toBe("?file=index.html");
  });
});

// ?ts= is how an embedder (couch's artifact side panel) syncs the reader's own
// font-size preference into the chrome-less viewer. Unknown values must land on
// 1 — a stale or hand-typed link should render at today's size, not a surprise.
describe("textScaleFromParam", () => {
  it("maps the named steps, including the embed-only xs", () => {
    expect(textScaleFromParam({ ts: "xs" })).toBe(0.72);
    expect(textScaleFromParam({ ts: "sm" })).toBe(0.85);
    expect(textScaleFromParam({ ts: "md" })).toBe(1);
    expect(textScaleFromParam({ ts: "lg" })).toBe(1.15);
  });
  it("falls back to 1 for absent, empty, or unknown values", () => {
    expect(textScaleFromParam({})).toBe(1);
    expect(textScaleFromParam({ ts: "" })).toBe(1);
    expect(textScaleFromParam({ ts: "huge" })).toBe(1);
    expect(textScaleFromParam({ ts: "0.85" })).toBe(1);
  });
  it("takes the first value of a repeated param", () => {
    expect(textScaleFromParam({ ts: ["sm", "lg"] })).toBe(0.85);
  });
});
