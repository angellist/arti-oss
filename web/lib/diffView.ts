// The diff layout preference (unified or side by side) for every ```diff block,
// kept in localStorage and applied as data-arti-diff-view on <html>, so CSS
// switches all blocks at once and server-rendered markdown needs no hydration.

const KEY = "arti.diffView";
const ATTR = "data-arti-diff-view";

export function diffViewBootstrapScript(): string {
  return `(function(){try{if(localStorage.getItem(${JSON.stringify(KEY)})==="split")document.documentElement.setAttribute(${JSON.stringify(ATTR)},"split");}catch(_){}})()`;
}

function apply(split: boolean) {
  const root = document.documentElement;
  if (split) root.setAttribute(ATTR, "split");
  else root.removeAttribute(ATTR);
}

function scrollerOf(el: HTMLElement): HTMLElement {
  for (let p = el.parentElement; p; p = p.parentElement) {
    const oy = getComputedStyle(p).overflowY;
    if ((oy === "auto" || oy === "scroll") && p.scrollHeight > p.clientHeight) return p;
  }
  return (document.scrollingElement as HTMLElement) ?? document.documentElement;
}

// One delegated listener serves every block on every markdown surface. Every
// block above the clicked one changes height, so the page scrolls by however far
// the button moved to keep it under the pointer.
export function installDiffViewToggle(): () => void {
  const onClick = (e: MouseEvent) => {
    const btn = (e.target as Element | null)?.closest<HTMLElement>("[data-arti-diff-toggle]");
    if (!btn) return;
    e.preventDefault();
    const before = btn.getBoundingClientRect().top;
    const split = document.documentElement.getAttribute(ATTR) !== "split";
    apply(split);
    try {
      localStorage.setItem(KEY, split ? "split" : "unified");
    } catch {
      /* storage disabled: the toggle still works for this page */
    }
    const moved = btn.getBoundingClientRect().top - before;
    if (moved) scrollerOf(btn).scrollBy(0, moved);
  };
  const onStorage = (e: StorageEvent) => {
    if (e.key === KEY) apply(e.newValue === "split");
  };
  document.addEventListener("click", onClick);
  window.addEventListener("storage", onStorage);
  return () => {
    document.removeEventListener("click", onClick);
    window.removeEventListener("storage", onStorage);
  };
}
