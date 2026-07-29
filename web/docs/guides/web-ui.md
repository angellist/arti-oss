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

- **Type** — chips for `all`, `TEXT`, `PACKAGE`, `APP`, `ATTACHMENT`, plus
  `markdown` and `html`. When you're logged in, a `👤 Owned by Me` chip filters
  to artifacts you created.
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

## Uploading

Click **Upload** in the rail to open the **Upload artifact** modal. (To make
something instead of uploading it, **New diagram** sits right below — see
[Drawing diagrams](diagrams.md).)

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
- **Full Page** — opens the content edge-to-edge, via a stable, slug/version-friendly `?v=full` URL. Works for any renderable artifact and for a selected file inside a PACKAGE (any directory).
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
| **TEXT, markdown** | Rendered prose (sanitized). Frontmatter is shown separately; code blocks are highlighted. |
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
edge-to-edge with no chrome. The link is consistent across artifact types: it's
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
