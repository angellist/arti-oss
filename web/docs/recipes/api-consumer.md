---
title: As an API consumer
order: 2
summary: Integrate arti's REST API from a service or script — auth, the create/fetch/list/search endpoints, pagination, filtering, content types, and error handling, with real curl.
---

# As an API consumer

You're writing a service or script that talks to arti over HTTP — to publish
generated documents, mirror a catalog, or pull artifacts into another system.
This page is the task-oriented tour; the [REST API reference](../reference/rest-api.md)
is the exhaustive surface.

Base URL in production: `https://arti.example.com`. Every example
below is relative to it.

## Authenticate

Every `/api/*` route requires a bearer token (`Authorization: Bearer <token>`).
How you get one depends on what you are — the [Using the API](../guides/api.md) guide
compares all the options; the common ones:

- **A service or script you own** — mint a self-serve
  [API key](../guides/api.md#api-keys-self-serve) (`arti_upload_…`). Long-lived, tied
  to your email, and `upload`-scoped (create, append, read only). The least ceremony
  for a standing uploader.
- **A headless agent / sandbox** where a human should vouch — use the
  [device flow](agent.md) to get an `ARTI_TOKEN`, also `upload`-scoped.
- **A service with a full session token** — a normal user/service JWT (from the
  CLI login or the SSO gateway) carries full access subject to the artifact's
  access rules.

The access rules are the same regardless of token: you see an artifact if its
`allowed_access` patterns match your email (`*` = everyone authenticated; an
empty list = creator-only). Admins with `MANAGE_ARTIFACTS` bypass read filtering.

## Create an artifact

`POST /api/artifacts`. JSON for text or small payloads; multipart for binary.
The four artifact types are `TEXT`, `PACKAGE`, `APP`, `ATTACHMENT`
(`internal/store/pgstore/store.go`). Global upload cap: 200 MiB. Reusing a
`named_slug` publishes the next version — but only if you can *read* the existing slug;
otherwise it's a `404`, so you can't fork or probe someone else's slug.

### TEXT (JSON)

```sh
curl -sX POST https://arti.example.com/api/artifacts \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "artifact_type": "TEXT",
    "named_slug": "weekly-report",
    "title": "Weekly Report 2026-06-19",
    "content_type": "text/markdown",
    "content": "# Summary\n\n…",
    "labels": ["report", "weekly"],
    "allowed_access": ["*@example.com"]
  }'
```

Key body fields (`internal/artifacts/dto.go`, `CreateRequest`):

| Field | Notes |
|---|---|
| `artifact_type` | `TEXT` (default), `PACKAGE`, `APP`, `ATTACHMENT`. |
| `named_slug` | Optional. Omit → a UUID-only artifact. Reusing a slug creates the next version. |
| `title` | Required. |
| `content_type` | Required. TEXT must be `text/*`, `application/json`, `application/yaml`, or `application/javascript`. |
| `content` | Raw UTF-8 text. |
| `content_base64` | Binary alternative to `content`. |
| `labels` | String array; trimmed + deduped. |
| `scopes` | Arbitrary grouping strings (e.g. `a:bt-auto-route`). |
| `allowed_access` | Email-glob patterns. Absent → inherit prior version, else `["*"]`. Empty `[]` → creator-only. |
| `entry_point` | PACKAGE/APP entry file override. |
| `ensure_new` | With `named_slug`: `409` if the slug already exists (don't accidentally version someone else's doc). |

Success is `201` with the artifact metadata (`ArtifactInfo`, below), including a
canonical `url`.

### Binary (multipart)

Send raw bytes — no base64. Form fields: `file` (the bytes), `title`,
`content_type`, optional `artifact_type`, `named_slug`, `description`, and
repeatable `labels`.

```sh
curl -sX POST https://arti.example.com/api/artifacts \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@diagram.pdf' \
  -F 'title=Architecture diagram' \
  -F 'content_type=application/pdf' \
  -F 'artifact_type=ATTACHMENT' \
  -F 'labels=design'
```

### PACKAGE / APP

A `PACKAGE` is a zip; an `APP` is a zip that also contains `arti-app.json`
(see [As an arti APP developer](app-developer.md)). Send the zip bytes via
`content_base64`, or upload the directory with the CLI (`arti add ./dir`), which
zips deterministically for you.

## Append to a running log

`POST /api/artifacts/by-slug/{slug}/append` adds content as a new version
without you fetching-then-reposting. It's the right primitive for an append-only
log an agent writes to repeatedly.

```sh
curl -sX POST https://arti.example.com/api/artifacts/by-slug/run-log/append \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "content": "2026-06-19T10:00Z step 3 ok",
    "separator": "\n",
    "idempotency_key": "step-3-2026-06-19",
    "title": "Run log",
    "content_type": "text/markdown"
  }'
```

- **Idempotency.** The server caches the first success per `(idempotency_key,
  creator)` for 24h and replays it on retry, returning header
  `X-Arti-Idempotent-Replay: true`. Use a stable hash of the logical event so a
  crash-retry doesn't double-append.
- **Auto-create.** If the slug doesn't exist, the first append creates v1 —
  `title` and `content_type` are required in that path, optional thereafter.
- **Separator.** Inserted between old and new body; default `"\n\n"`, pass `""`
  for none.
- Fields not supplied (`scopes`, `labels`, `allowed_access`) inherit from the
  prior version.

## Fetch

| Goal | Request |
|---|---|
| Metadata by UUID | `GET /api/artifacts/{id}/meta` |
| Content by UUID | `GET /api/artifacts/{id}` |
| Metadata by slug (latest) | `GET /api/artifacts/by-slug/{slug}` |
| Content by slug | `GET /api/artifacts/by-slug/{slug}/raw` |
| A specific version | append `?version=N` to either by-slug route |
| All versions of a slug | `GET /api/artifacts/by-slug/{slug}/versions` |
| Files in a PACKAGE/APP | `GET /api/artifacts/{id}/files` |
| One file from a PACKAGE/APP | `GET /api/artifacts/{id}/files/{path}` |

Metadata responses are `ArtifactInfo`:

```json
{
  "artifact_id": "550e8400-e29b-41d4-a716-446655440000",
  "artifact_type": "TEXT",
  "named_slug": "weekly-report",
  "version": 3,
  "title": "Weekly Report 2026-06-19",
  "description": null,
  "content_type": "text/markdown",
  "size_bytes": 4096,
  "sha256": "…",
  "creator": "alice@example.com",
  "scopes": [],
  "labels": ["report", "weekly"],
  "allowed_access": ["*@example.com"],
  "metadata": {},
  "created_at": "2026-06-19T10:30:00Z",
  "modified_at": "2026-06-19T10:30:00Z",
  "deleted_at": null,
  "url": "https://arti.example.com/s/weekly-report/3"
}
```

A by-slug fetch returns the latest version *the caller can read* — newer
restricted versions stay hidden.

## List, search, filter

`GET /api/artifacts` lists the catalog; `GET /api/artifacts/search?q=…` adds
substring matching on title/description. Both share the same filter and
pagination params and return `ListResponse` (`{ "artifacts": [...], "total": N }`).

List and search rows carry two extra fields that single-artifact fetches don't:
`comment_count` (live comments on that *version* — comments anchor to
`artifact_id`) and `open_thread_count` (how many of its threads are unresolved).
They are computed for the whole page in one query. Both are omitted, rather than
zero, if that count could not be produced — so treat "absent" as unknown and `0`
as genuinely no comments.

### Pagination

| Param | Default | Notes |
|---|---|---|
| `limit` | 50 | Page size. |
| `offset` | 0 | Skip N rows. |
| `order_by` | — | `title`, `type`, `slug`, `version`, `creator`, `scope`, `created`. |
| `order_dir` | — | `asc` / `desc`. |

`total` is the unpaginated count, so you can page with
`offset += limit` until `offset >= total`.

### Filtering

| Param | Effect |
|---|---|
| `type` | `TEXT`, `PACKAGE`, `APP`, `ATTACHMENT`, or pseudo-types `MARKDOWN`/`HTML` (match content-type prefix). |
| `creator` | Creator-email substring. |
| `scope` | Exact scope match. |
| `slug` | Exact slug (drills into one slug, shows its versions). |
| `label` | Repeatable; multiple labels are ANDed. |
| `all_versions=true` | Show every version, not just the latest per slug. |
| `archived` | `only`, `include`, or unset (hide archived). |

```sh
# every report-labeled markdown doc by one author, newest first, page 1
curl -s -H "Authorization: Bearer $TOKEN" \
  'https://arti.example.com/api/artifacts?type=MARKDOWN&label=report&creator=tian&order_by=created&order_dir=desc&limit=25'
```

`search` additionally parses `field:value` tokens in `q` — `slug:`, `creator:`,
`scope:`, `type:`, `label:`, each negatable with a leading `-`. Bare words are
title/description substring search:

```sh
curl -s -H "Authorization: Bearer $TOKEN" \
  --data-urlencode 'q=label:eval -type:ATTACHMENT nightly' \
  -G 'https://arti.example.com/api/artifacts/search'
```

For building faceted UIs, `GET /api/artifacts/aggregates` returns top-30
histograms of scopes, scope-types, and labels (cached 60s per caller).

## Error handling

Non-2xx responses are JSON: `{ "detail": "...", "code": "..." }`. Switch on the
HTTP status; `code` is a stable machine string for the common cases.

| Status | `code` | When |
|---|---|---|
| `400` | `bad-request` | Bad JSON, wrong content-type for the type, missing `title`, invalid UUID. |
| `403` | `forbidden` | Not the creator (delete/patch), or upload-scoped token hitting a denied route. |
| `404` | `not-found` | Missing, or you lack read access (access denial is masked as 404, not 403). |
| `409` | `slug-exists` / `conflict` | `ensure_new` and slug taken; or a concurrent append lost the race. |
| `410` | `idempotency-stale` | A cached idempotent reply points at a now-inaccessible artifact. |
| `413` | `too-large` | Body over the limit (200 MiB global; 25 MiB for upload-scoped tokens). |
| `500` | `internal` | Server-side fault. |

Treat `409 conflict` on append as retryable; `404` on a fetch means "gone or not
yours" — don't retry.

## Related

- [REST API reference](../reference/rest-api.md) — every endpoint, param, and field.
- [As an agent (headless)](agent.md) — getting an upload token without a browser.
- [Concepts](../overview/concepts.md) — slugs, versions, scopes, labels.
- [Data model](../architecture/data-model.md) — how artifacts are stored.
