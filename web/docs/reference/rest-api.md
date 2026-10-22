---
title: REST API
order: 1
summary: Every arti HTTP endpoint — artifacts, comments, share links, groups, roles, users, API keys, admin, auth/device, apps proxy, embed, and health — with auth, params, and responses.
---

# REST API

arti's HTTP surface, served by `arti-server`. Routing is registered in
`cmd/arti-server/cmd_serve.go`; the artifact handlers live in
`internal/artifacts/server.go`. The bundled `GET /openapi.yaml` is a hand-maintained
subset — **this page is the authoritative list** (it covers the routes the spec omits:
APP serving, `suggest-metadata`, `aggregates`, append, unarchive, PATCH, comments,
share links, groups, roles, users, admin, auth, device, apps, embed).

Base URLs: `https://arti.example.com` (prod) and `http://localhost:8090`
(dev). All paths below are relative to the base URL.

## Conventions

- **Auth groups.** Routes are either **public** (no SSO — registered directly on the
  root router, often with their own token or shared-secret auth) or **authed**
  (registered inside the `r.Use(authMiddleware)` group). Authed routes accept either an
  `arti_session` cookie (browser), a `Bearer` JWT (CLI/agent/service), or an
  [API key](#api-keys) sent as `Authorization: Bearer` — a self-serve
  `arti_upload_…` key (upload-scoped) or an admin-minted `arti_service_…` key (full
  bearer for a service principal). See the [auth architecture](../architecture/auth.md).
- **Identity.** Authed routes attribute every action to the caller's email.
- **Errors.** Failures return `{ "detail": string, "code": string }` with the matching
  HTTP status (`internal/artifacts/dto.go:287`).
- **`ArtifactInfo`** is the shared metadata DTO (below). UUID = artifact id; slug =
  named slug.
- **Trailing slashes.** A path under `/api`, `/app`, `/auth`, or `/share` that matches
  no route only because of a trailing slash is redirected to the slashless form —
  `301` for `GET`/`HEAD`, `308` for other methods so the body and method survive
  (`cmd/arti-server/trailing_slash.go`). Any other unmatched path under those prefixes
  is `404`.

### `ArtifactInfo`

`internal/artifacts/dto.go:14`. Note the field names differ from the stale
`openapi.yaml` (`scopes`/`labels`/`allowed_access` are plural arrays; `artifact_type`
includes `APP`).

| Field | Type | Notes |
|---|---|---|
| `artifact_id` | string (UUID) | |
| `artifact_type` | string | `TEXT` \| `PACKAGE` \| `APP` \| `ATTACHMENT` \| `MAP` |
| `named_slug` | string \| null | |
| `version` | int \| null | |
| `title` | string | |
| `description` | string \| null | |
| `content_type` | string | MIME |
| `size_bytes` | int64 \| null | |
| `sha256` | string \| null | |
| `creator` | string | email — the identity the write authenticated AS |
| `owner` | string | email — who the DOCUMENT belongs to. Distinct from `creator`, which names whoever pushed THIS version. Single-artifact responses only |
| `written_via` | string \| null | the credential that MADE the write: `apikey:<id>`, `device:<family>`, `token` (CLI or MCP), `session`, `service`. `null` on documents written before attribution shipped, which means unknown — not "a person in a browser" |
| `written_via_name` | string \| null | that credential's name at write time (an API key's name); `null` when it has none |
| `scopes` | string[] | e.g. `["a:bt-auto-route","u:alice"]` |
| `labels` | string[] | |
| `allowed_access` | string[] | glob-on-email patterns; `["*"]` = any authed reader; `[]` = creator-only |
| `allowed_write` | string[] \| null | tokens allowed to write (new version / append / edit). `null` = write follows read; `[]` = creator-only writes. Always emitted, and always a subset of `allowed_access` (see below). A document labelled `skill` with no write list is owner-only and is reported as `[]` |
| `asserted_by` | string \| omitted | the service principal that wrote this version on `creator`'s behalf (see [`X-Arti-Acting-For`](#writing-on-a-persons-behalf-x-arti-acting-for)) |
| `comments_enabled` | bool | per-DOCUMENT comment switch; `false` = the owner turned commenting off (no controls, no threads returned, writes 403) |
| `can_manage_comments` | bool \| omitted | whether *you* may flip `comments_enabled` (owner or admin). Single-artifact responses only |
| `can_write` / `can_edit_metadata` / `can_archive` / `can_share` | bool \| omitted | whether *you* may write a new version, edit title/description/labels/scopes, archive or unarchive, and mint an external share link. Single-artifact responses only |
| `metadata` | object | arbitrary; PACKAGE/APP carry a `package` sub-object |
| `created_at` / `modified_at` | RFC3339 | |
| `deleted_at` | RFC3339 \| null | set when archived |
| `url` | string | canonical artifact URL |
| `comment_count` / `open_thread_count` | int \| omitted | live comments on this version, and its unresolved threads. List and search rows only |
| `view_count` / `view_count_30d` / `bookmark_count` / `bookmarked` | int / bool \| omitted | view and bookmark stats, where the response computes them |
| `score` / `highlights` | number / object \| omitted | relevance score and highlight fragments, on OpenSearch-backed search results only |

## Artifacts (authed)

