// Pure logic behind the "New artifact" surfaces (/new/text, /new/diagram):
// what each kind is, how its slug is derived and de-duplicated, and what the
// single create POST carries. Kept framework-free so the rules that make the
// flow safe are unit-testable (see newartifact.test.ts).
//
// The governing principle for these pages: nothing is written until the user
// presses Create. So every value here is a *draft* — derived, displayed
// verbatim in an editable box, and only then submitted.

import type { CreateArtifactInput } from "./arti";
import { DIAGRAM_CONTENT_TYPE } from "./diagram";
import { bytesToBase64 } from "./upload";

// Longest slug we will generate. Nothing in the schema enforces this
// (named_slug is TEXT), but the pre-existing slugFromFilename settled on 50
// and a slug is a URL component people paste into Slack — keep them short.
const MAX_SLUG = 50;

// A generated timestamp suffix: -yymmdd-hhmm. Used to recognize (and replace)
// our own stamp so retries don't stack them.
const STAMP_RE = /-\d{6}-\d{4}$/;

export type NewKind = "text" | "diagram";

export interface KindSpec {
  // What the rail's NEW submenu calls it.
  label: string;
  // Both kinds are ordinary TEXT artifacts. That is the entire reason a
  // diagram gets slugs, versioning, compare, comments, access control and the
  // REST/MCP/CLI surface without a line of diagram-specific storage.
  artifactType: "TEXT";
  contentType: string;
  defaultTitle: string;
  // Base for the untouched-slug default, before the timestamp is appended.
  defaultSlugBase: string;
}

export const KINDS: Record<NewKind, KindSpec> = {
  text: {
    label: "Text (Markdown)",
    artifactType: "TEXT",
    contentType: "text/markdown",
    defaultTitle: "Untitled text",
    defaultSlugBase: "untitled-text",
  },
  diagram: {
    label: "Diagram",
    artifactType: "TEXT",
    contentType: DIAGRAM_CONTENT_TYPE,
    defaultTitle: "Untitled diagram",
    defaultSlugBase: "untitled-diagram",
  },
};

const pad = (n: number) => String(n).padStart(2, "0");

// stampSuffix renders a local wall-clock stamp as yymmdd-hhmm. Local, not UTC,
// because the author reads it as "when I made this" — and a UTC stamp on an
// evening edit shows tomorrow's date.
export function stampSuffix(d: Date): string {
  const yy = pad(d.getFullYear() % 100);
  return `${yy}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}`;
}

// slugify is the shared normalizer: lowercase, non-alphanumerics collapsed to
// single dashes, no leading/trailing dash, capped.
//
// Deliberately NOT reusing slugFromFilename(): that runs its input through
// baseNoExt(), which truncates at the last dot. Titles have no extension to
// strip, so "Q3 results vs. Q2" would silently lose " Q2".
function slugify(raw: string, cap = MAX_SLUG): string {
  return raw
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+/, "")
    .slice(0, cap)
    .replace(/-+$/, "");
}

// slugFromTitle derives the slug that shadows an untouched slug field.
export function slugFromTitle(title: string): string {
  return slugify(title);
}

// normalizeSlug cleans up what a user typed. Called on blur rather than per
// keystroke: rewriting mid-word fights the typist, but leaving "My Doc" in a
// box that will store "my-doc" is exactly the confirmation illusion these
// pages exist to avoid — so the user sees the normalized value before they
// ever reach Create.
export function normalizeSlug(raw: string): string {
  return slugify(raw.trim());
}

// defaultSlug is the slug for a draft nobody has named yet. The timestamp is
// what makes it safe: without it, every abandoned-and-retried "New diagram"
// competes for the same slug.
export function defaultSlug(kind: NewKind, now: Date): string {
  return `${KINDS[kind].defaultSlugBase}-${stampSuffix(now)}`;
}

// resolveSlug picks the slug a draft will actually be created with.
//
// Precedence: an explicitly typed slug wins; otherwise the slug shadows a
// user-typed title; otherwise it's the stamped default. A slug the user
// deliberately CLEARED stays empty — arti allows slugless artifacts (they just
// can't be versioned), so snapping back to a default would override an
// explicit choice.
//
// titleTouched, not "does the title yield a slug", is the discriminator: the
// default titles are themselves perfectly sluggable, so testing the title's
// content would let "Untitled diagram" shadow the stamp and hand every
// untouched draft the same colliding slug — the exact bug the stamp exists to
// prevent.
export function resolveSlug({
  slug,
  slugTouched,
  title,
  titleTouched,
  stampedDefault,
}: {
  slug: string;
  slugTouched: boolean;
  title: string;
  titleTouched: boolean;
  stampedDefault: string;
}): string {
  if (slugTouched) return normalizeSlug(slug);
  // A user-typed title carries a real name, so it no longer needs the
  // collision stamp; fall back to the stamp when it yields nothing sluggable.
  if (titleTouched) return slugFromTitle(title) || stampedDefault;
  return stampedDefault;
}

// conflictSuggestion is the slug pre-filled into the conflict dialog's box:
// the taken slug plus a stamp. Any stamp already on the tail is replaced
// rather than appended to, so retrying twice in one session doesn't produce
// "doc-260729-1426-260729-1430".
export function conflictSuggestion(slug: string, now: Date): string {
  const stamp = stampSuffix(now);
  const base = slug.replace(STAMP_RE, "");
  // Truncate the base — never the stamp — so the result stays within the cap
  // and still ends in a recognizable timestamp.
  const room = MAX_SLUG - (stamp.length + 1);
  return `${base.slice(0, room).replace(/-+$/, "")}-${stamp}`;
}

export interface Draft {
  kind: NewKind;
  title: string;
  slug: string;
  labels: string[];
  scopes: string[];
  body: string;
  access: string[];
  write: string[] | null;
  // Whether the user opened the access editor and changed anything. Untouched
  // drafts omit the access fields so the SERVER's default applies; sending our
  // displayed copy of that default would freeze today's default (['*']) into
  // every artifact ever created from the web.
  accessTouched: boolean;
}

// createInputFor builds the one POST that turns a draft into an artifact.
export function createInputFor(draft: Draft): CreateArtifactInput {
  const spec = KINDS[draft.kind];
  const input: CreateArtifactInput = {
    title: draft.title.trim() || spec.defaultTitle,
    content_type: spec.contentType,
    artifact_type: spec.artifactType,
    content_base64: bytesToBase64(new TextEncoder().encode(draft.body).buffer as ArrayBuffer),
    // The authoritative dupe guard. The live check as you type is advisory
    // only: latestVersionForSlug is access-filtered (so it can undercount a
    // slug you can't see) and check-then-create races a concurrent writer
    // regardless. ensure_new makes the server reject the collision (409).
    ensure_new: true,
  };
  if (draft.slug) input.named_slug = draft.slug;
  // Empty lists are omitted, never sent: [] would *clear* rather than default.
  if (draft.labels.length) input.labels = draft.labels;
  if (draft.scopes.length) input.scopes = draft.scopes;
  if (draft.accessTouched) {
    input.allowed_access = draft.access;
    // null is mirror mode (write follows read) and must stay ABSENT; an empty
    // array is the different, meaningful state "creator-only writes".
    if (draft.write !== null) input.allowed_write = draft.write;
  }
  return input;
}
