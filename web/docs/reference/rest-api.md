---
title: REST API
order: 1
summary: Every arti HTTP endpoint — artifacts, comments, groups, roles, API keys, admin, auth/device, apps proxy, embed, and health — with auth, params, and responses.
---

# REST API

arti's HTTP surface, served by `arti-server`. Routing is registered in
`cmd/arti-server/cmd_serve.go`; the artifact handlers live in
`internal/artifacts/server.go`. The bundled `GET /openapi.yaml` is a hand-maintained
subset — **this page is the authoritative list** (it covers the routes the spec omits:
APP serving, `suggest-metadata`, `aggregates`, append, unarchive, PATCH, comments,
groups, roles, admin, auth, device, apps, embed).

Base URLs: `https://arti.example.com` (prod) and `http://localhost:8090`
(dev). All paths below are relative to the base URL.

## Conventions

- **Auth groups.** Routes are either **public** (no SSO — registered directly on the
  root router, often with their own token or shared-secret auth) or **authed**
  (registered inside the `r.Use(authMiddleware)` group). Authed routes accept either an
  `arti_session` cookie (browser), a `Bearer` JWT (CLI/agent/service), or an
  `arti_upload_…` [API key](#api-keys) (also sent as `Authorization: Bearer`;
  upload-scoped). See the [auth architecture](../architecture/auth.md).
- **Identity.** Authed routes attribute every action to the caller's email.
- **Errors.** Failures return `{ "detail": string, "code": string }` with the matching
  HTTP status (`internal/artifacts/dto.go:183`).
- **`ArtifactInfo`** is the shared metadata DTO (below). UUID = artifact id; slug =
  named slug.

### `ArtifactInfo`

`internal/artifacts/dto.go:14`. Note the field names differ from the stale
`openapi.yaml` (`scopes`/`labels`/`allowed_access` are plural arrays; `artifact_type`
includes `APP`).

| Field | Type | Notes |
|---|---|---|
| `artifact_id` | string (UUID) | |
| `artifact_type` | string | `TEXT` \| `PACKAGE` \| `APP` \| `ATTACHMENT` |
| `named_slug` | string \| null | |
| `version` | int \| null | |
| `title` | string | |
| `description` | string \| null | |
| `content_type` | string | MIME |
| `size_bytes` | int64 \| null | |
| `sha256` | string \| null | |
| `creator` | string | email |
| `scopes` | string[] | e.g. `["a:bt-auto-route","u:alice"]` |
| `labels` | string[] | |
| `allowed_access` | string[] | glob-on-email patterns; `["*"]` = any authed reader; `[]` = creator-only |
| `metadata` | object | arbitrary; PACKAGE/APP carry a `package` sub-object |
| `created_at` / `modified_at` | RFC3339 | |
| `deleted_at` | RFC3339 \| null | set when archived |
| `url` | string | canonical artifact URL |

## Artifacts (authed)