Registered by `artifacts.Mount` (`internal/artifacts/server.go:1663`).

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/artifacts` | Create or version an artifact |
| POST | `/api/artifacts/suggest-metadata` | LLM-suggest title/slug/labels from content |
| GET | `/api/artifacts` | List (filterable) |
| GET | `/api/artifacts/search` | Substring search |
| GET | `/api/artifacts/aggregates` | Label/scope/content-type histograms for the catalog sidebar |
| GET | `/api/artifacts/aggregates/browse` | Every value of one facet, with counts, paged (Browse page) |
| GET | `/api/artifacts/{id}` | Fetch metadata + serve content |
| GET | `/api/artifacts/{id}/meta` | Metadata only |
| GET | `/api/artifacts/{id}/denial` · `/api/artifacts/by-slug/{slug}/denial` | Name the document and its owner to a signed-in person who cannot read it |
| POST | `/api/artifacts/{id}/access/grant-read` | Add one address to the document's readers (owner or admin) |
| POST | `/api/artifacts/{id}/views` | Record one human-facing view |
| GET | `/api/artifacts/{id}/views` | Aggregate view stats (viewer rows owner/admin-only) |
| GET | `/api/artifacts/{id}/bookmark` | Slug-wide bookmark count, plus whether the caller bookmarked it |
| PUT / DELETE | `/api/artifacts/{id}/bookmark` | Add / remove the caller's bookmark |
| GET | `/api/artifacts/{id}/files` | PACKAGE/APP file manifest |
| GET | `/api/artifacts/{id}/files/*` | One file inside a PACKAGE/APP |
| PATCH | `/api/artifacts/{id}` | Edit mutable fields |
| DELETE | `/api/artifacts/{id}` | Archive (soft-delete); `?hard=true` admin-only hard-delete |
| POST | `/api/artifacts/{id}/unarchive` | Restore an archived artifact |
| GET | `/api/artifacts/by-slug/{slug}` | Metadata by slug (`?version=`) |
| GET | `/api/artifacts/by-slug/{slug}/raw` | Content by slug (`?version=`) |
| GET | `/api/artifacts/by-slug/{slug}/versions` | All versions under a slug |
| GET | `/api/artifacts/by-slug/{slug}/files` | PACKAGE manifest by slug (`?version=`) |
| GET | `/api/artifacts/by-slug/{slug}/files/*` | One file by slug (`?version=`) |
| POST | `/api/artifacts/by-slug/{slug}/append` | Append text as a new version (atomic) |
| POST | `/api/artifacts/by-slug/{slug}/owner` | Transfer the document to a new owner |
| DELETE | `/api/artifacts/by-slug/{slug}` | Archive **all** versions under a slug |
| GET / POST / PUT / DELETE | `/api/artifacts/by-slug/{slug}/map…` | MAP entries — see [MAP routes](#map-routes-under-apiartifactsby-slugslugmap) |
| GET | `/app/{ident}` · `/app/{ident}/{version}` | Serve an APP artifact full-page |
| POST | `/app/{ident}/consent` | Record the viewer's consent to run this APP version as them |
| GET | `/api/me` | Current caller's identity + permissions |

### POST `/api/artifacts`

Create a new artifact or a new version of an existing slug. Accepts
`application/json` **or** `multipart/form-data` (the latter for binary uploads;
`httpCreate`, `internal/artifacts/server.go:1734`). Adding a version to an **existing** slug requires
read access to it — a caller who can't read the slug gets `404` (it can't be silently
forked or probed).

JSON body (`CreateRequest`, `internal/artifacts/dto.go:183`):

| Field | Type | Notes |
|---|---|---|
| `title` | string | required |
| `content_type` | string | required (MIME) |
| `artifact_type` | string | `TEXT` (default) \| `PACKAGE` \| `APP` \| `ATTACHMENT`. A `MAP` is created through the [MAP routes](#map-routes-under-apiartifactsby-slugslugmap), not here (`400`) |
| `content` | string | raw text body |
| `content_base64` | string | base64 body (binary) — alternative to `content` |
| `named_slug` | string | optional; omitting it makes an unnamed artifact |
| `description` | string | |
| `scopes` | string[] | (`scope` singular is deprecated) |
| `labels` | string[] | |
| `metadata` | object | arbitrary |
| `entry_point` | string | PACKAGE/APP launch file |
| `ensure_new` | boolean | with `named_slug`: 409 if the slug already exists (no auto-version) |
| `expected_latest_version` | int | with `named_slug`: publish only if the slug is still at this version (409 `stale-base-version` otherwise) |
| `allow_type_change` | boolean | permit a version that changes `artifact_type` or the base of `content_type` (409 `type-change` without it) |
| `allowed_access` | string[] | omit → inherit-from-prior or default `["*"]`; `[]` → creator-only |
| `allowed_write` | string[] | omit → write follows read (or inherit); `[]` → creator-only writes. Unioned into `allowed_access` server-side |

Response: `201` + `ArtifactInfo`; `413` if the body exceeds the 200 MiB cap. Three
`409`s, distinguished by the envelope's `code`:

| `code` | Meaning |
|---|---|
| `slug-exists` | `ensure_new` was set and the slug already has a version |
| `stale-base-version` | the slug moved past `expected_latest_version` — re-read it and publish again |
| `type-change` | this version would change the document's `artifact_type` or the base of its `content_type`; re-send with `allow_type_change` if that is the intent |

Both new checks run against a fresh read inside the write transaction, so a
publish that races another one is refused rather than silently layered on top of
a version the caller never saw.

**ATTACHMENT and slugs.** An `ATTACHMENT` posted with a `named_slug` by a person
(browser session, their own CLI or MCP client) is a versioned document like any
other type. From a **service credential** (an `arti_` API key, or a device-flow
upload token) the same request is a `400`: those uploads are another app's files,
and they stay slugless and creator-only. Omit `named_slug` and they always have.

**Compressed bodies (`Content-Encoding: gzip`).** This endpoint and `/append`
accept a gzip-compressed request body — gzip the JSON (or multipart) payload and
send `Content-Encoding: gzip`; the server decompresses transparently before
decoding. **Always use this for HTML or JavaScript uploads, even a single file:**
Cloudflare's WAF inspects request bodies and false-matches its `<script>` rule on
inline markup, returning `403` before the request reaches arti; a gzipped body
carries no matchable markup. The `413` size cap applies to the **decompressed**
size. PACKAGE/APP uploads are zip already, so they need no extra compression. The
`arti` CLI and the web upload do this automatically for textual content; over raw
`curl`, gzip the body yourself:

```sh
jq -n --arg c "$(base64 < report.html)" \
  '{artifact_type:"TEXT",content_type:"text/html",named_slug:"q3-report",title:"Q3 Report",content_base64:$c}' \
  | gzip | curl -sX POST https://arti.example.com/api/artifacts \
      -H "Authorization: Bearer $ARTI_TOKEN" \
      -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
      --data-binary @-
```

#### Writing on a person's behalf (`X-Arti-Acting-For`)

A service principal holding `ASSERT_ACTOR` may send `X-Arti-Acting-For: <email>` on
`POST /api/artifacts` and on append. The named person becomes the version's `creator`;
the principal is recorded in `asserted_by`, owns a slug it opens, and is added to the
version's readers. The write is authorized as the principal alone: the person's roles
are never consulted. The person can read the version but holds no creator authority
over it (no metadata edit, archive or unarchive). The person must be an allowed, active
user and not a service principal. A slug with only archived versions is reopened this
way only by its owner. The header is refused (`403`) on every other route, and from any
caller that is not a service principal with `ASSERT_ACTOR`.

A slugless `ATTACHMENT` is creator-only unless the request names readers; a reader list
the request passes is kept, and one inherited from a named slug is not.

### POST `/api/artifacts/by-slug/{slug}/append`

Append to a slug as a new version, atomically (`body = prior + separator + content`).
Auto-creates v1 when the slug doesn't exist (then `title` + `content_type` are required).
Binary content types are rejected — use a PACKAGE/ATTACHMENT instead.

Body (`AppendRequest`, `internal/artifacts/dto.go:247`): `content` (required),
`separator` (default `"\n\n"`, `""` for none), `idempotency_key` (24h dedup per
`(key, creator)`), `title`/`description`/`content_type`/`scopes`/`labels`/`allowed_access`
(optional overrides; nil inherits). Response `200` + `ArtifactInfo`; replays carry
`X-Arti-Idempotent-Replay: true`. Accepts a `Content-Encoding: gzip` body — gzip
appended HTML/JS so the WAF can't false-match its `<script>` rule (see the create
endpoint above).

### MAP routes, under `/api/artifacts/by-slug/{slug}/map`

A MAP's keys are a live head rather than a body, so they have their own routes. Every
one of them resolves the slug first and applies the document's read or write access,
and all of them answer `400` while `ARTI_MAP_ARTIFACTS` is off.

| Route | Does |
|---|---|
| `POST .../map` | write a batch of up to 100 entries; creates the MAP at v1 when the slug is new, which needs `title` and may carry `allowed_access` / `allowed_write` |
| `PUT .../map/keys/{key}` | write one entry — the body IS the value, and the guards ride as `?if_absent=true` or `?if_rev=<n>` so a value is never wrapped |
| `GET .../map/keys/{key}` | read one entry |
| `GET .../map` | list a page — `?prefix=`, `?cursor=`, `?limit=` |
| `GET .../map/browse` | the viewer's table: `?q=`, `?sort=`, `?dir=`, `?limit=`, `?offset=`, values truncated to a preview |
| `DELETE .../map/keys/{key}` | delete one entry; deleting an absent key succeeds with `deleted: 0` |
| `POST .../map/snapshot` | freeze the head as a new NDJSON version; a snapshot matching the latest version returns it with `unchanged: true` |

A write returns `200` when every entry landed and `409` when any guard lost, with the
incumbent entry alongside the one that lost. A value over 64 KiB is `413`; a bad key, a
batch over 100, or a write that would cross the 10,000-key or 8 MiB ceiling is `400`
with the limit named in the message.

### POST `/api/artifacts/suggest-metadata`

Given `{ filename, content_type, artifact_type?, sample }`, returns
`{ "title", "slug", "labels" }` from the built-in metadata completer (haiku). Requires
`ANTHROPIC_API_KEY` to be configured.

### POST `/api/dictate`

Body is one recorded clip, `Content-Type: audio/*` (webm, mp4, ogg, wav or mp3), at most
25 MB. Returns `{ "text" }`. `501` when `OPENAI_API_KEY` is unset, `413` over 25 MB, `429`
over `ARTI_DICTATION_RPM_PER_VIEWER`, `502` when the provider fails. Upload tokens and API
keys get `403`. See [Dictation](configuration.md#dictation).

### GET `/api/artifacts` and `/api/artifacts/search`

List/search query params:

| Param | Effect |
|---|---|
| `limit` / `offset` | page size (default 50; a value over 500 is clamped to 500) and rows to skip |
| `type` | an `artifact_type`, or a pseudo-type — `MARKDOWN`, `HTML`, `DIAGRAM`, `JSON`, `PDF`, `IMAGE` — that matches a content-type family |
| `creator` / `owner` | the version's creator, or the document's owner. Exact match, or a `*` glob |
| `scope` / `label` | scope; label is repeatable and every label must match. `*` globs allowed |
| `slug` | one slug — drills in and lists its versions |
| `via` | the writing credential, glob-aware — e.g. `via=apikey:3f2a…` or `via=apikey:*` |
| `archived` | `only`, `include`, or unset (hide archived). `include_archived=true` is the older spelling of `include` |
| `all_versions=true` | every version instead of the latest per slug (the default) |
| `order_by` / `order_dir` | `created` \| `title` \| `type` \| `slug` \| `version` \| `creator` \| `scope` \| `archived` \| `views`; `asc` \| `desc` |

Search adds `q`. Bare words are a substring search; `field:value` tokens in `q` act as
filters — `slug:`, `creator:`, `owner:`, `scope:`, `type:`, `label:`, `content_type:`,
`via:`, and `bookmarked:me`. `creator`, `scope`, `type`, `label`, and `content_type`
can be negated with a leading `-`. Both return
`{ "artifacts": ArtifactInfo[], "total": int }`. Non-admins see only artifacts they can
read (their email + group memberships); admins with `MANAGE_ARTIFACTS` see all.

`GET /api/artifacts/aggregates` returns the sidebar's top-30 counts of scope types,
scopes, labels, and content types for the caller. A cached result up to 60 seconds old
is served while a refresh runs in the background.

`GET /api/artifacts/aggregates/browse?facet=` returns `{ values: [{ value, count }],
total }` for one facet: `type`, `label`, `scope`, `content_type`, `owner` (the document
owner), or `creator` (each version's creator). `?sort=count|name`, `?dir=asc|desc`,
`?page=` (from 1), `?page_size=` (default 50, max 500), and `?versions=all` to count
every live version instead of each slug's latest. `owner` and `creator` count every
artifact whatever the caller can read; the other facets count only what the caller can
read.

### PATCH `/api/artifacts/{id}`

Edit mutable fields: `{ title?, description?, scopes?, labels?, allowed_access?, allowed_write?,
comments_enabled? }`. The version's creator, the document's owner, or a holder of
`MANAGE_ARTIFACTS`. Response `200` + updated `ArtifactInfo`.

**`comments_enabled`, `allowed_access`, and `allowed_write` are per-DOCUMENT,
not per-version:** the server writes them to every version of the slug
(archived versions included, so unarchiving can't resurrect a stale setting),
and a new version inherits them. They are settable only by the artifact's
OWNER (its first version's creator, unless transferred) or an admin — not by a delegated
writer, and not by the creator of the patched version if that differs from
the owner. For the ACL fields the owner's write is applied slug-wide even when
the values match the patched version — that resend is how a slug whose
versions disagree (data predating the slug-wide model) gets converged. A
non-owner re-sending the current ACL unchanged is still accepted as a no-op;
changing it is still `403`. Everything else in this section (`title`,
`description`, `labels`, `scopes`) stays per-version.
`false` turns commenting off everywhere: no overlay in the viewer, none injected
into served HTML, no threads from the list endpoints, `403` on every write.
Existing threads are kept and reappear if it is set back to `true`.

**Read vs write access.** `allowed_access` gates who can *read* a version;
`allowed_write` gates who can *write* it (push a new version, append, or edit
metadata/content). `allowed_write` is always a **subset of `allowed_access`** —
the server unions any write grant into `allowed_access` on save, so a writer can
always read. Omitting `allowed_write` (or leaving it `null`) means write follows
read — the historical behavior, and the state of every artifact created before
this field existed. An empty `allowed_write` (`[]`) means creator-only writes
while reads stay as `allowed_access`. The invariant is enforced server-side, so
raw REST/MCP/CLI writers cannot create a write-but-not-read grant.

### POST `/api/artifacts/by-slug/{slug}/owner`

Transfer the document to a new owner. Body `{ "owner": "<email>" }`, response
`{ "slug", "owner", "previous_owner" }`.

Ownership is per-DOCUMENT and stored (`artifact_owners`, migration 0029). It is
claimed by the slug's first version and moves only through this endpoint — not
when someone else publishes a version, not when v1 is archived. It is the
authority every document-level setting resolves to: the ACL, the comment
switch, external share links, and this transfer itself.

Authorization: the current owner or an admin (`MANAGE_ARTIFACTS`). The target
must be a single email address — a glob or `group:` token is rejected, since
ownership is one principal. Transferring grants the new owner read access on
every version, so they cannot inherit a document they cannot open; the previous
owner's own grants are left in place for the new owner to withdraw.

### DELETE `/api/artifacts/{id}` and `/api/artifacts/by-slug/{slug}`

Archive (soft-delete). By id: `204`. By slug: archives every version, returns
`{ "archived_count": int }`. `?hard=true` on the id route hard-deletes and needs
`MANAGE_ARTIFACTS`. Authorization: by id, the version's creator, the document's owner,
or `MANAGE_ARTIFACTS`; by slug, the document's owner, the creator of every version, or
`MANAGE_ARTIFACTS`. A non-admin archiving a skill also needs write access to it, and a version
written on your behalf by a service principal does not count as yours.

### Access denial and grant-read

`GET /api/artifacts/{id}/denial` and `.../by-slug/{slug}/denial` answer
`{ named_slug, version, title, owner }` only when the document exists, is not archived,
and the caller cannot read any version of it — so a person who opened a link without
access learns whom to ask. Every other case is the same `404` the ordinary read gives.
Only an interactive browser session gets an answer; API keys, device tokens, and app or
embed tokens always get `404`.

`POST /api/artifacts/{id}/access/grant-read` with `{ "email": "<address>" }` adds that
one address to the document's `allowed_access` (it never removes anything and never
touches `allowed_write`). The caller must be able to read the document and must be its
owner or an admin (`403` otherwise). Patterns — `*`, globs, `group:` tokens — are a
`400`. Returns the updated `ArtifactInfo`.

### POST `/app/{ident}/consent`

An APP runs as whoever opens it, so until the viewer approves a version the APP routes
serve a consent page instead of the app. The page's form posts here; the approval is
stored as a 30-day cookie for that version, or with `scope=slug` for every version of
the slug by the same publisher with the same declared tools. With `next` (a path under
`/app/` or `/api/artifacts/`) the response is a `303` to it; without, `204`.

### Serving content

`GET /api/artifacts/{id}` and `.../by-slug/{slug}/raw` stream the body with the stored
`Content-Type`. Served content is sandboxed by content type (`setContentSecurity`,
`internal/artifacts/server.go`): HTML gets `Content-Security-Policy: sandbox
allow-scripts …` plus the comments overlay for authed viewers; any **other scriptable**
type (SVG, XML, JSON, plain text, …) gets a script-less `Content-Security-Policy:
sandbox`; only inert media — raster images (not SVG), PDF, audio, and video — are served
with no CSP. `GET /app/{ident}` serves an APP artifact in an opaque-origin sandbox with
the [app bridge](app-sdk.md) injected instead. PACKAGE file routes (`/files`, `/files/*`)
list and serve individual entries under the same per-type CSP.

`GET /api/artifacts/{id}/files-token/{token}/*` is the public, token-authenticated twin
of `/files/*`: the viewer's rendered page points its relative asset loads here, because
a sandboxed page cannot send the session cookie. The token in the path is the
credential.

### GET `/api/me`

Returns `{ email, is_admin, name, picture, permissions: string[], share_links_enabled }`
(`internal/artifacts/server.go:2200`). `is_admin` is derived from holding the `ADMIN`
role; `permissions` is the effective permission set used for FE capability gating.

## Comments (authed)

Registered by `commentsSvc.Mount` (`internal/comments/`). All scoped to artifacts the
caller can read.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/artifacts/{id}/comments` | List threads (with comments) |
| GET | `/api/artifacts/by-slug/{slug}/comments` | List threads by slug (`?version=`, `?exclude_resolved=true`) |
| GET | `/api/artifacts/{id}/comments/people` | Mention typeahead (`?q=`). Readers only; the owner or an admin also sees non-readers, flagged, plus `can_grant` |
| GET | `/api/comments/{threadID}` | Fetch one thread (with comments) |
| POST | `/api/artifacts/{id}/comments` | Create a thread — body `{ anchor?, quote?, body }` → `201` write envelope |
| POST | `/api/artifacts/by-slug/{slug}/comments` | Create by slug — body `{ anchor?, quote?, body }` → `201` write envelope |
| POST | `/api/comments/{threadID}/replies` | Reply — body `{ body }` → `201` Comment |
| POST | `/api/comments/{threadID}/resolve` | Mark resolved → `200` Thread |
| POST | `/api/comments/{threadID}/reopen` | Reopen → `200` Thread |
| PUT | `/api/comments/{threadID}/comments/{commentID}` | Edit (author only) — body `{ body }` |
| DELETE | `/api/comments/{threadID}/comments/{commentID}` | Delete (author only) → `204` |

A **Thread** is `{ id, anchor, status (open\|resolved), created_by, created_at,
resolved_by?, comments: Comment[] }`; a **Comment** is `{ id, author, author_name,
author_picture?, body, source?, created_at, edited_at? }`. Create responses include
`{ artifact_id, slug?, version?, thread, mentions_notified, mentions_unreachable }`
so slug callers can see which version received the thread. `quote` is matched
against rendered prose, not raw markdown. Addresses written as `@user@example.com`
in the body send DMs when the address can read the artifact; unreachable mentions
are echoed in the write response. Comment writes return `403` when
`comments_enabled=false` and `429` when the per-principal comment write cap is hit.

## Comments embed (public, token-authed)

For the comments overlay injected into sandboxed served-HTML pages, which can't send the
session cookie. Authenticated by a per-artifact scoped Bearer token minted server-side
and CORS-enabled (`commentsSvc.MountEmbed`). Same operations as above, under the
`/api/embed/` prefix:

| Method | Path |
|---|---|
| OPTIONS / GET / POST | `/api/embed/artifacts/{id}/comments` |
| GET | `/api/embed/artifacts/{id}/comments/people` (never offers a grant) |
| POST | `/api/embed/comments/{threadID}/replies` |
| POST | `/api/embed/comments/{threadID}/resolve` · `/reopen` |
| PUT / DELETE | `/api/embed/comments/{threadID}/comments/{commentID}` |

## Apps proxy (public, app-token-authed) {#apps-proxy}

The governed MCP back end for APP artifacts (`appsSvc.MountProxy`,
`internal/apps/apps.go:581`). Authenticated by the injected, app-scoped Bearer token
(not the session cookie), CORS-enabled.

| Method | Path | Purpose |
|---|---|---|
| OPTIONS | `/api/apps/mcp` | CORS preflight |
| POST | `/api/apps/mcp` | Forward one `tools/call` to a named upstream MCP server |

Body: `{ app_id, server, tool, arguments, timeout_ms? }`. `timeout_ms` defaults to 60
seconds and is capped by `ARTI_APP_CALL_TIMEOUT_MAX` (default 90 seconds). The proxy
validates the token, enforces the app's `arti-app.json` allowlist, resolves the server
(URL + auth policy live server-side), attaches the viewer's OBO credential for `oauth`
servers, and forwards the call with `_meta.consumer = "arti:<slug>"` (the app's UUID
when it has no slug). Returns the upstream MCP result, or `401 { error: "authorization_required",
authorize_url }` when the viewer must consent. See the [APP SDK](app-sdk.md).

## Embed surfaces (public, secret-authed)

Serve arti artifacts into external (cross-site) iframes, authenticated per-surface by a
shared secret (no SSO). Disabled unless `ARTI_EMBED_SURFACES` is set (`embed.New().Mount`,
`internal/embed/`). A surface's `identity` is `service` (content reads as a fixed
configured email) or `user` (the viewer authorizes themselves once via a consent popup
and tool calls run as the real viewer). See [front embed](../architecture/front-embed.md).

| Method | Path | Purpose |
|---|---|---|
| GET | `/embed/{surface}` | Serve an artifact by slug (`?slug=`, `?version=`, `?auth_secret=`) |
| GET | `/embed/{surface}/shell` | Client adapter/loader for shell surfaces |
| GET | `/embed/{surface}/_files/{token}/*` | Serve a packaged file (the scoped token in the path is the credential) |
| OPTIONS / POST | `/embed/{surface}/token` | User-mode handshake: the consent popup's fetch completes the mint (CORS; no-op for service surfaces) |
| GET | `/embed/{surface}/token` | User-mode handshake: the embedded app polls for its minted token (CORS) |
| POST | `/embed/{surface}/host-token` | Trusted host mint: the embedding host's server, holding the surface's `host_key` as a Bearer, mints a viewer's app token with no consent click — see [front embed](../architecture/front-embed.md#per-user-mode-trusted-host-mint-host_key) |

The consent page itself is `GET /auth/embed/app-token` (session-authed — self-hosters
must route it behind the auth front door, while `/embed/*` stays public; see the
[front embed](../architecture/front-embed.md) self-hosting notes).

## Groups (authed)

Named member sets used in `allowed_access` patterns and role assignments
(`groups.NewService().Mount`, `internal/groups/`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/groups` | List the caller's groups (all, if admin) |
| POST | `/api/groups` | Create — `{ name, display_name, members }` → `201` |
| PATCH | `/api/groups/{name}` | Update `{ display_name?, members? }` (owner or admin) |
| DELETE | `/api/groups/{name}` | Delete (owner or admin) → `204` |
| POST | `/api/groups/{name}/members` | Add one member — `{ email }` → the updated group (owner or admin) |
| DELETE | `/api/groups/{name}/members?email=` | Remove one member → the updated group (owner or admin) |
| GET | `/api/idp-groups` | List captured IdP (SSO) group names + member counts (no rosters) |
| GET | `/api/people` | Email typeahead: `?q=` (min 2 chars), `?limit=` (default 10) → `{ people: [{ email }], min_query }` |

A group DTO carries `{ name, display_name, members, member_count, token (group:name),
created_by, created_at, modified_at }`.

**`/api/people`** backs the email typeahead in the access editor and the membership
editor. arti provisions no users, so "who exists" is the union of the tables that have
recorded an address: login snapshots (`user_idp_groups`), artifact creators, group
members, and user role assignments. Glob members (`*@domain`) are excluded — they are
patterns, not people. A query under two characters returns an empty list, so no request
shape enumerates the workspace.

**Role-bearing groups.** A group that has a role assigned to its `group:<name>` principal
is privileged: creating it, changing its membership, or deleting it additionally requires
the `MANAGE_USER_GROUPS` permission (`403` otherwise, `internal/groups/groups.go`) — a
separation-of-duties gate so a group owner can't quietly grant a role's members to
arbitrary emails. A display-name-only rename, and any action on a non-role-bearing group,
stay owner-or-admin.

## Roles & permissions (authed, `MANAGE_ROLES`)

RBAC administration (`roles.NewService().Mount`, `internal/roles/`). Every route requires
the `MANAGE_ROLES` permission.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/permissions` | List all permission names |
| GET | `/api/roles` | List roles |
| POST | `/api/roles` | Create a custom role — `{ name, description, permissions }` |
| PATCH | `/api/roles/{name}` | Update `{ description?, permissions? }` |
| DELETE | `/api/roles/{name}` | Delete a custom role → `204` |
| GET | `/api/role-assignments` | List assignments (`?role=`) |
| POST | `/api/role-assignments` | Assign — `{ principal_type (user\|group), principal_id, role_name }` → `204` |
| DELETE | `/api/role-assignments` | Unassign (`?principal_type=&principal_id=&role_name=`); guards against removing your own ADMIN |
| GET | `/api/role-lookup` | Effective access for `?email=` — roles + sources + effective permissions |

A caller without `MANAGE_ROLES` gets `404`, not `403`.

## Users (authed, `MANAGE_ROLES`)

The roster on Settings → Users (`internal/roles/roles.go`). Same gate as roles: `404`
without `MANAGE_ROLES`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/users` | Every principal arti knows → `{ users: User[], sources }` |
| POST | `/api/users` | Record a principal — `{ email, kind (human\|service), note? }` → `201` User. Assigns no role |
| POST | `/api/users/deactivate` | `{ email }` → User. Refuses every credential the email holds until reactivated; you cannot deactivate yourself (`400`) |
| POST | `/api/users/reactivate` | `{ email }` → User |
| GET | `/api/users/access?email=` | One person's stored credentials — `api_keys`, `device_tokens`, `share_links`, `connections` — plus 90 days of credential `usage` |
| POST | `/api/users/access/revoke` | `{ email, kind (apikey\|device\|share\|connection), id }` → `204`. Revokes one of that person's credentials |

A **User** is `{ email, kind, note, roles: [{ name, source }], groups, idp_groups,
idp_stale, last_seen_at, source, registered, added_by, added_at, deactivated_at,
deactivated_by }`. arti provisions no accounts, so the roster is derived from every
table that has recorded an address; `POST /api/users` exists for a principal with no
other trace, such as a service principal that has not acted yet. `last_seen_at: null`
does not mean "never signed in" — logins were not always recorded.

## Admin (authed, `MANAGE_ARTIFACTS`)

`admin.NewService().Mount` (`internal/admin/`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/admin/stats` | Read-only operational snapshot (artifact counts by type; comment thread/activity counts) |
| GET | `/api/admin/blocks` | The block list: `{ blocks: [{ pattern, reason, created_by, created_at }] }`, newest first |
| POST | `/api/admin/blocks` | Block everything matching a pattern. Body `{ pattern, reason? }` |
| DELETE | `/api/admin/blocks?pattern=<p>` | Lift a blocked pattern |
| GET | `/api/admin/blocks/matches?pattern=<p>` | The documents a pattern currently hides |
| GET | `/api/admin/blocked/{ident}` | Metadata for one blocked document, for review |
| GET | `/api/admin/blocked/{ident}/raw` | Its body, as `text/plain` under `nosniff`, capped at 1 MiB |
| GET | `/api/admin/mcp-servers` | APP MCP connectors → `{ servers }`. **`MANAGE_CONNECTORS`** |
| POST | `/api/admin/mcp-servers` | Register a connector — `{ name, resource_url, auth (none\|oauth), scope?, enabled?, tool_allowlist?, notes? }` → `201`. **`MANAGE_CONNECTORS`** |
| PATCH | `/api/admin/mcp-servers/{name}` | Change any field but `name` (a rename is `400`). **`MANAGE_CONNECTORS`** |
| DELETE | `/api/admin/mcp-servers/{name}` | Delete → `{ name, deleted, tokens_revoked }`; revokes the stored user tokens for it. **`MANAGE_CONNECTORS`** |
| GET | `/api/admin/mcp-servers/{name}/apps` | The live APPs whose manifest declares this connector. **`MANAGE_CONNECTORS`** |

A block hides every version matching the pattern from **every** caller, admins
included, and refuses writes to it; the versions, owner and ACL are untouched, so
deleting the row restores the document. The pattern matches a slug, or the artifact
id of a slug-less artifact, with `*` for any run of characters and `?` for one,
case-insensitive. Blocked documents answer `404` on every surface, so the block
needs no special handling in a client. The first three routes read and write the
list itself, never a blocked document, so an admin can always lift what an admin
set. The last three are the one exception to a block: they exist so an admin can
see what a pattern hides and decide whether to lift it. Their store reads return
a row only while it is blocked, a body is served as plain text under `nosniff`
whatever the document's own type is, and a PACKAGE, APP or MAP is refused rather
than unpacked. Non-admins get `404` on all of them, not `403`, so the endpoints
are not discoverable.

The connector routes manage which upstream MCP servers the [apps proxy](#apps-proxy) may
reach. `name` is lowercase letters, digits, and dashes, and may not shadow a built-in
server. `resource_url` must be `https` and its host must be in
`ARTI_APP_MCP_ALLOWED_HOSTS` (an empty list refuses every write). `auth: oauth` needs a
`scope`. See [Connect your own MCP servers](../guides/self-hosting.md#mcp-servers).

## API keys (authed) {#api-keys}

Self-serve, personal bearer keys for programmatic upload (`apikeys.NewService().Mount`,
`internal/apikeys/service.go`). A key looks like `arti_upload_XXXXX…`; arti stores only
its SHA-256, so the plaintext is returned **once**, at creation. A key carries the
`upload` scope — so an API-key-authed request runs through the same default-deny
[upload-scope guard](#device) (create/append/read only, same body cap) as a device
token. Revocation and expiry are enforced at authentication (a revoked or expired key
fails to authenticate). Managing your own keys needs no special permission; the
cross-owner admin view needs `MANAGE_API_KEYS`.

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/keys` | Mint a key → `201` with the plaintext `key` (shown once). **Browser session only** |
| GET | `/api/keys` | List your keys (`?all=true` → every owner, needs `MANAGE_API_KEYS`) |
| GET | `/api/keys/usage` | Where your credentials have been used, and how many documents each wrote (`?all=true` → every owner, needs `MANAGE_API_KEYS`; `?days=` window, default 30, max 365) |
| DELETE | `/api/keys/{id}` | Revoke (soft) — your own key, or any key with `MANAGE_API_KEYS` |
| POST | `/api/admin/service-keys` | Mint a key for a service principal → `201` with the plaintext `key`. **Browser session + `MANAGE_API_KEYS`** |

### POST `/api/keys`

Requires the **browser session** credential: the `arti_session` cookie, or the
access token oauth2-proxy forwards for a browser. Every bearer token is rejected
`403` — a CLI or MCP token, the service secret, and another API key alike — because
a credential authenticates as its owner, so nothing downstream can tell a person
from an agent holding that person's token. Create keys in Settings → API Keys.
The route is also IP-rate-limited (`ARTI_API_KEY_RPM`).

Body: `{ name, scopes?, ttl_days? }`. `name` is required. `scopes` defaults to
`["upload"]` and must be exactly one self-serve scope (only `upload`; `service` keys are
minted by an admin, below). `ttl_days`
defaults to 90, clamped to `[1, ARTI_API_KEY_MAX_TTL]` (max default 365 days).

Response `201` — the key view plus the one-time `key`:

```json
{
  "id": "…uuid…",
  "name": "ci-upload",
  "key_prefix": "arti_upload_AbCdE",
  "owner_email": "you@example.com",
  "scopes": ["upload"],
  "created_at": "2026-07-02T…Z",
  "expires_at": "2026-09-30T…Z",
  "last_used_at": null,
  "revoked_at": null,
  "key": "arti_upload_…full-secret-shown-once…"
}
```

### POST `/api/admin/service-keys`

Mints a key for a **service principal**: a roster row with `kind: service` that an admin
added (`POST /api/users`) and nobody deactivated. Service principals use addresses under
the reserved `.invalid` TLD, so no login can be completed as one and no allowlist admits
them; their keys pass the API-key rung through the service-principal check instead.

Same browser-session rule as `POST /api/keys`, plus `MANAGE_API_KEYS`. Body:
`{ owner_email, name, ttl_days? }`. The key carries the `service` scope (a full bearer for
that principal, including `/mcp`), records the minting admin in `minted_by`, and the mint
notification goes to that admin. `400` when `owner_email` is not an active service
principal.

A service principal's `.invalid` address never matches an org-wide pattern such as
`*@example.com`. A role that grants `READ_ORG_SHARED` lets it read documents shared that
way. The permission grants no write, gives a person nothing, and does not open comment
or APP-proxy access.

`GET /api/keys` returns an array of that view **without** `key` (includes revoked/expired
keys, newest first). `DELETE /api/keys/{id}` soft-revokes (`204`; `404` if it isn't yours
and you lack `MANAGE_API_KEYS`). See the [Using the API](../guides/api.md#api-keys-self-serve)
guide for the how-to.

### GET / PUT `/api/notifications`

The signed-in person's own Slack notification choices. No permission required: these
govern messages sent to that caller, and the identity comes from the session, so a
caller can only ever read and write their own. `GET` returns each category with its
state, plus `slack_configured`, `deployment_enabled` and `can_manage_deployment`;
`PUT {key, enabled}` sets one and returns the new state. Comment mentions default
**on**; the credential categories default **off**. A change takes effect within 15
seconds, without a deploy.

### GET / PUT `/api/admin/notifications`

The deployment-wide switch (`notifications.slack.enabled`), which stops every Slack
message arti sends to anyone. `MANAGE_ARTIFACTS` only; anyone else gets `404`, as with
the rest of the admin surface. It overrides personal choices in one direction only:
off means nothing is sent, on returns everyone to what they chose.

### GET `/api/keys/usage`

Where each of the caller's credentials has been used, and how many live documents
each has written:

```json
{
  "sources": [
    {
      "cred": "apikey:3f2a…",
      "ip": "34.72.11.8",
      "user_agent": "python-requests/2.31",
      "reads": 1240,
      "writes": 12,
      "first_seen": "2026-09-01T14:02:11Z",
      "last_seen": "2026-09-04T17:29:02Z"
    }
  ],
  "docs": { "apikey:3f2a…": 261 },
  "days": 30
}
```

`cred` matches the `written_via` stamped on the documents that credential wrote, so
`GET /api/artifacts?via=<cred>` lists them. Rows are aggregated from daily counts per
source, not per request, and are deleted after 90 days. An **API key** answering from a
network (`/24` or `/64`) it has never answered from also DMs its owner; no other
credential kind is notified about, because a person's own clients change address and
user agent constantly. `?all=true` adds `owner_email` and covers every owner for a
`MANAGE_API_KEYS` holder (and silently narrows to the caller's own for anyone else).

## Share links (authed; public serve)

External, time-limited links for one document, for a reader with no arti login. Off
unless `ARTI_SHARE_ENABLED=true`; when off, the routes below are not registered (`404`).
How links behave for people is in [Sharing](../guides/sharing.md).

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/artifacts/{id}/shares` | Mint a link — `{ scope, ttl, note? }` → `201 { id, url, token_prefix, expires_at }`. IP-rate-limited (`ARTI_SHARE_MINT_RPM`) |
| GET | `/api/artifacts/{id}/shares` | The document's links |
| DELETE | `/api/shares/{shareID}` | Revoke a link |
| GET | `/api/shares/{shareID}/opens` | The link's access log |
| GET | `/share/{token}` · `/share/{token}/download` · `/share/{token}/_files/*` | **Public.** Serve the shared document, its download, or one PACKAGE file. The token is the credential. IP-rate-limited (`ARTI_SHARE_OPEN_RPM`) |

All four authed routes are owner-or-admin. `scope` is `version` (pinned to the current
version) or `slug` (tracks the newest version). `ttl` is one of `1h`, `8h`, `24h`, `7d`,
`30d`, and no more than `ARTI_SHARE_MAX_TTL`. The `url` is returned once and cannot be
recovered. APP and archived documents cannot be shared.

## MCP (authed)

| Method | Path | Purpose |
|---|---|---|
| POST | `/mcp` | arti's own MCP server (Streamable HTTP, JSON-RPC 2.0) |

Inside the authed group, so a session cookie or Bearer works; MCP clients can also use
the OAuth flow below. Full tool list: [MCP tools](mcp-tools.md).

## Health & discovery (public)

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness — `204`, no body |
| GET | `/openapi.yaml` | The bundled (partial) OpenAPI document |
| GET | `/.well-known/oauth-protected-resource` · `/.well-known/oauth-protected-resource/mcp` | RFC 9728 resource metadata (the `/mcp` form is the one a `401` advertises) |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414 AS metadata |

## Auth — sessions & CLI (public)

| Method | Path | Purpose |
|---|---|---|
| GET | `/auth/login` | Browser login front door, per `ARTI_AUTH_MODE`. `oidc`: starts the authorization-code + PKCE flow at the configured issuer; `proxy`: mints from the trusted proxy headers. Mints `arti_session` (browser) or renders an explicit confirmation page for `cli_code`/`user_code`; the pairing itself happens via `POST /auth/login/confirm` |
| POST | `/auth/login/confirm` | Completes a `cli_code` / `user_code` pairing from the confirmation page that `/auth/login` rendered |
| GET | `/auth/callback` | `oidc` mode only: the registered IdP redirect URI — completes the code exchange and sets `arti_session` (404 in other modes) |
| GET | `/auth/google/login` | Legacy alias for `/auth/login` (redirects in `oidc` mode) |
| GET | `/auth/logout` | Clear `arti_session`, bounce to `ARTI_LOGOUT_URL` |
| GET | `/auth/embed/app-token` | User-mode embed consent: renders the consent page to a signed-in session; a GET carrying a valid consent challenge completes the mint (see [Embed surfaces](#embed-surfaces-public-secret-authed)) |
| POST | `/auth/cli/exchange` | CLI pair flow: exchange a one-time `code` for `{ access_token, refresh_token, expires_at, email }` |
| POST | `/auth/cli/refresh` | Refresh a CLI session from `refresh_token` |
| POST | `/auth/test` | **Dev only** (`ARTI_TEST_MODE=true`): mint an HS256 token from `{ email }`, no IdP |

## Auth — device flow (RFC 8628) {#device}

Headless agents obtain a human-approved, **upload-scoped** token. Public except
`/auth/device/revoke`, which is authed (to prevent cross-user revocation). `POST
/auth/device/code` is IP-rate-limited (`ARTI_DEVICE_CODE_RPM`, default 10/min).

| Method | Path | Purpose |
|---|---|---|
| POST | `/auth/device/code` | Start the flow → `{ device_code, user_code, verification_uri, expires_in, interval }` |
| GET | `/auth/device` | Human approval page (visited with `?code=` / user code) |
| POST | `/auth/device/token` | Poll → `{ access_token, refresh_token }` once approved |
| POST | `/auth/device/refresh` | Refresh from `refresh_token` (bounded by the family max-TTL) |
| POST | `/auth/device/revoke` | **Authed**: revoke a token family |

**Upload scope is default-deny** (`internal/auth/scope_guard.go`). An upload-scoped token
may only: `GET /api/me`, `GET /api/artifacts…` (read), `POST /api/artifacts` (create), and
`POST /api/artifacts/by-slug/{slug}/append`. Everything else — delete, PATCH,
suggest-metadata, admin, MCP — is rejected `403`, even for an admin's token. POST bodies
are additionally capped at `ARTI_DEVICE_MAX_UPLOAD_BYTES` (default 200 MiB). TTLs:
`ARTI_DEVICE_TOKEN_TTL` (default 24h) and the family ceiling
`ARTI_DEVICE_TOKEN_MAX_TTL` (default 720h). See [Configuration](configuration.md#auth).

## Auth — MCP OAuth & OBO (public)

For MCP gateways (e.g. Runlayer) registering and brokering user logins without
pre-shared credentials (RFC 7591 DCR + RFC 6749 authorization-code + PKCE).

| Method | Path | Purpose |
|---|---|---|
| POST | `/oauth/register` | Dynamic client registration → `client_id` (+ `client_secret` unless `none`). IP-rate-limited (`ARTI_OAUTH_REGISTER_RPM`, default 10/min) |
| GET | `/oauth/authorize` | Authorization request (requires `code_challenge` S256, `state`); renders the consent page — issues no code. Identity comes from the `arti_session` cookie, or the proxy headers under `ARTI_AUTH_MODE=proxy`; no session redirects to `/auth/login` |
| POST | `/oauth/authorize/confirm` | Approves the consented request and redirects to the client with `code`. Same-origin: the page's signed `consent` field plus its `SameSite=Strict` identity cookie |
| POST | `/oauth/token` | `authorization_code` (with PKCE `code_verifier`) or `refresh_token` grant |
| GET | `/oauth/obo/callback` | OBO broker callback after upstream consent (the arti→upstream leg) |

Tokens are HS256 JWTs: access TTL 7 days, refresh TTL 90 days
(`cmd/arti-server/cmd_serve.go:231`). The flow and metadata are detailed under
[MCP tools → OAuth](mcp-tools.md#oauth).

## See also

- [MCP tools](mcp-tools.md) — the `/mcp` tool surface and OAuth.
- [CLI](cli.md) — the `arti` client wraps these endpoints.
- [APP SDK](app-sdk.md) — the apps proxy from inside an APP page.
- [Configuration](configuration.md) — env vars that gate auth, limits, and embed.
