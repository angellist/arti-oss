import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { DARK, LIGHT, SYSTEM_THEME, THEMES, resolveTheme, themeBootstrapScript } from "./appearance";

const themesCss = readFileSync(join(__dirname, "..", "app", "themes.css"), "utf8");

const block = (selector: string): string => {
  const m = new RegExp(`\\[${selector}\\]\\s*\\{([^}]*)\\}`).exec(themesCss);
  expect(m, `${selector} should have a block`).toBeTruthy();
  return m![1];
};

const token = (body: string, name: string): string => {
  const m = new RegExp(`--${name}:\\s*([^;]+);`).exec(body);
  expect(m, `--${name} should be set`).toBeTruthy();
  return m![1].trim();
};

// Perceived lightness of a #rrggbb, enough to tell a dark ramp from a light one.
const lightness = (hex: string): number => {
  const n = parseInt(hex.slice(1), 16);
  return (0.299 * ((n >> 16) & 255) + 0.587 * ((n >> 8) & 255) + 0.114 * (n & 255)) / 255;
};

const RAMP = [50, 100, 200, 300, 400, 500, 600, 700, 800, 900, 950];

describe("themes", () => {
  it.each([LIGHT, DARK])("%s states the whole ramp", (theme) => {
    const body = block(`data-theme="${theme}"`);
    for (const step of RAMP) expect(token(body, `color-neutral-${step}`)).toMatch(/^#[0-9a-f]{6}$/);
    expect(token(body, "color-white")).toMatch(/^#[0-9a-f]{6}$/);
  });

  // The ramp's roles are fixed (see app/themes.css): -50 is a surface and -900
  // is heading text. Dark inverts the lightness while keeping those roles, so a
  // ramp pasted the wrong way round renders text on text.
  it.each([
    [LIGHT, 1],
    [DARK, -1],
  ])("%s runs the right way", (theme, sign) => {
    const body = block(`data-theme="${theme}"`);
    const surface = lightness(token(body, "color-neutral-50"));
    const ink = lightness(token(body, "color-neutral-900"));
    expect(Math.sign(surface - ink)).toBe(sign);
    // Text has to separate from the surface it sits on, in either direction.
    expect(Math.abs(surface - ink)).toBeGreaterThan(0.5);
  });

  it("flips a tinted pair for dark and restates it for light", () => {
    // bg-rose-100 + text-rose-900 is a failure chip: light tint, dark text in
    // light mode, and the other way round in dark, with no component change.
    const dark = block(`data-theme="${DARK}"`);
    const light = block(`data-theme="${LIGHT}"`);
    expect(token(dark, "color-rose-100")).toBe(token(light, "color-rose-900"));
    expect(token(dark, "color-rose-900")).toBe(token(light, "color-rose-100"));
  });
});

describe("resolveTheme", () => {
  it("resolves System against the OS", () => {
    expect(resolveTheme(SYSTEM_THEME, true)).toBe(DARK);
    expect(resolveTheme(SYSTEM_THEME, false)).toBe(LIGHT);
  });

  it("keeps an explicit choice whatever the OS says", () => {
    expect(resolveTheme(LIGHT, true)).toBe(LIGHT);
    expect(resolveTheme(DARK, false)).toBe(DARK);
  });

  it("reads an unknown or missing value as System", () => {
    expect(resolveTheme("nord", true)).toBe(DARK);
    expect(resolveTheme(null, false)).toBe(LIGHT);
  });
});

describe("bootstrap script", () => {
  const run = (stored: string | null, prefersDark: boolean) => {
    const html: Record<string, string> = {};
    const listeners: Array<() => void> = [];
    const window = {
      localStorage: { getItem: () => stored },
      matchMedia: () => ({
        matches: prefersDark,
        addEventListener: (_: string, fn: () => void) => listeners.push(fn),
      }),
      document: { documentElement: { setAttribute: (k: string, v: string) => { html[k] = v; } } },
    };
    new Function("window", `with(window){${themeBootstrapScript()}}`)(window);
    return { html, listeners };
  };

  it("offers exactly System, Light and Dark", () => {
    expect(THEMES.map((t) => t.id)).toEqual([SYSTEM_THEME, LIGHT, DARK]);
  });

  it("applies the stored choice before paint", () => {
    expect(run(LIGHT, true).html).toEqual({ "data-theme": LIGHT });
    expect(run(DARK, false).html).toEqual({ "data-theme": DARK });
  });

  it("follows the OS when nothing is stored, and keeps following it", () => {
    const { html, listeners } = run(null, true);
    expect(html).toEqual({ "data-theme": DARK });
    expect(listeners).toHaveLength(1);
  });
});
