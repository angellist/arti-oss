---
title: MCP tools
order: 2
summary: arti's own MCP server — the 12 tools it exposes at /mcp, their input schemas and results, and the OAuth flow MCP clients use to connect.
---

# MCP tools

arti exposes its artifact catalog as an MCP server at `POST /mcp`
(`internal/mcp/server.go`). Any MCP client — an agent runtime, Runlayer, Claude — can
list and call these tools to create, version, read, search, edit, and comment on artifacts.

This is **arti's own** MCP server. It is distinct from the [apps proxy](app-sdk.md),
which lets APP artifacts reach *other* upstream MCP servers.

## The `/mcp` endpoint

| Property | Value |
|---|---|
| Path | `POST /mcp` |
| Transport | Streamable HTTP, JSON-RPC 2.0 |
| Protocol version | `2024-11-05` |
| Server info | `{ name: "arti", version: "0.1.0" }` |
| Auth | Bearer JWT or `arti_session` cookie (mounted inside the authed group) |

> If your organization runs an MCP gateway, prefer reaching this endpoint
> through it rather than connecting directly — see
> [Using the MCP server](../guides/mcp.md#authentication).

Supported JSON-RPC methods (`internal/mcp/server.go:75`):

| Method | Purpose |
|---|---|
| `initialize` | Handshake; returns `protocolVersion`, `capabilities`, `serverInfo` |
| `tools/list` | Enumerate the tools below |
| `tools/call` | Invoke a tool by `name` with `arguments` |
| `ping` | Liveness |

Every tool reply uses the MCP envelope: a `content` array of
`{ "type": "text", "text": "…" }`, where the text is the JSON-serialized result. Tool
errors are returned in the JSON-RPC `error` object; the HTTP status stays `200`.

Calls are attributed to the authenticated caller. List/search results are filtered to
what the caller can read (their email + group memberships); admins with
`MANAGE_ARTIFACTS` see everything. Archive and edit enforce creator-or-admin.

## Tools

12 tools (`internal/mcp/server.go:120`). `ident` accepts a UUID **or** a named slug.
`version` is optional — for a slug it defaults to the latest version the caller can read.

### `add_artifact`

Create (or version) an artifact.

> Create (or version) an artifact. Default type=TEXT. Pass ensure_new=true with a
> named_slug to fail (409) if that slug already exists. Pass allowed_access to gate who
> can read this version … Empty array = creator-only. Pass scopes as an array …; the
> singular "scope" is deprecated.

| Param | Type | Required | Notes |
|---|---|---|---|
| `title` | string | yes | |
| `content_type` | string | yes | MIME |
| `content` | string | no | raw text body |
| `content_base64` | string | no | base64 body (binary) |
| `artifact_type` | string (`TEXT`\|`PACKAGE`\|`APP`) | no | default `TEXT` |
| `named_slug` | string | no | |
| `description` | string | no | |
| `scope` | string | no | deprecated; use `scopes` |
| `scopes` | string[] | no | e.g. `["a:bt-auto-route","u:alice"]` |
| `labels` | string[] | no | |
| `entry_point` | string | no | PACKAGE/APP launch file |
| `ensure_new` | boolean | no | 409 if the slug exists |
| `allowed_access` | string[] | no | glob-on-email; `["*"]` any reader, `[]` creator-only |
| `allowed_write` | string[] | no | subset of readers allowed to write; omit = writers follow readers, `[]` = creator-only writes |

**Returns:** `ArtifactInfo` (see [REST API](rest-api.md#artifactinfo)).

### `append_artifact`

Append text to a slug as a new version, atomically (`body = prior || separator ||
content`). Auto-creates v1 if the slug is new (`title` + `content_type` required then).
Binary content types are rejected. `idempotency_key` makes retries safe — the server
caches the response 24h per `(key, creator)` and replays it.

| Param | Type | Required | Notes |
|---|---|---|---|
| `slug` | string | yes | |
| `content` | string | yes | text to append |
| `separator` | string | no | default `"\n\n"` |
| `idempotency_key` | string | no | stable hash of the logical event |
| `title` | string | no | required on auto-create |
| `description` | string | no | |
| `content_type` | string | no | required on auto-create |
| `scopes` / `labels` / `allowed_access` | string[] | no | nil → inherit from prior |
| `allowed_write` | string[] | no | subset of readers allowed to write; nil → inherit / writers follow readers, `[]` → creator-only writes |

**Returns:** `{ "artifact": ArtifactInfo, "idempotent_replay": boolean }`.

### `update_artifact`

Update an existing artifact's **metadata** in place — `title`, `scopes`, `labels`,
and/or `allowed_access` — **without** creating a new version. Content and
`artifact_type` are immutable: to change the body, call `add_artifact` with the same
`named_slug` to publish a new version. Only the fields you pass change; omit a field to
leave it untouched, or pass an empty array to clear it (e.g. `allowed_access: []` →
creator-only). The edit applies to a single version — for a slug, the latest version the
caller can read; sibling versions keep their prior metadata. Creator-or-`MANAGE_ARTIFACTS`
only; editing a `kind:skill` artifact also requires `MANAGE_SKILLS`.

| Param | Type | Required | Notes |
|---|---|---|---|
| `ident` | string | yes | UUID (that version) or slug (latest readable version) |
| `version` | integer | no | pin a slug to a specific version |
| `title` | string | no | non-empty when present |
| `scopes` | string[] | no | replaces the set; `[]` clears |
| `labels` | string[] | no | replaces the set; `[]` clears |
| `allowed_access` | string[] | no | glob-on-email; `[]` → creator-only |
| `allowed_write` | string[] | no | subset of readers allowed to write; omit → writers follow readers, `[]` → creator-only writes (unioned into `allowed_access`) |
| `comments_enabled` | bool | no | per-DOCUMENT comment switch — applies to every version of the slug, and only the artifact's OWNER (earliest version's creator) or an admin may set it |

**Returns:** the refreshed `ArtifactInfo`.

### `get_artifact`

Fetch metadata for one artifact by UUID or slug.

| Param | Type | Required |
|---|---|---|
| `ident` | string | yes |
| `version` | integer | no |

**Returns:** `ArtifactInfo` (metadata only, no content) — including `size_bytes` and
`sha256`. This is the cheap way to gauge how large an artifact is *before* reading it:
for a large (blob-backed) artifact it doesn't transfer the content.

### `read_artifact`

Fetch the content of an artifact. Textual content_types come back as UTF-8 text; other
types are base64-encoded. Pass `max_bytes` to read only the first N bytes of a large
artifact instead of pulling it whole into context.

| Param | Type | Required | Notes |
|---|---|---|---|
| `ident` | string | yes | UUID or slug |
| `version` | integer | no | slug → latest readable |
| `max_bytes` | integer | no | cap the bytes returned; the reply flags `truncated` |

**Returns:** the content envelope (text, or base64 for binary —
`internal/mcp/server.go`), annotated with `size_bytes` (full artifact size), `sha256`,
`returned_bytes` (how many this reply carries), and `truncated` (true when `max_bytes`
cut it short). `size_bytes`/`sha256` describe the whole artifact, so they're accurate
even on a capped read.

### `list_artifacts`

List artifacts (filterable). All params optional.

| Param | Type | Notes |
|---|---|---|
| `type` | string | filter by artifact type |
| `creator` | string | filter by creator email |
| `scope` | string | filter by scope |
| `labels` | string[] | filter by labels |
| `limit` / `offset` | integer | pagination |
| `include_archived` | boolean | non-admins still see only their own archived |

**Returns:** `{ "artifacts": ArtifactInfo[], "total": int }`. Each `ArtifactInfo`
carries `size_bytes`, so you can gauge document sizes straight from the listing.

### `search_artifacts`

Substring search over title, description, slug.

| Param | Type | Required |
|---|---|---|
| `q` | string | yes |
| `scope` | string | no |
| `labels` | string[] | no |
| `limit` / `offset` | integer | no |
| `include_archived` | boolean | no |

**Returns:** same shape as `list_artifacts`.

### `list_artifact_versions`

List every non-deleted version of a slug.

| Param | Type | Required |
|---|---|---|
| `slug` | string | yes |

**Returns:** `{ "versions": ArtifactInfo[] }`.

### `archive_artifact`

Soft-delete by UUID (one version) or slug (all versions). Creator-or-admin; unauthorized
calls error.

| Param | Type | Required |
|---|---|---|
| `ident` | string | yes |

**Returns:** `{ "archived_count": int }`.

### `list_package_files`

List entries inside a PACKAGE artifact.

| Param | Type | Required |
|---|---|---|
| `ident` | string | yes |
| `version` | integer | no |

**Returns:** `{ "entries": [{ path, size, sha256, content_type }], "entry_point"? }`.

### `read_package_file`

Read one file from inside a PACKAGE artifact. Pass `max_bytes` to read only the first N
bytes of a large file.

| Param | Type | Required | Notes |
|---|---|---|---|
| `ident` | string | yes | UUID or slug |
| `path` | string | yes | package-relative path |
| `version` | integer | no | slug → latest readable |
| `max_bytes` | integer | no | cap the bytes returned; the reply flags `truncated` |

**Returns:** the content envelope (base64 for binary, as `read_artifact`), annotated with
`size_bytes` (the file's full size), `returned_bytes`, and `truncated`.

### `list_comments`

List comment threads (with their comments) on an artifact. Read-only; only returns
comments on artifacts the caller can already read. Available only when the comments
service is configured.

| Param | Type | Required | Notes |
|---|---|---|---|
| `ident` | string | yes | UUID or slug |
| `version` | integer | no | slug → latest readable |
| `exclude_resolved` | boolean | no | omit resolved threads |

**Returns:** `{ "threads": Thread[] }` (same Thread/Comment shape as the
[comments REST API](rest-api.md#comments-authed)).

## OAuth for MCP clients {#oauth}

A client that can't present an `arti_session` cookie or a pre-issued JWT authenticates
through arti's OAuth endpoints — RFC 7591 dynamic client registration plus an RFC 6749
authorization-code grant with PKCE. This is how Runlayer and similar gateways connect
without pre-shared credentials.

### Discovery

On an unauthenticated `/mcp` call, arti returns `401` with a `WWW-Authenticate` header
pointing at its protected-resource metadata (absolute URL, per RFC 9728). Two
discovery documents (`internal/auth/wellknown.go`):

- `GET /.well-known/oauth-protected-resource` — `resource`, `authorization_servers`,
  `bearer_methods_supported`, `resource_documentation`.
- `GET /.well-known/oauth-authorization-server` — `authorization_endpoint`,
  `token_endpoint`, `registration_endpoint`, `code_challenge_methods_supported: ["S256"]`,
  `token_endpoint_auth_methods_supported: ["none","client_secret_basic",
  "client_secret_post"]`.

### Flow

| Step | Endpoint | Notes |
|---|---|---|
| 1. Register | `POST /oauth/register` | Body `{ client_name?, redirect_uris, … }`. Redirect URIs must be `https://` or `http://localhost`. Returns `client_id` (+ `client_secret` unless auth method `none`). |
| 2. Authorize | `GET /oauth/authorize` | Requires `client_id`, `redirect_uri`, `response_type=code`, `code_challenge` (S256), `code_challenge_method=S256`, `state`. The caller's identity comes from the signed `arti_session` cookie, or from the upstream proxy headers where `ARTI_AUTH_MODE=proxy` declares a proxy overwrites them; an unidentified caller is redirected to `/auth/login` and returned here. Identity is checked against the email-domain allowlist, and against the required-groups list on the proxy path. Renders a consent page naming the client and the redirect URI; issues nothing by itself. |
| 2b. Approve | `POST /oauth/authorize/confirm` | The consent page's submit. Carries the page's signed `consent` blob and its `SameSite=Strict` identity cookie, so only the user who was shown the page can approve it. Redirects back with a single-use `code` (10-min TTL). |
| 3. Token | `POST /oauth/token` | `grant_type=authorization_code` with `code`, `redirect_uri`, `code_verifier`, client auth; or `grant_type=refresh_token`. Returns `{ access_token, refresh_token, token_type: "Bearer", expires_in, scope }`. |

Token lifetimes (`cmd/arti-server/cmd_serve.go:227`): access **7 days**, refresh
**90 days**. The client secret is stored only as a hash. Use the returned access token as
`Authorization: Bearer …` on `/mcp`.

## See also

- [REST API](rest-api.md) — the same operations over plain HTTP, plus the full route map.
- [APP SDK](app-sdk.md) — the apps proxy and the *other* MCP servers APPs can reach.
- [Configuration](configuration.md#auth) — auth, allowlist, and signing-key env vars.
