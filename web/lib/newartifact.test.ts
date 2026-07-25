import { describe, expect, it } from "vitest";

import {
  KINDS,
  conflictSuggestion,
  createInputFor,
  defaultSlug,
  normalizeSlug,
  resolveSlug,
  slugFromTitle,
  stampSuffix,
} from "./newartifact";

// 2026-07-29 14:26 local — the moment in the screenshot that prompted this
// feature. Constructed with the local-time ctor on purpose: the suffix is a
// human-facing "when I made this", so it must read as the author's wall clock,
// not UTC (which is a day ahead for evening PT edits).
const NOW = new Date(2026, 6, 29, 14, 26);

describe("stampSuffix", () => {
  it("formats local wall-clock time as yymmdd-hhmm", () => {
    expect(stampSuffix(NOW)).toBe("260729-1426");
  });

  // Zero-padding is the whole reason this is a function and not a template
  // literal: an unpadded month/hour sorts wrong and reads as a typo.
  it("zero-pads every field", () => {
    expect(stampSuffix(new Date(2026, 0, 5, 9, 3))).toBe("260105-0903");
  });

  it("pads midnight to 0000 rather than dropping it", () => {
    expect(stampSuffix(new Date(2026, 11, 31, 0, 0))).toBe("261231-0000");
  });
});

describe("slugFromTitle", () => {
  it("lowercases and dash-joins", () => {
    expect(slugFromTitle("My Design Doc")).toBe("my-design-doc");
  });

  // Regression guard. The pre-existing slugFromFilename() runs titles through
  // baseNoExt(), which truncates at the last dot — so "Q3 results vs. Q2"
  // silently lost its trailing " Q2". A title is not a filename and has no
  // extension to strip, so slugFromTitle must keep every word.
  it("keeps text after a period instead of treating it as an extension", () => {
    expect(slugFromTitle("Q3 results vs. Q2")).toBe("q3-results-vs-q2");
  });

  it("collapses runs of punctuation and trims edge dashes", () => {
    expect(slugFromTitle("  **Hello** — world!!  ")).toBe("hello-world");
  });

  it("drops characters that cannot appear in a slug", () => {
    expect(slugFromTitle("café ☕ notes")).toBe("caf-notes");
  });

  it("returns empty for a title with nothing sluggable in it", () => {
    expect(slugFromTitle("!!!")).toBe("");
    expect(slugFromTitle("")).toBe("");
  });

  it("caps length and never ends on a dash", () => {
    const slug = slugFromTitle("a".repeat(40) + " " + "b".repeat(40));
    expect(slug.length).toBeLessThanOrEqual(50);
    expect(slug.endsWith("-")).toBe(false);
  });
});

describe("defaultSlug", () => {
  // The untouched case is the one that silently collided before: every
  // abandoned-then-retried "New diagram" wanted the same slug. The stamp is
  // what makes the default safe without asking the user anything.
  it("stamps the kind's base slug", () => {
    expect(defaultSlug("diagram", NOW)).toBe("untitled-diagram-260729-1426");
    expect(defaultSlug("text", NOW)).toBe("untitled-text-260729-1426");
  });
});

describe("normalizeSlug", () => {
  // Normalization runs on blur, not per keystroke: rewriting mid-word fights
  // the typist, but leaving "My Doc" in a box that will store "my-doc" is the
  // confirmation illusion we are removing everywhere else.
  it("normalizes what a user typed", () => {
    expect(normalizeSlug("My Doc")).toBe("my-doc");
    expect(normalizeSlug("  spaced-out  ")).toBe("spaced-out");
    expect(normalizeSlug("Trailing---")).toBe("trailing");
  });

  it("leaves an already-valid slug byte-identical", () => {
    expect(normalizeSlug("already-fine-123")).toBe("already-fine-123");
  });

  it("maps an unsluggable string to empty (a slugless artifact)", () => {
    expect(normalizeSlug("???")).toBe("");
  });
});

describe("resolveSlug", () => {
  const stamped = defaultSlug("diagram", NOW);

  it("uses the stamped default while neither field is touched", () => {
    expect(
      resolveSlug({
        slug: "",
        slugTouched: false,
        title: "Untitled diagram",
        titleTouched: false,
        stampedDefault: stamped,
      }),
    ).toBe("untitled-diagram-260729-1426");
  });

  // The discriminator has to be "did the user type a title", not "does the
  // title yield a slug": the default titles ARE sluggable, so content-testing
  // would let "Untitled diagram" shadow the stamp and hand every untouched
  // draft the same colliding slug.
  it("keeps the stamp even though the default title is itself sluggable", () => {
    expect(
      resolveSlug({
        slug: "",
        slugTouched: false,
        title: "Untitled text",
        titleTouched: false,
        stampedDefault: defaultSlug("text", NOW),
      }),
    ).toBe("untitled-text-260729-1426");
  });

  // Once there is a real title the slug should follow it — and drop the
  // timestamp, because a titled doc has a meaningful name and the stamp was
  // only ever a collision guard for the anonymous case.
  it("tracks the title once typed, without the timestamp", () => {
    expect(
      resolveSlug({
        slug: "",
        slugTouched: false,
        title: "Payments Rearchitecture",
        titleTouched: true,
        stampedDefault: stamped,
      }),
    ).toBe("payments-rearchitecture");
  });

  it("prefers an explicitly typed slug over the title", () => {
    expect(
      resolveSlug({
        slug: "hand-picked",
        slugTouched: true,
        title: "Some Other Title",
        titleTouched: true,
        stampedDefault: stamped,
      }),
    ).toBe("hand-picked");
  });

  // Clearing the slug is a deliberate act meaning "no slug" — arti allows
  // slugless artifacts, they just can't be versioned. It must NOT silently
  // snap back to the title or the default.
  it("honors a deliberately cleared slug as slugless", () => {
    expect(
      resolveSlug({
        slug: "",
        slugTouched: true,
        title: "Has A Title",
        titleTouched: true,
        stampedDefault: stamped,
      }),
    ).toBe("");
  });

  it("falls back to the stamped default when the typed title is not sluggable", () => {
    expect(
      resolveSlug({ slug: "", slugTouched: false, title: "!!!", titleTouched: true, stampedDefault: stamped }),
    ).toBe("untitled-diagram-260729-1426");
  });
});

