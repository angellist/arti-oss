// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMentionMenu, insertMention, mentionHTML, mentionQueryAt, type MentionResult } from "./mentionMenu";

describe("mentionQueryAt", () => {
  const at = (s: string) => mentionQueryAt(s, s.length);

  it("finds the mention being typed", () => {
    expect(at("@ali")).toEqual({ start: 0, query: "ali" });
    expect(at("hey @ali")).toEqual({ start: 4, query: "ali" });
    expect(at("(@ali")).toEqual({ start: 1, query: "ali" });
    expect(at("hey @alice@ex")).toEqual({ start: 4, query: "alice@ex" });
  });

  it("does not fire on an address that is merely being typed or quoted", () => {
    // The whole point of the marker: "forwarded from bob@x.com" must not open a
    // menu offering to notify the x.com domain, and must not parse as a mention
    // server-side either. The two rules are the same rule.
    expect(at("forwarded from bob@x.com")).toBeNull();
    expect(at("bob@")).toBeNull();
    expect(at("bob@x")).toBeNull();
  });

  it("closes once the mention is finished", () => {
    // A space ends the token, which is exactly what insertMention appends.
    expect(at("@alice@x.com ")).toBeNull();
    expect(at("no marker here")).toBeNull();
  });

  it("respects the caret, not the end of the text", () => {
    expect(mentionQueryAt("@ali and more", 4)).toEqual({ start: 0, query: "ali" });
    expect(mentionQueryAt("@ali and more", 13)).toBeNull();
  });

  it("gives up on a run too long to be an address being picked", () => {
    expect(at("@" + "x".repeat(65))).toBeNull();
  });
});

describe("insertMention", () => {
  it("replaces the typed fragment and keeps what follows the caret", () => {
    const v = "hey @ali, thoughts?";
    const caret = 8; // just after "@ali"
    expect(insertMention(v, 4, caret, "alice@x.com")).toEqual({
      value: "hey @alice@x.com , thoughts?",
      caret: 17,
    });
  });

  it("ends the mention with a space so the next keystroke doesn't extend it", () => {
    expect(insertMention("@ali", 0, 4, "alice@x.com").value).toBe("@alice@x.com ");
  });
});

describe("mentionHTML", () => {
  const esc = (s: string) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c] as string);

  it("marks up a mention and escapes everything around it", () => {
    expect(mentionHTML("ping @alice@x.com <b>", esc)).toBe(
      'ping <span class="ac-mention">@alice@x.com</span> &lt;b&gt;',
    );
  });

  it("leaves a quoted address alone", () => {
    expect(mentionHTML("forwarded from bob@x.com", esc)).toBe("forwarded from bob@x.com");
  });

  it("does not swallow sentence punctuation into the address", () => {
    expect(mentionHTML("ask @alice@x.com.", esc)).toBe('ask <span class="ac-mention">@alice@x.com</span>.');
  });
});

