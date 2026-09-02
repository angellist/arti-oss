// Client-side helpers for the upload modal: content-type guessing,
// artifact-type detection (incl. APP vs PACKAGE by scanning a zip's
// central directory for arti-app.json), and the byte/text encoders the
// modal needs. Kept framework-free so it's trivially unit-testable.

import { zipSync } from "fflate";
import type { ArtifactType } from "./types";

// Extension → MIME map, mirroring the arti CLI's guessMIME so a file
// uploaded from the web gets the same content_type it would from `arti add`.
const EXT_MIME: Record<string, string> = {
  md: "text/markdown",
  markdown: "text/markdown",
  html: "text/html",
  htm: "text/html",
  txt: "text/plain",
  text: "text/plain",
  log: "text/plain",
  json: "application/json",
  yaml: "application/yaml",
  yml: "application/yaml",
  js: "application/javascript",
  mjs: "application/javascript",
  py: "text/x-python",
  go: "text/x-go",
  ts: "text/x-typescript",
  csv: "text/csv",
  css: "text/css",
  md5: "text/plain",
  xml: "application/xml",
  pdf: "application/pdf",
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  zip: "application/zip",
};

export function extOf(name: string): string {
  const base = name.slice(name.lastIndexOf("/") + 1);
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(i + 1).toLowerCase() : "";
}

// guessContentType prefers our extension map, then the browser-reported
// type, then a binary default. Returns a value the server will accept.
export function guessContentType(file: File): string {
  const byExt = EXT_MIME[extOf(file.name)];
  if (byExt) return byExt;
  if (file.type) return file.type;
  return "application/octet-stream";
}

// baseContentType strips parameters and case, so `text/html; charset=utf-8`
// and `text/html` are one kind of document. Mirrors the server's own
// baseContentType, which decides whether a version changes the document's type.
export function baseContentType(ct: string): string {
  return ct.split(";")[0].trim().toLowerCase();
}

// isTextualContentType mirrors the server's rule for what may be a TEXT
// artifact: text/* plus a few application/* text formats.
export function isTextualContentType(ct: string): boolean {
  const base = baseContentType(ct);
  if (base.startsWith("text/")) return true;
  // Structured-suffix JSON (RFC 6839), e.g. the diagram type
  // application/vnd.arti.diagram+json, is text like plain JSON is.
  if (base.endsWith("+json")) return true;
  return ["application/json", "application/yaml", "application/javascript"].includes(base);
}

// zipEntryNames reads every stored file name from a zip's central directory
// WITHOUT decompressing — it only walks the central-directory headers.
// Returns [] on any parse failure or a zip64 directory (offset 0xFFFFFFFF),
// so callers safely fall back to treating the zip as a plain PACKAGE.
export function zipEntryNames(buf: ArrayBuffer): string[] {
  const n = buf.byteLength;
  if (n < 22) return [];
  const dv = new DataView(buf);
  // Locate the End Of Central Directory record (sig 0x06054b50) by scanning
  // back from the end; the trailing comment is at most 65535 bytes.
  const minStart = Math.max(0, n - (22 + 0xffff));
  let eocd = -1;
  for (let i = n - 22; i >= minStart; i--) {
    if (dv.getUint32(i, true) === 0x06054b50) {
      eocd = i;
      break;
    }
  }
  if (eocd < 0) return [];
  const count = dv.getUint16(eocd + 10, true);
  let off = dv.getUint32(eocd + 16, true); // central directory start offset
  if (off === 0xffffffff) return []; // zip64 — bail, treat as PACKAGE
  const dec = new TextDecoder();
  const names: string[] = [];
  for (let k = 0; k < count; k++) {
    if (off + 46 > n) return names;
    if (dv.getUint32(off, true) !== 0x02014b50) return names; // central file header
    const nameLen = dv.getUint16(off + 28, true);
    const extraLen = dv.getUint16(off + 30, true);
    const commentLen = dv.getUint16(off + 32, true);
    if (off + 46 + nameLen > n) return names;
    names.push(dec.decode(new Uint8Array(buf, off + 46, nameLen)));
    off += 46 + nameLen + extraLen + commentLen;
  }
  return names;
}

// zipIsApp decides APP vs PACKAGE the way the server will AFTER it flattens a
// single wrapping directory (pkgzip.FlattenSingleRoot): arti-app.json at the
// effective root → APP. Without mirroring the flatten here, a dir-wrapped app
// ("zip -r app.zip app/") would be detected as a plain PACKAGE in the modal
// and silently lose its APP treatment.
export function zipIsApp(buf: ArrayBuffer): boolean {
  // Mirror the server's pkgzip.isMacOSXJunk exactly (basename-based), incl.
  // AppleDouble `._*` resource forks — otherwise a root-level `._foo` makes
  // the client think the zip isn't single-root-wrapped (→ PACKAGE) while the
  // server drops the junk, flattens, and detects APP. Keeping them in sync
  // keeps the modal's type chip matching what the server will store.
  const isJunk = (p: string) => {
    if (p.startsWith("__MACOSX/")) return true;
    const base = p.slice(p.lastIndexOf("/") + 1);
    return base.startsWith("._") || base === ".DS_Store";
  };
  const files = zipEntryNames(buf).filter((p) => !p.endsWith("/") && !isJunk(p));
  if (files.length === 0) return false;
  // Find a single wrapping root, matching the server's rule.
  let root: string | null = null;
  let wrapped = true;
  for (const p of files) {
    const idx = p.indexOf("/");
    if (idx <= 0) {
      wrapped = false; // a root-level file → not wrapped
      break;
    }
    const seg = p.slice(0, idx);
    if (root === null) root = seg;
    else if (seg !== root) {
      wrapped = false; // multiple top-level dirs
      break;
    }
  }
  const prefix = wrapped && root ? `${root}/` : "";
  return files.some((p) => p === `${prefix}arti-app.json`);
}

