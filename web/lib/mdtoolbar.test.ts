import { describe, expect, it } from "vitest";

import { applyMarkdownAction, type EditState } from "./mdtoolbar";

// A compact way to write "this text, with this bit selected". The | markers are
// stripped and become selStart/selEnd, so each case reads as what the user
// actually had highlighted.
function at(marked: string): EditState {
  const first = marked.indexOf("|");
  const second = marked.indexOf("|", first + 1);
  if (first === -1) throw new Error("no cursor marker");
  if (second === -1) {
    return { text: marked.replace("|", ""), selStart: first, selEnd: first };
  }
  return {
    text: marked.slice(0, first) + marked.slice(first + 1, second) + marked.slice(second + 1),
    selStart: first,
    selEnd: second - 1,
  };
}

// Renders a result back into the |-marked form so failures are readable.
function show(s: EditState): string {
  if (s.selStart === s.selEnd) return s.text.slice(0, s.selStart) + "|" + s.text.slice(s.selStart);
  return (
    s.text.slice(0, s.selStart) + "|" + s.text.slice(s.selStart, s.selEnd) + "|" + s.text.slice(s.selEnd)
  );
}

// Convention: after wrapping, the INNER text stays selected (not the markers) —
// same as GitHub's and Obsidian's toolbars. It's also what makes a second press
// toggle cleanly: the selection is then in the "markers just outside" shape that
// the unwrap path recognizes.
describe("inline wraps", () => {
  it("wraps a selection in bold and keeps the inner text selected", () => {
    expect(show(applyMarkdownAction(at("make |this| bold"), "bold"))).toBe("make **|this|** bold");
  });

  it("wraps a selection in italic", () => {
    expect(show(applyMarkdownAction(at("make |this| italic"), "italic"))).toBe("make *|this|* italic");
  });

  it("wraps a selection in inline code", () => {
    expect(show(applyMarkdownAction(at("call |foo()| here"), "code"))).toBe("call `|foo()|` here");
  });

  // The round trip that convention buys: bold then bold again is a no-op.
  it("round-trips bold on the selection it hands back", () => {
    const once = applyMarkdownAction(at("make |this| bold"), "bold");
    expect(applyMarkdownAction(once, "bold").text).toBe("make this bold");
  });

  // Toggling off matters more than it sounds: without it, clicking B twice
  // gives ****text**** which renders as literal asterisks, and the user has no
  // idea why. Second press must undo the first.
  it("unwraps when the selection is already bold", () => {
    expect(show(applyMarkdownAction(at("make |**this**| bold"), "bold"))).toBe("make |this| bold");
  });

  it("unwraps when the markers sit just outside the selection", () => {
    expect(show(applyMarkdownAction(at("make **|this|** bold"), "bold"))).toBe("make |this| bold");
  });

  it("does not mistake bold for italic when toggling", () => {
    // "**x**" is bold, not italic — italic must wrap it, not strip a layer.
    expect(applyMarkdownAction(at("|**x**|"), "italic").text).toBe("***x***");
  });

  // With no selection there is nothing to emphasize, so the useful behavior is
  // to drop in the markers and park the caret between them, ready to type.
  it("inserts empty markers and centers the caret when nothing is selected", () => {
    expect(show(applyMarkdownAction(at("a | b"), "bold"))).toBe("a **|** b");
  });
});

describe("line prefixes", () => {
  it("makes the current line a heading", () => {
    expect(show(applyMarkdownAction(at("|Title"), "h2"))).toBe("## |Title");
  });

  // Re-pressing H2 on an H2 line should not accrete hashes.
  it("replaces an existing heading level instead of stacking hashes", () => {
    expect(show(applyMarkdownAction(at("## |Title"), "h1"))).toBe("# |Title");
  });

  it("removes the heading when the same level is pressed again", () => {
    expect(show(applyMarkdownAction(at("## |Title"), "h2"))).toBe("|Title");
  });

  it("bullets every line of a multi-line selection", () => {
    expect(show(applyMarkdownAction(at("|one\ntwo\nthree|"), "bullet"))).toBe("|- one\n- two\n- three|");
  });

  it("numbers a multi-line selection sequentially", () => {
    expect(show(applyMarkdownAction(at("|one\ntwo\nthree|"), "ordered"))).toBe(
      "|1. one\n2. two\n3. three|",
    );
  });

  // Blank lines are left alone, so they must not consume a number either —
  // numbering off the raw line index gave the line after a blank "3." when
  // it is the second list item.
  it("numbers only non-blank lines, so a blank line does not skip a number", () => {
    expect(show(applyMarkdownAction(at("|one\n\ntwo|"), "ordered"))).toBe("|1. one\n\n2. two|");
  });

  it("un-bullets an already-bulleted selection", () => {
    expect(show(applyMarkdownAction(at("|- one\n- two|"), "bullet"))).toBe("|one\ntwo|");
  });

  it("quotes a multi-line selection", () => {
    expect(show(applyMarkdownAction(at("|one\ntwo|"), "quote"))).toBe("|> one\n> two|");
  });

  // A blank line inside a bulleted range should stay blank — "- " on an empty
  // line renders as an empty list item and breaks the list in two.
  it("skips blank lines when prefixing", () => {
    expect(show(applyMarkdownAction(at("|one\n\ntwo|"), "bullet"))).toBe("|- one\n\n- two|");
  });

  it("prefixes the line the caret sits in when there is no selection", () => {
    expect(show(applyMarkdownAction(at("one\ntw|o\nthree"), "bullet"))).toBe("one\n- tw|o\nthree");
  });
});

describe("link", () => {
  it("uses the selection as the link text and selects the empty target", () => {
    const out = applyMarkdownAction(at("see |the docs| now"), "link");
    expect(out.text).toBe("see [the docs]() now");
    // The caret lands inside the parens so the next keystroke types the URL.
    expect(out.text.slice(out.selStart, out.selEnd)).toBe("");
    expect(out.selStart).toBe("see [the docs](".length);
  });

  // Pasting a URL then hitting the link button is the other common order.
  it("treats a selected URL as the target and selects the placeholder text", () => {
    const out = applyMarkdownAction(at("see |https://example.com| now"), "link");
    expect(out.text).toBe("see [text](https://example.com) now");
    expect(out.text.slice(out.selStart, out.selEnd)).toBe("text");
  });

  it("inserts an empty link skeleton with nothing selected", () => {
    const out = applyMarkdownAction(at("see |"), "link");
    expect(out.text).toBe("see []()");
    expect(out.selStart).toBe("see [".length);
  });
});

describe("block inserts", () => {
  it("fences a multi-line selection as a code block", () => {
    expect(applyMarkdownAction(at("|a\nb|"), "codeblock").text).toBe("```\na\nb\n```");
  });

  it("inserts a table skeleton with the first cell selected", () => {
    const out = applyMarkdownAction(at("|"), "table");
    expect(out.text).toContain("| --- |");
    expect(out.text.split("\n").length).toBeGreaterThanOrEqual(3);
    expect(out.text.slice(out.selStart, out.selEnd).length).toBeGreaterThan(0);
  });

  // A rule needs its own line; inserting mid-paragraph would turn the
  // preceding text into a setext heading instead.
  it("puts a horizontal rule on its own line", () => {
    expect(applyMarkdownAction(at("para|"), "hr").text).toBe("para\n\n---\n\n");
  });
});