describe("mention menu", () => {
  afterEach(() => {
    document.documentElement.innerHTML = "";
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  const setup = (res: Partial<MentionResult>, grantRead?: (email: string) => Promise<void>) => {
    vi.useFakeTimers();
    const ta = document.createElement("textarea");
    document.body.append(ta);
    const search = vi.fn(
      async (): Promise<MentionResult> => ({ people: [], can_grant: false, min_query: 2, ...res }),
    );
    const menu = createMentionMenu({ search, grantRead });
    menu.attach(ta);
    return { ta, menu, search };
  };

  const type = async (ta: HTMLTextAreaElement, value: string) => {
    ta.value = value;
    ta.setSelectionRange(value.length, value.length);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    await vi.advanceTimersByTimeAsync(200);
  };

  const rows = () => Array.from(document.querySelectorAll<HTMLElement>(".ac-mm-row"));

  it("lists suggestions and inserts the chosen address", async () => {
    const { ta, menu } = setup({ people: [{ email: "alice@x.com", can_read: true }] });
    await type(ta, "hey @ali");
    expect(rows().map((r) => r.textContent)).toEqual(["alice@x.com"]);

    const consumed = menu.key(new KeyboardEvent("keydown", { key: "Enter" }));
    expect(consumed).toBe(true); // else Enter would also send the comment
    expect(ta.value).toBe("hey @alice@x.com ");
    expect(document.querySelector<HTMLElement>(".ac-mm")!.style.display).toBe("none");
  });

  it("leaves keys alone when it is closed", () => {
    const { menu } = setup({});
    expect(menu.key(new KeyboardEvent("keydown", { key: "Enter" }))).toBe(false);
  });

  it("ignores a lookup that lands after the caret has moved on", async () => {
    // Two searches in flight and the slower one landing last would otherwise
    // put one query's people under a different query's caret — one Enter away
    // from mentioning the wrong person.
    vi.useFakeTimers();
    const ta = document.createElement("textarea");
    document.body.append(ta);
    let resolveFirst: (r: MentionResult) => void = () => {};
    const search = vi
      .fn()
      .mockImplementationOnce(() => new Promise<MentionResult>((r) => (resolveFirst = r)))
      .mockImplementation(async () => ({ people: [{ email: "bob@x.com", can_read: true }], can_grant: false, min_query: 2 }));
    createMentionMenu({ search }).attach(ta);

    await type(ta, "@al");
    await type(ta, "@bo");
    resolveFirst({ people: [{ email: "alice@x.com", can_read: true }], can_grant: false, min_query: 2 });
    await vi.advanceTimersByTimeAsync(0);

    expect(rows().map((r) => r.textContent)).toEqual(["bob@x.com"]);
  });

  it("hides people who can't read the doc from a caller who can't grant", async () => {
    const { ta } = setup({
      people: [
        { email: "alice@x.com", can_read: true },
        { email: "outsider@y.com", can_read: false },
      ],
      can_grant: false,
    });
    await type(ta, "@a");
    expect(rows().map((r) => r.querySelector(".ac-mm-email")!.textContent)).toEqual(["alice@x.com"]);
  });

  it("offers the owner an explicit grant instead of granting silently", async () => {
    const grantRead = vi.fn(async () => {});
    const { ta, menu } = setup(
      { people: [{ email: "outsider@y.com", can_read: false }], can_grant: true },
      grantRead,
    );
    await type(ta, "@out");
    expect(rows()[0].textContent).toContain("no access");

    menu.key(new KeyboardEvent("keydown", { key: "Enter" }));
    expect(grantRead).not.toHaveBeenCalled(); // picking asks, it does not grant
    expect(document.querySelector(".ac-mm-ask")!.textContent).toContain("read access?");

    document.querySelector<HTMLButtonElement>(".ac-mm-primary")!.click();
    await vi.advanceTimersByTimeAsync(0);
    expect(grantRead).toHaveBeenCalledWith("outsider@y.com");
    expect(ta.value).toBe("@outsider@y.com ");
  });

  it("writes no mention when the grant is refused", async () => {
    const grantRead = vi.fn(async () => {
      throw new Error("403");
    });
    const { ta, menu } = setup(
      { people: [{ email: "outsider@y.com", can_read: false }], can_grant: true },
      grantRead,
    );
    await type(ta, "@out");
    menu.key(new KeyboardEvent("keydown", { key: "Enter" }));
    document.querySelector<HTMLButtonElement>(".ac-mm-primary")!.click();
    await vi.advanceTimersByTimeAsync(0);

    expect(ta.value).toBe("@out");
    expect(document.querySelector(".ac-mm-ask")!.textContent).toContain("Couldn't grant access");
  });

  it("never offers a grant on a surface that has no grantRead", async () => {
    // The embed bundle: the server also reports can_grant=false there, but the
    // client must not depend on that — a served page can call the API itself.
    const { ta } = setup({ people: [{ email: "outsider@y.com", can_read: false }], can_grant: true });
    await type(ta, "@out");
    expect(rows()).toHaveLength(0);
  });

  it("takes Escape while open and lets it through once closed", async () => {
    const { ta, menu } = setup({ people: [{ email: "alice@x.com", can_read: true }] });
    await type(ta, "@ali");
    expect(menu.key(new KeyboardEvent("keydown", { key: "Escape" }))).toBe(true);
    // Closed now, so the key belongs to whatever is behind the menu again.
    expect(menu.key(new KeyboardEvent("keydown", { key: "Escape" }))).toBe(false);
  });

  it("keeps the composer usable when the directory fails", async () => {
    vi.useFakeTimers();
    const ta = document.createElement("textarea");
    document.body.append(ta);
    createMentionMenu({ search: async () => { throw new Error("500"); } }).attach(ta);
    await type(ta, "@ali");
    expect(rows()).toHaveLength(0);
    expect(ta.value).toBe("@ali");
  });
});
