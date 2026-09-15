// The viewer's theme: light, dark, or the OS setting. It lives in localStorage
// and is applied as one attribute on <html> (`data-theme`), from which
// app/themes.css overrides the Tailwind colour tokens every component already
// reads. Switching costs one attribute write and no re-render.

export interface ThemeDef {
  id: string;
  label: string;
  blurb: string;
}

export const SYSTEM_THEME = "system";
export const LIGHT = "light";
export const DARK = "dark";

export const THEMES: ThemeDef[] = [
  { id: SYSTEM_THEME, label: "System", blurb: "Follow the OS setting" },
  { id: LIGHT, label: "Light", blurb: "The shipped look" },
  { id: DARK, label: "Dark", blurb: "Dark chrome, same layout" },
];

const DARK_QUERY = "(prefers-color-scheme: dark)";
const KEY = "arti.theme";

// The stored value may be SYSTEM_THEME, which is not a CSS block; it resolves
// against the OS setting at apply time. Anything unknown reads as System.
export function resolveTheme(stored: string | null | undefined, prefersDark: boolean): string {
  if (stored === LIGHT || stored === DARK) return stored;
  return prefersDark ? DARK : LIGHT;
}

// One stored choice. An unreadable value reads as System rather than throwing,
// so a hand-edited key cannot leave the app unstyled.
function makePref() {
  const ids = THEMES.map((t) => t.id);
  const sanitize = (v: string | null | undefined) => (v && ids.includes(v) ? v : SYSTEM_THEME);
  // localStorage is an external store, so the picker reads it through
  // useSyncExternalStore rather than seeding state in an effect. `storage`
  // covers other tabs; the listener set covers this one.
  const listeners = new Set<() => void>();
  return {
    key: KEY,
    fallback: SYSTEM_THEME,
    read(): string {
      try {
        return sanitize(window.localStorage.getItem(KEY));
      } catch {
        return SYSTEM_THEME;
      }
    },
    apply(v: string): void {
      const resolved = resolveTheme(sanitize(v), window.matchMedia(DARK_QUERY).matches);
      document.documentElement.setAttribute("data-theme", resolved);
    },
    store(v: string): void {
      try {
        window.localStorage.setItem(KEY, sanitize(v));
      } catch {
        /* private mode / storage disabled — the choice still applies to this view */
      }
      listeners.forEach((l) => l());
    },
    subscribe(onChange: () => void): () => void {
      listeners.add(onChange);
      window.addEventListener("storage", onChange);
      return () => {
        listeners.delete(onChange);
        window.removeEventListener("storage", onChange);
      };
    },
  };
}

export const themePref = makePref();

// The attribute has to be set before first paint or every load flashes light,
// and a React effect is a frame too late. layout.tsx inlines this in <head>. It
// keeps following the OS while the tab stays open, which is what System means.
export function themeBootstrapScript(): string {
  const j = JSON.stringify;
  return (
    "(function(){try{" +
    `var q=matchMedia(${j(DARK_QUERY)});` +
    "function t(){" +
    `var v=localStorage.getItem(${j(KEY)});` +
    `if(v!==${j(LIGHT)}&&v!==${j(DARK)})v=q.matches?${j(DARK)}:${j(LIGHT)};` +
    'document.documentElement.setAttribute("data-theme",v);}' +
    "t();q.addEventListener('change',t);" +
    "}catch(_){}})()"
  );
}