export interface Detected {
  artifactType: ArtifactType;
  contentType: string;
}

// detectType decides the artifact_type + content_type for a picked file:
//   .zip with arti-app.json at the (effective) root → APP
//   .zip otherwise                                   → PACKAGE
//   textual content_type                             → TEXT
//   anything else                                    → ATTACHMENT
export function detectType(file: File, buf: ArrayBuffer): Detected {
  const ct = guessContentType(file);
  const isZip = extOf(file.name) === "zip" || ct === "application/zip";
  if (isZip) {
    return { artifactType: zipIsApp(buf) ? "APP" : "PACKAGE", contentType: "application/zip" };
  }
  if (isTextualContentType(ct)) return { artifactType: "TEXT", contentType: ct };
  return { artifactType: "ATTACHMENT", contentType: ct };
}

// bytesToBase64 encodes an ArrayBuffer to base64 in 32KB chunks so we don't
// blow the argument limit of String.fromCharCode on multi-MB files.
export function bytesToBase64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let binary = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
}

// sha256Hex hashes the bytes the same way the server records them, so a
// version upload can tell the user the file is byte-identical to what the slug
// already holds. Returns null wherever WebCrypto isn't available (an insecure
// origin, or a test environment) — the check is advisory, never a gate.
export async function sha256Hex(buf: ArrayBuffer): Promise<string | null> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle) return null;
  try {
    const digest = await subtle.digest("SHA-256", buf);
    return Array.from(new Uint8Array(digest))
      .map((b) => b.toString(16).padStart(2, "0"))
      .join("");
  } catch {
    return null;
  }
}

// textSample decodes up to `max` characters of UTF-8 from the head of the
// buffer, for the metadata auto-fill. Non-fatal decoding tolerates a byte
// boundary split mid-rune at the cut.
export function textSample(buf: ArrayBuffer, max = 1000): string {
  // Decode a little extra raw (UTF-8 is up to 4 bytes/char) then trim.
  const raw = buf.byteLength > max * 4 ? buf.slice(0, max * 4) : buf;
  const txt = new TextDecoder("utf-8", { fatal: false }).decode(raw);
  return txt.slice(0, max);
}

function baseNoExt(name: string): string {
  const base = name.slice(name.lastIndexOf("/") + 1).trim();
  const i = base.lastIndexOf(".");
  return i > 0 ? base.slice(0, i) : base;
}

// slugFromFilename mirrors the server's slugify for an immediate, editable
// default before the user (optionally) runs auto-fill.
export function slugFromFilename(name: string): string {
  return baseNoExt(name)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 50)
    .replace(/-+$/g, "");
}

// titleFromFilename turns "weekly_report-v2.md" into "Weekly Report V2".
export function titleFromFilename(name: string): string {
  const words = baseNoExt(name)
    .replace(/[_\-.]+/g, " ")
    .split(/\s+/)
    .filter(Boolean);
  if (words.length === 0) return "Untitled";
  return words.map((w) => w[0].toUpperCase() + w.slice(1)).join(" ");
}

// bundleFiles turns a drop/selection into a single upload. One file passes
// through unchanged; multiple files are zipped (flat, keyed by basename) into
// a .zip File so the existing PACKAGE upload path handles them — and since
// the zip is already flat, the server's single-root-dir flatten leaves it be.
// Duplicate basenames get a numeric suffix so none are silently dropped.
export async function bundleFiles(files: File[]): Promise<File> {
  if (files.length === 1) return files[0];
  const entries: Record<string, Uint8Array> = {};
  for (const f of files) {
    let name = f.name.slice(f.name.lastIndexOf("/") + 1) || "file";
    if (entries[name]) {
      const dot = name.lastIndexOf(".");
      const stem = dot > 0 ? name.slice(0, dot) : name;
      const ext = dot > 0 ? name.slice(dot) : "";
      let i = 2;
      while (entries[`${stem}-${i}${ext}`]) i++;
      name = `${stem}-${i}${ext}`;
    }
    entries[name] = new Uint8Array(await f.arrayBuffer());
  }
  const zipped = zipSync(entries, { level: 6 });
  const stem = files[0].name.replace(/\.[^./]+$/, "") || "bundle";
  return new File([zipped], `${stem}.zip`, { type: "application/zip" });
}

// humanBytes formats a size for the file chip.
export function humanBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