describe("conflictSuggestion", () => {
  it("appends a stamp to the taken slug", () => {
    expect(conflictSuggestion("design-doc", NOW)).toBe("design-doc-260729-1426");
  });

  // Retrying twice in one session must not pile up stamps
  // ("doc-260729-1426-260729-1430"): replace the trailing one instead.
  it("replaces an existing trailing stamp rather than stacking one", () => {
    expect(conflictSuggestion("design-doc-260729-1426", new Date(2026, 6, 29, 14, 30))).toBe(
      "design-doc-260729-1430",
    );
  });

  it("does not mistake an ordinary numeric tail for a stamp", () => {
    expect(conflictSuggestion("q3-2026", NOW)).toBe("q3-2026-260729-1426");
    expect(conflictSuggestion("v2-1426", NOW)).toBe("v2-1426-260729-1426");
  });

  it("keeps the suggestion within the slug length cap", () => {
    const suggestion = conflictSuggestion("a".repeat(50), NOW);
    expect(suggestion.length).toBeLessThanOrEqual(50);
    expect(suggestion.endsWith("-260729-1426")).toBe(true);
  });
});

describe("KINDS", () => {
  // A diagram is an ordinary TEXT artifact — only its content_type differs.
  // If this ever drifts to a bespoke artifact_type it loses slugs, versioning,
  // compare, comments and the whole REST/MCP/CLI surface for free.
  it("maps both kinds to TEXT and differs only by content type", () => {
    expect(KINDS.text.artifactType).toBe("TEXT");
    expect(KINDS.diagram.artifactType).toBe("TEXT");
    expect(KINDS.text.contentType).toBe("text/markdown");
    expect(KINDS.diagram.contentType).toBe("application/vnd.arti.diagram+json");
  });
});

describe("createInputFor", () => {
  const base = {
    kind: "text" as const,
    title: "My Doc",
    slug: "my-doc",
    labels: ["design-doc"],
    scopes: [],
    body: "# hello",
    access: ["*"],
    write: null,
    accessTouched: false,
  };

  it("builds the create POST for a markdown draft", () => {
    const input = createInputFor(base);
    expect(input.title).toBe("My Doc");
    expect(input.named_slug).toBe("my-doc");
    expect(input.artifact_type).toBe("TEXT");
    expect(input.content_type).toBe("text/markdown");
    expect(input.labels).toEqual(["design-doc"]);
  });

  // ensure_new is the authoritative dupe guard. The live typeahead check is
  // only advisory: latestVersionForSlug is access-filtered (so it can
  // undercount) and check-then-create races a concurrent writer regardless.
  it("always asserts ensure_new so the server is the real gate", () => {
    expect(createInputFor(base).ensure_new).toBe(true);
  });

  // Omitting the field lets the server apply its own default (['*']).
  // Sending our displayed copy of that default would freeze today's default
  // into every artifact created from the web.
  it("omits access entirely when the user never touched it", () => {
    const input = createInputFor(base);
    expect("allowed_access" in input).toBe(false);
    expect("allowed_write" in input).toBe(false);
  });

  it("sends access and write once the user has touched them", () => {
    const input = createInputFor({
      ...base,
      accessTouched: true,
      access: ["*@example.com", "group:eng"],
      write: ["group:eng"],
    });
    expect(input.allowed_access).toEqual(["*@example.com", "group:eng"]);
    expect(input.allowed_write).toEqual(["group:eng"]);
  });

  // An empty (non-nil) write list means creator-only writes, which is a real
  // and different state from mirror mode — it must survive as [].
  it("distinguishes creator-only writes from mirror mode", () => {
    const creatorOnly = createInputFor({ ...base, accessTouched: true, write: [] });
    expect(creatorOnly.allowed_write).toEqual([]);

    const mirror = createInputFor({ ...base, accessTouched: true, write: null });
    expect("allowed_write" in mirror).toBe(false);
  });

  it("omits a slug rather than sending an empty one", () => {
    const input = createInputFor({ ...base, slug: "" });
    expect(input.named_slug).toBeUndefined();
  });

  it("omits empty label and scope lists so nothing is cleared", () => {
    const input = createInputFor({ ...base, labels: [], scopes: [] });
    expect(input.labels).toBeUndefined();
    expect(input.scopes).toBeUndefined();
  });

  it("falls back to the kind's default title when the field is blank", () => {
    expect(createInputFor({ ...base, title: "   " }).title).toBe("Untitled text");
  });

  it("base64-encodes the body as UTF-8", () => {
    const input = createInputFor({ ...base, body: "héllo ☕" });
    expect(Buffer.from(input.content_base64, "base64").toString("utf8")).toBe("héllo ☕");
  });
});