Registered by `artifacts.Mount` (`internal/artifacts/server.go:764`).

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/artifacts` | Create or version an artifact |
| POST | `/api/artifacts/suggest-metadata` | LLM-suggest title/slug/labels from content |
| GET | `/api/artifacts` | List (filterable) |
| GET | `/api/artifacts/search` | Substring search |
| GET | `/api/artifacts/aggregates` | Label/scope histograms for the catalog sidebar |
| GET | `/api/artifacts/{id}` | Fetch metadata + serve content |
| GET | `/api/artifacts/{id}/meta` | Metadata only |
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
| DELETE | `/api/artifacts/by-slug/{slug}` | Archive **all** versions under a slug |
| GET | `/app/{ident}` · `/app/{ident}/{version}` | Serve an APP artifact full-page |
| GET | `/api/me` | Current caller's identity + permissions |

### POST `/api/artifacts`

Create a new artifact or a new version of an existing slug. Accepts
`application/json` **or** `multipart/form-data` (the latter for binary uploads;
`internal/artifacts/server.go:811`). Adding a version to an **existing** slug requires
read access to it — a caller who can't read the slug gets `404` (it can't be silently
forked or probed).

JSON body (`CreateRequest`, `internal/artifacts/dto.go:96`):

| Field | Type | Notes |
|---|---|---|
| `title` | string | required |
| `content_type` | string | required (MIME) |
| `artifact_type` | string | `TEXT` (default) \| `PACKAGE` \| `APP` \| `ATTACHMENT` |
| `content` | string | raw text body |
| `content_base64` | string | base64 body (binary) — alternative to `content` |
| `named_slug` | string | optional; omitting it makes an unnamed artifact |
| `description` | string | |
| `scopes` | string[] | (`scope` singular is deprecated) |
| `labels` | string[] | |
| `metadata` | object | arbitrary |
| `entry_point` | string | PACKAGE/APP launch file |
| `ensure_new` | boolean | with `named_slug`: 409 if the slug already exists (no auto-version) |
| `allowed_access` | string[] | omit → inherit-from-prior or default `["*"]`; `[]` → creator-only |

Response: `201` + `ArtifactInfo`. `409` if `ensure_new` and the slug exists; `413` if the
body exceeds the size cap.

### POST `/api/artifacts/by-slug/{slug}/append`

Append to a slug as a new version, atomically (`body = prior + separator + content`).
Auto-creates v1 when the slug doesn't exist (then `title` + `content_type` are required).
Binary content types are rejected — use a PACKAGE/ATTACHMENT instead.

Body (`AppendRequest`, `internal/artifacts/dto.go:146`): `content` (required),
`separator` (default `"\n\n"`, `""` for none), `idempotency_key` (24h dedup per
`(key, creator)`), `title`/`description`/`content_type`/`scopes`/`labels`/`allowed_access`
(optional overrides; nil inherits). Response `200` + `ArtifactInfo`; replays carry
`X-Arti-Idempotent-Replay: true`.

### POST `/api/artifacts/suggest-metadata`

Given `{ filename, content_type, artifact_type?, sample }`, returns
`{ "title", "slug", "labels" }` from the built-in metadata completer (haiku). Requires
`ANTHROPIC_API_KEY` to be configured.

### GET `/api/artifacts` and `/api/artifacts/search`

List/search query params: `limit` (default 50), `offset` (default 0), `type`,
`creator`, `scope`, `label` (repeatable), `include_archived`. Search additionally
requires `q`. Both return `{ "artifacts": ArtifactInfo[], "total": int }`. Non-admins see
only artifacts they can read (their email + group memberships); admins with
`MANAGE_ARTIFACTS` see all.

### PATCH `/api/artifacts/{id}`

Edit mutable fields: `{ title?, scopes?, labels?, allowed_access? }`. Creator or a holder
of `MANAGE_ARTIFACTS`; writing a `kind:skill` artifact additionally requires
`MANAGE_SKILLS`. Response `200` + updated `ArtifactInfo`.

### DELETE `/api/artifacts/{id}` and `/api/artifacts/by-slug/{slug}`

Archive (soft-delete). By id: `204`. By slug: archives every version, returns
`{ "archived_count": int }`. `?hard=true` on the id route hard-deletes and is gated by
`ARTI_ADMIN_EMAILS`. Authorization: creator (of the version, or of every version for the
slug route) or admin.

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

### GET `/api/me`

Returns `{ email, is_admin, name, picture, permissions: string[] }`
(`internal/artifacts/server.go:1219`). `is_admin` is derived from holding the `ADMIN`
role; `permissions` is the effective permission set used for FE capability gating.

## Comments (authed)

Registered by `commentsSvc.Mount` (`internal/comments/`). All scoped to artifacts the
caller can read.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/artifacts/{id}/comments` | List threads (with comments) |
| POST | `/api/artifacts/{id}/comments` | Create a thread — body `{ anchor, body }` → `201` Thread |
| POST | `/api/comments/{threadID}/replies` | Reply — body `{ body }` → `201` Comment |
| POST | `/api/comments/{threadID}/resolve` | Mark resolved → `204` |
| POST | `/api/comments/{threadID}/reopen` | Reopen → `204` |
| PUT | `/api/comments/{threadID}/comments/{commentID}` | Edit (author only) — body `{ body }` |
| DELETE | `/api/comments/{threadID}/comments/{commentID}` | Delete (author only) → `204` |

A **Thread** is `{ id, anchor, status (open\|resolved), created_by, created_at,
resolved_by?, comments: Comment[] }`; a **Comment** is `{ id, author, author_name,
author_picture?, body, created_at, edited_at? }`.

## Comments embed (public, token-authed)

For the comments overlay injected into sandboxed served-HTML pages, which can't send the
session cookie. Authenticated by a per-artifact scoped Bearer token minted server-side
and CORS-enabled (`commentsSvc.MountEmbed`). Same operations as above, under the
`/api/embed/` prefix:

| Method | Path |
|---|---|
| OPTIONS / GET / POST | `/api/embed/artifacts/{id}/comments` |
| POST | `/api/embed/comments/{threadID}/replies` |
| POST | `/api/embed/comments/{threadID}/resolve` · `/reopen` |
| PUT / DELETE | `/api/embed/comments/{threadID}/comments/{commentID}` |

## Apps proxy (public, app-token-authed) {#apps-proxy}

The governed MCP back end for APP artifacts (`appsSvc.MountProxy`,
`internal/apps/apps.go:125`). Authenticated by the injected, app-scoped Bearer token
(not the session cookie), CORS-enabled.

| Method | Path | Purpose |
|---|---|---|
| OPTIONS | `/api/apps/mcp` | CORS preflight |
| POST | `/api/apps/mcp` | Forward one `tools/call` to a named upstream MCP server |

