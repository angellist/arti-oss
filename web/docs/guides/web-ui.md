---
title: Using the web UI
order: 1
summary: Browse and filter the catalog, upload artifacts, view TEXT/HTML/PACKAGE artifacts, switch versions, go full-page, and comment.
---

# Using the web UI

The catalog at the root of the site is the home base: a left rail for finding
things, a main table of artifacts, and a viewer for each one. Everything the
[CLI](command-line.md) does, you can do here.

## The left rail

The rail on the left is where you search and filter. It runs in two modes.

### Search and filter (catalog mode)

Search lives in a **reveal-on-demand bar** at the top of the listing, not in the
rail. Open it with the **⌕ Search** item in the rail, press `/` anywhere on the
catalog, or just start typing while it's open. The full-width box takes free text
or `field:value` syntax; press **Enter** to run it, and **✕ close** to collapse
it. Two toggles ride alongside — **latest version only** and **show archived**.
See [Searching the catalog](search.md) for the full operator reference and
examples.

The rail itself holds the filter groups:

- **Type** — two families in one row. The uppercase chips (`TEXT`, `PACKAGE`,
  `APP`, `ATTACHMENT`) are artifact types; the lowercase ones (`markdown`,
  `diagram`, `html`, `json`, `image`) filter by *body* type, and are
  exactly the families the viewer renders differently. `all` clears it. When
  you're logged in, a `👤 Owned by Me` chip filters to artifacts you created.
  These lowercase names also work as `type:` search tokens (`type:diagram`) and
  in the REST/MCP/CLI list filters.
- **Content types** — the exact content-type values that exist, with live counts,
  when you want a specific one rather than a family.
