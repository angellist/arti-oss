---
title: Data model
order: 2
summary: One artifacts table in Postgres holds every version of every artifact; small TEXT bodies live inline, everything else is an S3 blob referenced by a sharded key, and (slug, version) is the addressing scheme.
---

# Data model

Every artifact — TEXT, PACKAGE, APP, ATTACHMENT, MAP — is a row in one Postgres table,
`artifacts`. A row is a single *version*: a new version is a new row sharing the
slug. Bytes live inline in the row (small TEXT) or in S3 with a pointer
(everything else).

## The artifacts table

The shape that matters conceptually:

- **`artifact_id`** — a UUID primary key, scoped to a single *version*. Comments,
  app tokens, and embed tokens all key off it, so they pin to the exact version
  they were made against.
- **`named_slug` + `version`** — the human address. A slug groups many versions;
  `version` is monotonic within a slug. A NULL slug means an unnamed, single-shot
  artifact addressed only by UUID.
- **`artifact_type`** — `TEXT` | `PACKAGE` | `APP` | `ATTACHMENT` | `MAP`. MAP is
  off unless `ARTI_MAP_ARTIFACTS` is set; a MAP row also carries a `map_id`, the
  same for every version of that map.
- **`inline_content` vs `blob_ref`** — exactly one is set. Small TEXT bodies live
  in `inline_content`; everything else lives in S3 and `blob_ref` holds the object
  key (with `sha256` + `size_bytes` alongside it).
- **`scopes` / `labels`** — free-form *organizational tags* you search and filter
  by (e.g. `topic:platform:auth`, `app:couch`).
- **`metadata`** — schema-less JSONB; holds the PACKAGE manifest, declared APP
  params, and the like.
- **`allowed_access`** — the read-access ACL: `['*']` = everyone authenticated,
  `[]` = creator-only, otherwise email globs or `group:<name>` tokens.
- **`allowed_write`** — the write list. NULL means write follows read; a non-NULL
  list (empty included) is authoritative. It is always a subset of
  `allowed_access`.
- **`comments_enabled`** — the per-document comment switch. See
  [Comments](comments.md).
