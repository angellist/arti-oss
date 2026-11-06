// Where a character offset sits inside a textarea, which the DOM does not
// expose. A hidden mirror div with the textarea's text metrics lays out the text
// before the offset, and a marker span after it reports the position.

const COPIED = [
  "boxSizing",
  "paddingTop",
  "paddingRight",
  "paddingBottom",
  "paddingLeft",
  "fontFamily",
  "fontSize",
  "fontStyle",
  "fontWeight",
  "fontVariant",
  "fontStretch",
  "lineHeight",
  "letterSpacing",
  "wordSpacing",
  "textTransform",
  "textIndent",
  "tabSize",
] as const;

/** Offset box of a caret, relative to the textarea's border box, after scroll. */
export interface CaretBox {
  left: number;
  top: number;
  height: number;
}

// mirrorStyle — copies the textarea's text metrics onto `div` so its text wraps
// exactly as the textarea's does. The border is dropped and the content width
// pinned to the textarea's, so offsets come out relative to its padding box.
export function mirrorStyle(el: HTMLTextAreaElement, div: HTMLElement) {
  const cs = getComputedStyle(el);
  for (const k of COPIED) div.style[k] = cs[k];
  div.style.boxSizing = "content-box";
  div.style.border = "0";
  div.style.whiteSpace = "pre-wrap";
  div.style.overflowWrap = "break-word";
  div.style.wordBreak = cs.wordBreak;
  const padX = parseFloat(cs.paddingLeft) + parseFloat(cs.paddingRight);
  div.style.width = `${Math.max(0, el.clientWidth - padX)}px`;
}

// caretBox — where offset `pos` of the textarea's value is drawn, or null when it
// is scrolled out of the visible box.
export function caretBox(el: HTMLTextAreaElement, pos: number): CaretBox | null {
  const div = document.createElement("div");
  mirrorStyle(el, div);
  div.style.position = "absolute";
  div.style.visibility = "hidden";
  div.style.top = "0";
  div.style.left = "-9999px";
  div.textContent = el.value.slice(0, pos);
  const mark = document.createElement("span");
  // A zero-width space: an empty span has no line box to measure.
  mark.textContent = "​";
  div.appendChild(mark);
  document.body.appendChild(div);
  const top = mark.offsetTop + el.clientTop - el.scrollTop;
  const left = mark.offsetLeft + el.clientLeft - el.scrollLeft;
  const height = mark.offsetHeight;
  div.remove();
  if (top < el.clientTop || top + height > el.clientTop + el.clientHeight) return null;
  return { left, top, height };
}

// caretToEnd — the words of a new clip go after what is already there unless the
// user moves the caret while it records. Touch focuses only a box that already
// has focus: focusing opens the keyboard over the recording bar.
export function caretToEnd(el: HTMLTextAreaElement) {
  const coarse = window.matchMedia?.("(pointer: coarse)").matches;
  if (coarse && document.activeElement !== el) return;
  el.focus({ preventScroll: true });
  const end = el.value.length;
  el.setSelectionRange(end, end);
  el.scrollTop = el.scrollHeight;
}