Body: `{ app_id, server, tool, arguments }`. The proxy validates the token, enforces the
app's `arti-app.json` allowlist, resolves the server (URL + auth policy live
server-side), attaches the viewer's OBO credential for `oauth` servers, and forwards the
call. Returns the upstream MCP result, or `401 { error: "authorization_required",
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

A group DTO carries `{ name, display_name, members, member_count, token (group:name),
created_by, created_at, modified_at }`.

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

## Admin (authed, `MANAGE_ARTIFACTS`)

`admin.NewService().Mount` (`internal/admin/`).

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/admin/stats` | Read-only operational snapshot (artifact counts by type; comment thread/activity counts) |

## API keys (authed) {#api-keys}

Self-serve, personal bearer keys for programmatic upload (`apikeys.NewService().Mount`,
`internal/apikeys/service.go`). A key looks like `arti_upload_XXXXX…`; arti stores only
its SHA-256, so the plaintext is returned **once**, at creation. A key carries the
`upload` scope — so an API-key-authed request runs through the same default-deny
[upload-scope guard](#device) (create/append/read only, 25 MiB body cap) as a device
token. Revocation and expiry are enforced at authentication (a revoked or expired key
fails to authenticate). Managing your own keys needs no special permission; the
cross-owner admin view needs `MANAGE_API_KEYS`.

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/keys` | Mint a key → `201` with the plaintext `key` (shown once) |
| GET | `/api/keys` | List your keys (`?all=true` → every owner, needs `MANAGE_API_KEYS`) |
| DELETE | `/api/keys/{id}` | Revoke (soft) — your own key, or any key with `MANAGE_API_KEYS` |

### POST `/api/keys`

Requires a **full-access** credential (session cookie or CLI token) — an upload-scoped
credential (a device token or another API key) is rejected `403`, and the mint route is
IP-rate-limited (`ARTI_API_KEY_RPM`).

Body: `{ name, scopes?, ttl_days? }`. `name` is required. `scopes` defaults to
`["upload"]` and must be exactly one supported scope (v1: only `upload`). `ttl_days`
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

`GET /api/keys` returns an array of that view **without** `key` (includes revoked/expired
keys, newest first). `DELETE /api/keys/{id}` soft-revokes (`204`; `404` if it isn't yours
and you lack `MANAGE_API_KEYS`). See the [Using the API](../guides/api.md#api-keys-self-serve)
guide for the how-to.

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
| GET | `/.well-known/oauth-protected-resource` | RFC 9728 resource metadata |
| GET | `/.well-known/oauth-authorization-server` | RFC 8414 AS metadata |

## Auth — sessions & CLI (public)

| Method | Path | Purpose |
|---|---|---|
| GET | `/auth/login` | Browser login front door, per `ARTI_AUTH_MODE`. `oidc`: starts the authorization-code + PKCE flow at the configured issuer; `proxy`: mints from the trusted proxy headers. Mints `arti_session` (browser) or pairs `cli_code`→email (CLI) |
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
are additionally capped at `ARTI_DEVICE_MAX_UPLOAD_BYTES` (default 25 MiB). TTLs:
`ARTI_DEVICE_TOKEN_TTL` (default 24h) and the family ceiling
`ARTI_DEVICE_TOKEN_MAX_TTL` (default 720h). See [Configuration](configuration.md#auth).

## Auth — MCP OAuth & OBO (public)

For MCP gateways (e.g. Runlayer) registering and brokering user logins without
pre-shared credentials (RFC 7591 DCR + RFC 6749 authorization-code + PKCE).

| Method | Path | Purpose |
|---|---|---|
| POST | `/oauth/register` | Dynamic client registration → `client_id` (+ `client_secret` unless `none`). IP-rate-limited (`ARTI_OAUTH_REGISTER_RPM`, default 10/min) |
| GET | `/oauth/authorize` | Authorization request (requires `code_challenge` S256, `state`); redirects with `code` |
| POST | `/oauth/token` | `authorization_code` (with PKCE `code_verifier`) or `refresh_token` grant |
| GET | `/oauth/obo/callback` | OBO broker callback after upstream consent (the arti→upstream leg) |

Tokens are HS256 JWTs: access TTL 7 days, refresh TTL 90 days
(`cmd/arti-server/cmd_serve.go:227`). The flow and metadata are detailed under
[MCP tools → OAuth](mcp-tools.md#oauth).

## See also

- [MCP tools](mcp-tools.md) — the `/mcp` tool surface and OAuth.
- [CLI](cli.md) — the `arti` client wraps these endpoints.
- [APP SDK](app-sdk.md) — the apps proxy from inside an APP page.
- [Configuration](configuration.md) — env vars that gate auth, limits, and embed.
