import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// Guard against the bug class that made explicitly-sans text render SERIF.
//
// Tailwind's `font-sans` / `font-serif` / `font-mono` utilities emit exactly
// `font-family: var(--font-sans)` etc. If such a token is defined as a bare
// `var(--font-geist-sans)` and that webfont is unavailable (not yet loaded,
// blocked, or the Next font variable unset in some render context), the family
// list resolves to nothing and the browser falls back to its own default —
// Times / Liberation Serif. Verified with CDP `CSS.getPlatformFontsForNode`:
// a bare-var declaration rendered in Liberation Serif, byte-identical to
// `font-family: serif`.
//
// So every font token must END in a generic family, which can never fail.
const css = readFileSync(join(__dirname, "..", "app", "globals.css"), "utf8");

const tokenValue = (name: string): string => {
  // Grab `--font-x: ...;` allowing a multi-line value.
  const m = new RegExp(`--${name}:\\s*([^;]+);`).exec(css);
  expect(m, `--${name} should be defined in globals.css`).toBeTruthy();
  return m![1].replace(/\s+/g, " ").trim();
};

describe("font tokens always end in a generic family", () => {
  it.each([
    ["font-sans", /(^|,\s*)sans-serif$/],
    ["font-serif", /(^|,\s*)serif$/],
    ["font-mono", /(^|,\s*)monospace$/],
  ])("--%s falls back to a generic family", (name, generic) => {
    const value = tokenValue(name);
    expect(value, `--${name} = ${value}`).toMatch(generic);
    // A token whose ONLY entry is a custom-property reference is the exact
    // shape that silently degrades to the browser default serif.
    expect(value).not.toMatch(/^var\([^)]+\)$/);
  });

  it("body keeps its own explicit fallback chain", () => {
    expect(css).toMatch(/font-family:\s*var\(--font-geist-sans\),[^;]*sans-serif;/);
  });
});