- **`creator`**, **`created_at` / `modified_at`**, and **`deleted_at`** — the last
  being a soft-delete (archive) tombstone; non-NULL means archived. `creator` is
  the author of *this version*; the document's owner is stored separately (see
  [Adjacent tables](#adjacent-tables)).
- **`written_via` / `written_via_name`** — the credential that wrote the version
  (for example `apikey:<id>` with the key's name, `device:<family>`, `session`, or
  `app:<artifact-uuid>`).
- **`asserted_by`** — set when a service principal wrote the version on a
  person's behalf; `creator` is then that person.

The storage invariants are enforced as CHECK constraints in the DB itself: exactly
one of `inline_content` / `blob_ref` is set, a blob row must carry its hash and
size, and inline content is capped at the inline limit.

> `scopes`/`labels` and `allowed_access` are different axes. The former are tags
> you search by; the latter is the read-access ACL. See
> [Authentication & access](auth.md) for how `allowed_access` is enforced.

## Inline vs S3

The split is a write-time policy:

- **TEXT under the inline cap (64 KiB)** → stored inline, with no S3 round-trip on
  read.
- **Everything else** — all PACKAGE, APP, ATTACHMENT, MAP, and any TEXT over the cap →
  written to S3, with `blob_ref` set to the object key.

Readers don't see the difference: an inline row returns an in-memory reader, a
blob row streams from S3.

### The S3 key scheme

Keys are `<prefix>/<shard>/<uuid>[.zip|.ndjson]`, where the prefix encodes the type
(TEXT under `artifacts`, PACKAGE/APP under `packages` with a `.zip` suffix,
ATTACHMENT under `attachments`, MAP snapshots under `artifacts` with a `.ndjson`
suffix) and `<shard>` is the first two hex chars of the UUID, keeping
prefix listings cheap.

Because the key is derived from `artifact_id` (the per-version UUID), each
version's bytes are a distinct object — versions never overwrite each other. The
blob backend is content-addressed at write, so re-putting identical bytes is
idempotent.

## Slugs and versioning

A slug maps to many versions. The addressing rules:

- **Versions are server-assigned and monotonic.** Each write to a slug appends the
  next number (one past the highest live version); callers *cannot* pin a version
  on upload. Tombstoned versions don't count. A caller may send
  `expected_latest_version`; if the slug has moved past it, the write fails with
  `409` instead of landing on top of someone else's version.
- **Uniqueness** of `(slug, version)` is enforced by a partial unique index over
  live rows. Concurrent writes to a slug race on this index, and the loser retries.
- **Resolution.** Asking for a slug with no version returns the latest non-deleted
  version; asking for a specific version pins it. The catalog's default list view
  collapses each slug to its highest live version.
- **URLs.** A named artifact is `/s/<slug>` (latest) or `/s/<slug>/<version>`; a
  slug-less one is `/a/<uuid>`.

There is also an in-place "add to a doc" path: it reads the latest version's body,
concatenates the new content onto it, and writes the result as the next version.
It refuses PACKAGE/APP (concat would corrupt the zip), ATTACHMENT (slugless by
design) and MAP (its versions are snapshots of the keyed head).

## PACKAGE manifests

A PACKAGE (and an APP, which is a PACKAGE that also declares a tool allowlist) is a
zip. On upload, arti reads the zip's central directory and produces a structural
manifest stored in `metadata`:

```json
{
  "format": "zip",
  "entry_point": "index.html",
  "entries": [
    { "path": "index.html", "size": 1234, "sha256": "…", "content_type": "text/html" },
    { "path": "style.css",   "size": 567,  "sha256": "…", "content_type": "text/css" }
  ]
}
```

`entry_point` is the file served at the package root; it's guessed unless the
upload overrides it, and macOS `__MACOSX` sidecars are dropped. The manifest is
what the viewer's file tree and the file-serving path read; see
[App serving](app-serving.md) and [Attachments](attachments.md) for the serving
side.

## Adjacent tables {#adjacent-tables}

Other domains have their own tables:

- **Document owners** (`artifact_owners`) — one row per slug naming its owner.
  The first version claims it and it stays put through later versions, archive
  and ACL changes; only an explicit transfer moves it. A slugless artifact has no
  row: its owner is its creator (or `asserted_by`, when set).
- **Comment threads + comments** — per-artifact-version commenting, cascade-deleted
  with the artifact. Each comment records its `source` (`web`, `embed` or `mcp`).
  See [Comments](comments.md).
- **MCP-OAuth, OBO, and device-auth tables** — the auth flows. See
  [Authentication & access](auth.md).
- **API keys** — self-serve and admin-minted keys, stored as a SHA-256 hash, with
  `minted_by` set when an admin minted the key for a service principal.
- **Credential usage + alerts** — per-credential, per-day counts by network and
  user agent (kept 90 days), and a record of which new networks an owner was
  already told about.
- **MAP entries** — one row per key of a MAP's live head, keyed by the map's
  `map_id`, with its value, revision and last writer. The artifact rows for that
  slug hold snapshots of this table, not the keys themselves. See
  [Concepts](../overview/concepts.md#artifact-types).
- **Page views** (`artifact_views`) — one row per human-facing view, with the
  served version and the surface (a repeat by the same person on the same surface
  within 30 seconds is not recorded); counts group by slug, or by UUID for a
  slugless artifact.
- **Bookmarks** (`artifact_bookmarks`) — one row per (slug or UUID, person).
- **Block list** (`artifact_blocks`) — slug, slug-glob or id patterns an admin
  has taken away from everyone. See [Authentication & access](auth.md).
- **Share links + opens** — external timed links and their open log (off unless
  `ARTI_SHARE_ENABLED` is set).
- **Users** — the roster: every person arti has seen (backfilled, recorded at
  login, or added by an admin), their `kind` (`human` or `service`), and
  deactivation.
- **App MCP connectors** (`app_mcp_servers`) — the upstream MCP servers APPs may
  reach. See [App serving](app-serving.md).
- **Notification settings** — the deployment switch and each person's own Slack
  notification choices.
- **Idempotency keys** — dedup for write retries.
- **User groups, captured IdP groups, RBAC roles and assignments** — access
  control.
- **LLM usage** — the budget ledger for the built-in `llm.complete`.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
