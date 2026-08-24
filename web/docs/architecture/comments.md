---
title: Comments
order: 7
summary: Per-artifact-version threads anchored doc-level, by text-quote, or by pin; a vanilla DOM overlay drives the in-app viewer and is also injected into sandboxed served HTML where it authenticates via a scoped embed token; comment events fan out to Slack DMs.
---

# Comments

Commenting attaches threads to a *specific artifact version*. The key is the
artifact's per-version UUID, so a comment made on v3 doesn't leak onto v4. The
data model is two tables: threads (an anchor + open/resolved status) and comments
(bodies hanging off a thread in time order), both cascade-deleted when the
artifact is hard-deleted. Access reuses the artifact's read rule — anyone who can
read an artifact can read and add comments on it — unless the document's owner has
turned commenting off (below).

## Turning comments off (per document)

Commenting can be switched off for a whole document. The switch is the
`artifacts.comments_enabled` column (default `TRUE`), exposed on the artifact DTO
as `comments_enabled` and toggled from the viewer's ⋮ menu — the row shows the
current state with a green (allowed) or grey (off) dot.

- **Per document, not per version.** Unlike `allowed_access`, a change writes
  *every version of the slug* (archived ones included), and publishing a new
  version inherits the previous version's value — resolved inside `store.Put`
  from a read taken immediately before the insert, so a concurrent toggle can't
  be overwritten by an in-flight version. So a doc can't end up with commenting
  on for v3 and off for v4. Inheritance also falls back to archived rows: a slug
  whose versions are *all* archived is still re-versionable, and republishing
  onto it must not re-open a document the owner closed.
- **Owner only.** The setter is the slug's OWNER — the creator of its *earliest*
  version — or a `MANAGE_ARTIFACTS` admin. It is deliberately not the patched
  version's creator: versioning reassigns that field, so a delegated writer could
  otherwise push a content-only version and take over the switch. The DTO carries
  the server's own verdict as `can_manage_comments`, so the UI never re-derives it.
- **Off means gone, not hidden.** The viewer mounts no overlay, arti-server stops
  injecting the overlay into served HTML, the list endpoints (REST and MCP) report
  zero threads, and every mutating endpoint returns 403. Existing threads stay in
  the table — flipping the switch back restores them.

## Anchors

A thread carries a JSONB anchor of one of three kinds:

| Kind | What it points at |
| --- | --- |
| Doc | The whole document. At most one doc thread per artifact (find-or-create under an advisory lock). |
| Text | A quoted text range, re-anchored on render by searching the rendered prose for the quote. |
| Pin | A normalized point on an image/diagram (HTML pages only). |

Pin numbers are stable by creation order, not array position: the frontend and the
backend independently compute the same 1-based number among open pins, so a Slack
notification and the overlay agree.

## The overlay

The comment UI is a vanilla DOM controller, deliberately outside React because it
does imperative work — wrapping text ranges in `<mark>`, positioning floating cards
by anchor geometry, drawing pins on a lightbox-enlarged diagram — that would fight
reconciliation. It talks to the server through a small API interface, so the *same*
controller can be driven two ways:

- **In-app viewer** — mounted over arti-rendered prose (markdown / text / JSON)
  using the cookie-based client. Text-select commenting only; pins are for HTML
  pages.
- **Injected into served HTML** — see below; uses a token-based client.

The in-app layer *suppresses* the outer overlay for `text/html` and PACKAGE
artifacts, because those render in a sandboxed iframe that gets its own overlay
injected in-page — otherwise you'd see two sets of controls. (Known gap: a PACKAGE
whose viewed file is markdown gets no overlay at all, since all files in a package
share one artifact_id and per-file comments would mix.)

## Commenting on sandboxed pages

A served HTML artifact (a prototype, an APP, a package's HTML file) runs in an
opaque-origin sandbox and *cannot send the `arti_session` cookie*. To let readers
comment on the page itself, arti-server injects the overlay at serve time and hands
it a scoped credential:

- At serve time, before `</body>`, arti appends a config blob (artifact id, the
  viewer's identity, and a token) plus a `<script>` tag pulling in the embed
  bundle. It no-ops on non-HTML or unauthenticated requests.
- The token is a **comment-embed** scoped JWT, short-lived (12h) and authorizing
  commenting on exactly that one artifact.
- The injected bundle calls the **embed comment API** — the same handlers, but
  mounted with CORS and Bearer auth instead of the cookie. That middleware verifies
  the token and pins the request to its one artifact, refusing any other id.

This is why a leaked embed token is contained: it works only via the embed
middleware, only for its artifact, and the main auth middleware rejects it outright
on the regular API/MCP routes (it's embed-scoped — see
[Authentication & access](auth.md)). The route also stays on the public ingress and
is kept out of the Next middleware matcher, so the sandboxed page can load the
bundle as a subresource.

## Slack notifications

When a Slack bot token is configured, comment events DM the relevant people via the
"Arti" Slack app. Each write — new comment, reply, resolve, reopen — dispatches
**after** the DB write commits, in a recover-guarded goroutine with its own
context. It is fire-and-forget: neither the latency nor a Slack failure ever
touches the request, and with no token configured every hook is a no-op.

The event gathers the artifact (owner, title, permalink), the thread's participants
(distinct comment authors), the anchor kind and quoted text, and the pin number;
the notifier then resolves Slack users by email and sends the DMs. Permalinks are
the artifact viewer URL with a `#comment-<id>` hash, which the overlay opens to that
exact comment on load.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