- **Scopes** — the most common [scopes](../overview/concepts.md#scope) by count.
  Each is a tri-state toggle: click once to filter to it, again to *exclude* it,
  again to clear.
- **Popular labels** — the same tri-state toggle over the top labels.

Filters and search compose, and the URL updates so a filtered view is shareable.
A `× reset` link appears whenever any filter is on.

### File tree (package mode)

Open a PACKAGE artifact and the rail switches to its file tree under a `FILES`
header. `index.html` is starred (`★`), markdown/HTML files are emphasized, and
the selected file is highlighted. `◂ back to catalog` returns to the list.

## Browse all

The **Browse All** link in the left rail opens `/browse` — a faceted index of the
*whole* catalog rather than a filtered list. It shows every distinct **type**,
**label**, **scope**, and **content type**, each with a count. Pick a facet at the
top, sort by count or name, and click any value to jump straight into the catalog
filtered to it. It's the fastest way to discover what labels and content types
actually exist across all artifacts before you start filtering.

## The catalog table: columns

The table itself is adjustable, and your layout is remembered per browser (in an
`arti_cols` cookie, read server-side so a reload paints your layout directly
rather than flashing the default first). Nothing is stored per account — a
different browser starts fresh.

- **Resize** — drag the divider on the right edge of any header. Double-click a
  divider to return that one column to its default width. Widths are clamped so
  a column can't be dragged to nothing.
- **Reorder** — drag a header sideways; a blue edge shows where it will land.
- **Choose columns** — **right-click any header** (or click the **⋮** at the far
  right of the header row) for a checklist of every available column. The last
  entry resets columns, order, and widths in one go.

Shown by default: **title**, **slug**, **v**, **creator**, **scope · labels**,
**type**, **created**. Available to switch on:

| Column | What it shows |
| --- | --- |
| **comments** | Comments on *that version* — comments anchor to a version, not a slug. An amber **●** marks unresolved threads. |
| **description** | The artifact's description, truncated (full text on hover). |
| **content type** | The MIME type as its own column. (It rides under **type** as a subline while this column is off, so it's never shown twice.) |
| **size** | Stored byte size of the content. |
| **access** | Who can read it: `everyone`, `private` (creator-only), or the first entry of the read list with a `+n` for the rest. |
| **modified** | Last change to that version. |
| **archived** | When a version was archived — pairs with **show archived**. |
| **id** | The artifact UUID of that exact version. |

Sorting is unchanged: click a header to sort by it. **archived** sorts like the
default columns do; the rest of the opt-in columns have no server-side sort
order behind them, so their headers are plain labels rather than buttons.

**The header stays put.** The rows scroll inside the catalog rather than
scrolling the whole page, so the header row is always on screen — as are the
search bar above it and the paging bar below it, and the horizontal scrollbar
when enough columns are switched on to need one. Paging or re-sorting rewinds
the list to the top.

## Uploading

Click **Upload** in the rail to open the **Upload artifact** modal. (To write
something instead of uploading a file, **New** sits right below — see
[Creating artifacts in the browser](#creating-artifacts-in-the-browser).)

1. **Drop or choose a file.** The drop zone reads *"Drag & drop a file here / or
   click to choose — drop several to bundle them into a package."* Dropping
   several files (or a directory) bundles them into a PACKAGE. The modal shows
   the filename, size, and detected type.
2. **Title** is required. **✨ Auto-fill metadata** asks the server to suggest a
   title and other fields from the content.
3. **Slug (optional)** is the stable handle. As you type, the modal tells you
   whether it's a new slug (uploads as v1) or an existing one (uploads as the
   next version).
4. **Type** chips — `TEXT`, `PACKAGE`, `APP`, `ATTACHMENT` — with a one-line hint
   each. It's auto-detected from the file; you rarely change it. (An APP is a zip
   containing `arti-app.json`; an ATTACHMENT is a download-only binary with no
   slug.)
5. **Content type** is auto-detected (read-only).
6. **Scopes** and **Labels** are chip inputs with typeahead from existing values.

**Upload** commits it (the button reads *Uploading…* while it works); **Cancel**
closes the modal.

## Creating artifacts in the browser

Upload brings a file in from outside. **New** writes one here. Hover (or click)
**New** in the rail and pick a kind:

- **Text (Markdown)** — a markdown editor with a formatting toolbar and a
  **Write / Split / Preview** switch. The preview is rendered by the same
  component that renders the finished artifact, so it can't disagree with what
  readers will see. What you type is stored verbatim: no editor rewrites your
  markdown on save, so a one-word change stays a one-word diff in
  [Compare versions](#versions).
  Fenced `mermaid` blocks render in the preview and finished artifact.
- **Diagram** — the drag-and-drop canvas; see [Drawing diagrams](diagrams.md).

Both create an ordinary **TEXT** artifact — they differ only in content type —
so either one gets slugs, versions, labels, scopes, access control, comments and
search from the [artifact model](../overview/concepts.md).

The page's header mirrors the viewer's, with one deliberate difference: every
field is a plain edit box rather than click-to-edit. On the viewer, editing the
title or a label saves immediately; here **nothing is written until you press
Create**, so no field pretends otherwise. **Access** opens the same editor the
viewer uses, labelled to say the access applies when the artifact is created.

### Slugs and collisions

The slug box shows what will actually be created:

- Leave the title alone and the slug is `untitled-text-<yymmdd-hhmm>` — the
  timestamp keeps an unnamed draft from colliding with the last one you
  abandoned. It's stamped when the page opens and shown to you, not applied
  silently on save.
- Start typing a title and the slug follows it, without the timestamp.
- Type a slug yourself and it wins. It's normalized (lowercased, dashed) when
  you leave the field, so you see the final value before you commit.
- Clear the slug entirely to create a slugless artifact — legal, but it can't be
  versioned.

If the slug is already taken, a dialog says so and asks for a different one,
pre-filled with a timestamped variant. Creating here never quietly adds a version
to an existing document; to do that deliberately, open that document and use
**Edit**. The server enforces this too, so it holds even if someone claims the
slug while you're typing.

## Viewing an artifact

Click a row to open the viewer. A sticky header carries the metadata and
controls; the body below renders the content.

The header shows the title (click to rename if you're the creator or an admin),
a type badge and content type, a **permalink chip** (`a/<uuid>`, version-free)
with a copy button, and a **slug chip** (`s/<slug> · v<version>`) that links to
every version of that slug. Scopes (purple) and labels (gray) are shown as
chips; creators and admins can add or remove them inline. Each chip is also a
link that filters the catalog.

The toolbar on the right gives you:

- **Width** — `Wide` / `Medium` / `Narrow` for the rendered content column.
- **Full Page** — opens the content edge-to-edge, via a stable, slug/version-friendly `?v=full` URL. Works for any renderable artifact and for a selected file inside a PACKAGE (any directory). **APP** artifacts show **Visit app ↗** in this slot instead — the running app is already the chrome-less view.
- **Raw Source** — toggle between rendered and raw text.
- **↓ Download** — the file (or `↓ zip` for a whole PACKAGE).
- **Access** / **Edit Access** — a colored dot shows the access tier (public,
  domain-restricted, or restricted). Clicking opens a modal that lists every
  principal with access and its exact level. For creators/admins each row has a
  **Read** / **Read & write** control, and you can add an email, a `*@domain`,
  `*` for anyone who can sign in, a **group**, or an **SSO group** (`idp:` — the
  IdP groups your login carries, badged *SSO*). Read-only viewers see the same
  list without controls. **Read** lets someone open the version; **Read & write**
  additionally lets them publish new versions, append, and edit — a write grant
  always implies read. Changes apply to the shown version only.
- **Archive** / **Unarchive** — soft-delete, for creators and admins.

### How each type renders

| Type | What you see |
|---|---|
| **TEXT, markdown** | Rendered prose (sanitized). Frontmatter is shown separately; code blocks are highlighted and fenced `mermaid` blocks render as diagrams. |
| **TEXT, HTML** | A **sandboxed `<iframe>`** with scripts allowed but *not* same-origin access — the page can't touch your session. |
| **TEXT, code/JSON/YAML** | Monospace, as-is. |
| **PDF / image** | Native browser viewer / inline image. |
| **PACKAGE** | Pick a file from the rail's tree; it renders in the main area with a path strip on top. |
| **ATTACHMENT** | A download card (no inline render). |
| **APP** | A live app — use **Visit app ↗** to open it. See the [app developer recipe](../recipes/app-developer.md). |

## Versions

Re-uploading under the same slug creates a new version; the viewer always opens
the latest. The slug chip (`s/<slug> · v<version>`) links to a catalog view
filtered to that slug, sorted newest-first, so you can open any prior version.
The permalink chip (`a/<uuid>`) instead points at one specific version forever.

## Full-page view

The **Full Page** button (and a direct `?v=full` on the URL) renders the content
edge-to-edge with no chrome. APP artifacts don't have it: **Visit app ↗** takes
its place and opens the running app at `/app/<slug>/<version>`. The link is consistent across artifact types: it's
the artifact's normal-view URL plus `?v=full` (add `&file=<path>` to deep-link a
specific file inside a PACKAGE). Omit the version to always open the latest.

- **HTML** fills the viewport in a sandboxed iframe. Its `target="_blank"` and
  `window.open` links open on a normal click — the entry document is served under
  a popups-enabled full-page policy. It still runs in a unique origin with **no
  same-origin access**, so uploaded HTML can never touch your session.
- **A file inside a PACKAGE** renders from its own per-file URL (so a package's
  HTML entry resolves to itself, not the zip), the same as a single-file artifact.
- **Markdown** and **plain text/code** render full-width with the same styling as
  the in-viewer view.
- **Images and PDFs** render inline (image / native PDF frame).

## Comments

On rendered TEXT (markdown/code) and on HTML pages, you can comment directly on
the content:

- **Select text** in the prose to start a comment anchored to that span.
- On HTML pages you can also **drop a pin** at a point in the document.
- Comment cards float beside the content with **edit**, **delete**, and **link**
  (copy a deep link) actions. Highlights are yellow when active, dimmed when
  resolved.

Comments live alongside the artifact, not inside it — see
[Comments](../architecture/comments.md) for how anchoring and resolution work.
Note that comments are suppressed on individual files *inside* a PACKAGE (all
files share one artifact id), and on non-text content.

## Settings

The user menu (bottom of the left rail) links to **Settings**, a small admin/account
area with its own sub-nav:

- **User Groups** — create and manage the named member sets used in access rules
  (everyone can manage their own; role-bearing groups need `MANAGE_USER_GROUPS`).
- **API Keys** — mint, list, and revoke your own self-serve **upload-only** API keys
  for programmatic access. Keys are shown once at creation; a yellow badge warns on the
  user menu when one is within 7 days of expiring. See
  [Using the API](api.md#api-keys-self-serve) for the how-to.
- **Roles and Permissions** — visible only to holders of `MANAGE_ROLES`; assign roles
  to users and groups.

## See also

- [Concepts](../overview/concepts.md) — slugs, versions, scopes, labels, types.
- [Command line](command-line.md) — the same tasks from a shell.
- [Using the API](api.md) — programmatic access and API keys.
- [Quickstart](../overview/quickstart.md) — upload your first artifact.
