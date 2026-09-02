---
title: Using the MCP server
order: 4
summary: Connect an MCP client to arti's /mcp endpoint, authenticate via OAuth, and let an agent read and write artifacts.
---

# Using the MCP server

arti speaks the [Model Context Protocol](https://modelcontextprotocol.io) so an
agent — Claude, or anything behind an MCP gateway like Runlayer — can read and
write artifacts as tool calls instead of shelling out to the [CLI](command-line.md).

## The endpoint

The MCP server is mounted at **`/mcp`** (`cmd/arti-server/cmd_serve.go:501`):

```text
https://arti.example.com/mcp
```

It's JSON-RPC 2.0 over HTTP POST — one request, one response, no SSE stream. It
sits behind the same auth middleware as the REST API, so every call needs a
bearer token in the `Authorization` header. A quick smoke test with a token in
hand:

```sh
curl -sX POST https://arti.example.com/mcp \
  -H "Authorization: Bearer $ARTI_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

And a worked `tools/call` roundtrip — the argument names that trip up first
attempts: `content_type` is a **MIME type** (required), the TEXT/PACKAGE/APP
kind is `artifact_type`, and reads take `ident` (a UUID *or* slug — a wrong
argument name reads as "not found", not a validation error). Full per-tool
schemas: [MCP tools](../reference/mcp-tools.md).

```jsonc
// add_artifact
{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
  "name":"add_artifact",
  "arguments":{
    "title":"Hello",
    "named_slug":"hello",
    "content":"# hi",
    "content_type":"text/markdown",
    "artifact_type":"TEXT"
  }}}

// read_artifact  (ident = UUID or slug)
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{
  "name":"read_artifact",
  "arguments":{"ident":"hello"}}}
```

## Authentication

> **Prefer your MCP gateway, if you run one.** Organizations that route agent
> tooling through an MCP gateway (Runlayer or similar) should reach arti's MCP
> through it rather than wiring up `/mcp` directly — the gateway brokers the
> OAuth login (below) and centralizes governance. Connect to `/mcp` directly
> when there is no gateway — e.g. a local dev client or a one-off script.

How you get that bearer token depends on who's connecting.

### Via an MCP gateway (OAuth)

arti is itself an **OAuth 2.0 authorization server** for MCP clients, so a
gateway can register and broker user logins without any pre-shared secret. The
relevant endpoints (registered in `cmd/arti-server/cmd_serve.go`):

| Endpoint | Purpose |
|---|---|
| `GET /.well-known/oauth-authorization-server` | Authorization-server metadata (RFC 8414). |
| `GET /.well-known/oauth-protected-resource[/mcp]` | Protected-resource metadata (RFC 9728). Because the resource is at a path (`/mcp`), a client builds the URL per RFC 9728 §3.1 as `…/oauth-protected-resource/mcp` — that's what the `/mcp` 401 `WWW-Authenticate` advertises; the bare-origin path is also served. |
| `POST /oauth/register` | Dynamic client registration (RFC 7591). |
| `GET /oauth/authorize` | Authorization endpoint; PKCE (`S256`) required. Renders a consent page. Identifies the user from the `arti_session` cookie, or from the proxy headers under `ARTI_AUTH_MODE=proxy`; with no session it redirects to `/auth/login` and returns here. |
| `POST /oauth/authorize/confirm` | The consent page's own submit — the only path that issues a code. |
| `POST /oauth/token` | Authorization-code and refresh-token grants. |

The flow: the gateway discovers the metadata, dynamically registers a client,
sends the user through `/oauth/authorize` (arti identifies the user and asks them
to approve the request; the code is issued when they do), then exchanges the code
at `/oauth/token` for an access
token plus a refresh token. **A human has to approve** — the authorization step
is a page with a button, not a redirect, so an unattended client cannot complete
it. The access token
is a signed JWT carrying the user's email; the gateway puts it in
`Authorization: Bearer …` on every `/mcp` call. Point a gateway at the `/mcp`
URL and let it handle discovery — you don't drive these endpoints by hand.

> The `authenticate` / `complete_authentication` tools you may see exposed by a
> gateway are the gateway's own in-band login helpers, not tools arti's MCP
> server defines. arti's tool set is the artifact tools listed below.

### Headless / direct (bearer token)

When you control the client directly — a local agent, a CI job — skip the OAuth
dance and supply a token via the `ARTI_TOKEN` bearer, the same one the
[CLI uses for headless auth](command-line.md#headless-arti_token). These come
from arti's device-authorization flow (RFC 8628); see
[Authentication](../architecture/auth.md) for issuing and refreshing them.

## What the tools do

arti's MCP server exposes 12 tools (`internal/mcp/server.go`). The exhaustive
schemas live in the [MCP tools reference](../reference/mcp-tools.md); the shape:

| Tool | Purpose |
|---|---|
| `add_artifact` | Create or version an artifact (TEXT by default). |
| `append_artifact` | Atomically append to a slug; supports an idempotency key. |
| `update_artifact` | Edit metadata (title/description/scopes/labels/access/comments) in place; no new version. |
| `get_artifact` | Fetch metadata by UUID or slug (incl. `size_bytes` — a cheap size check). |
| `read_artifact` | Fetch the content (base64 for binary); `max_bytes` peeks a prefix. |
| `list_artifacts` | List/filter the catalog (type, creator, scope, labels). |
| `search_artifacts` | Substring search over title, description, slug. |
| `list_artifact_versions` | List every non-deleted version of a slug. |
| `archive_artifact` | Soft-delete a version (UUID) or whole slug. |
| `list_package_files` | List entries inside a PACKAGE. |
| `read_package_file` | Read one file out of a PACKAGE. |
| `list_comments` | Read comment threads on an artifact. |

These mirror the [CLI](command-line.md) and [REST API](../reference/rest-api.md),
and respect the same access rules — a caller only reads what their token is
allowed to read, and writes are attributed to the token's email.

## How an agent uses it

A typical agent loop: `search_artifacts` or `list_artifacts` to find prior work,
`read_artifact` (or `read_package_file`) to pull it into context, then
`add_artifact` / `append_artifact` to persist a result under a stable slug so the
next run can find it. The [agent recipe](../recipes/agent.md) works a full
example end to end.

List/search results and `get_artifact` all carry `size_bytes`, so an agent can
check how large a document is before reading it — and pass `max_bytes` to
`read_artifact` to peek at a prefix rather than pulling a large file into
context wholesale.

## See also

- [MCP tools reference](../reference/mcp-tools.md) — every tool, with input schemas.
- [Agent recipe](../recipes/agent.md) — a worked agent workflow over MCP.
- [App serving](../architecture/app-serving.md) — how APP artifacts call MCP tools through arti's proxy.
