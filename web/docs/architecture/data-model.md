---
title: Data model
order: 2
summary: One artifacts table in Postgres holds every version of every artifact; small TEXT bodies live inline, everything else is an S3 blob referenced by a sharded key, and (slug, version) is the addressing scheme.
---

# Data model

Every artifact — TEXT, PACKAGE, APP, ATTACHMENT — is a row in one Postgres table,
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
- **`artifact_type`** — `TEXT` | `PACKAGE` | `APP` | `ATTACHMENT`.
- **`inline_content` vs `blob_ref`** — exactly one is set. Small TEXT bodies live
  in `inline_content`; everything else lives in S3 and `blob_ref` holds the object
  key (with `sha256` + `size_bytes` alongside it).
- **`scopes` / `labels`** — free-form *organizational tags* you search and filter
  by (e.g. `topic:platform:auth`, `app:couch`).
- **`metadata`** — schema-less JSONB; holds the PACKAGE manifest, declared APP
  params, and the like.
- **`allowed_access`** — the read-access ACL: `['*']` = everyone authenticated,
  `[]` = creator-only, otherwise email globs or `group:<name>` tokens.
- **`creator`**, **`created_at` / `modified_at`**, and **`deleted_at`** — the last
  being a soft-delete (archive) tombstone; non-NULL means archived.

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
- **Everything else** — all PACKAGE, APP, ATTACHMENT, and any TEXT over the cap →
  written to S3, with `blob_ref` set to the object key.

Readers don't see the difference: an inline row returns an in-memory reader, a
blob row streams from S3.

### The S3 key scheme

Keys are `<prefix>/<shard>/<uuid>[.zip]`, where the prefix encodes the type (TEXT
under `artifacts`, PACKAGE/APP under `packages` with a `.zip` suffix, ATTACHMENT
under `attachments`) and `<shard>` is the first two hex chars of the UUID, keeping
prefix listings cheap.

Because the key is derived from `artifact_id` (the per-version UUID), each
version's bytes are a distinct object — versions never overwrite each other. The
blob backend is content-addressed at write, so re-putting identical bytes is
idempotent.

## Slugs and versioning

A slug maps to many versions. The addressing rules:

- **Versions are server-assigned and monotonic.** Each write to a slug appends the
  next number (one past the highest live version); callers *cannot* pin a version
  on upload. Tombstoned versions don't count.
- **Uniqueness** of `(slug, version)` is enforced by a partial unique index over
  live rows. Concurrent writes to a slug race on this index, and the loser retries.
- **Resolution.** Asking for a slug with no version returns the latest non-deleted
  version; asking for a specific version pins it. The catalog's default list view
  collapses each slug to its highest live version.
- **URLs.** A named artifact is `/s/<slug>` (latest) or `/s/<slug>/<version>`; a
  slug-less one is `/a/<uuid>`.

There is also an in-place "add to a doc" path: it reads the latest version's body,
concatenates the new content onto it, and writes the result as the next version.
It refuses PACKAGE/APP (concat would corrupt the zip) and ATTACHMENT (slugless by
design).

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

## Adjacent tables

Other domains have their own tables:

- **Comment threads + comments** — per-artifact-version commenting, cascade-deleted
  with the artifact. See [Comments](comments.md).
- **MCP-OAuth, OBO, and device-auth tables** — the auth flows. See
  [Authentication & access](auth.md).
- **Idempotency keys** — dedup for write retries.
- **User groups + RBAC roles and assignments** — access control.
- **LLM usage** — the budget ledger for the built-in `llm.complete`.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
