---
title: arti APP SDK
order: 4
summary: The complete browser surface arti injects into APP artifacts — window.arti, the arti-app.json manifest, the built-in MCP servers, and llm.complete.
---

# arti APP SDK

An **APP** is a PACKAGE artifact that arti serves as a sandboxed single-page app and
augments at request time. The page ships no API keys and talks to no backend of its
own: arti injects a tiny bridge (`window.arti`) plus a per-viewer, app-scoped token,
and the page reaches governed MCP tools — and a built-in Claude completion — through
arti's proxy. Everything the proxy forwards is gated by the app's `arti-app.json`
allowlist and attributed to the signed-in viewer.

This page is the SDK reference: the injected globals, the manifest schema, the
built-in servers, and `llm.complete`. For the proxy endpoint itself see the
[REST API](rest-api.md#apps-proxy); for the architecture see
[app serving](../architecture/app-serving.md).

## What gets injected

When arti serves an APP's HTML entry page it rewrites the body before sending it
(`internal/artifacts/server.go:1787`, `injectAppBridge`):

1. A `<base href>` pointing relative asset references at the package-files API, unless
   the page already declares its own `<base>` (`injectBaseHref`, `internal/artifacts/server.go:1739`).
2. `window.__ARTI_APP__` — the per-request config blob (below).
3. The bridge script that defines `window.arti` and the host-compat shims.

Injection only happens for `text/html` responses served to an authenticated viewer.
Non-HTML package files are served as-is. The injected token is **per request, per
viewer**, app-scoped, and lives ~12h (`appTokenTTL`, `internal/apps/apps.go:41`) — it
is not part of the uploaded artifact and carries no upstream secrets.

## `window.__ARTI_APP__`

The raw config object the bridge reads. You normally use `window.arti` instead, but
these fields are available directly.

| Field | Type | Meaning |
|---|---|---|
| `appId` | string (UUID) | This artifact's id. Sent as `app_id` on every proxy call. |
| `token` | string (JWT) | The injected, app-scoped Bearer. Authenticates proxy calls. Reloading mints a fresh one. |
| `endpoint` | string | The proxy path — always `/api/apps/mcp`. |
| `params` | object | Declared URL params, coerced per the manifest (see [params](#url-params)). `{}` when none declared. Also exposed as `window.arti.params`. |

Source: `internal/artifacts/server.go:1795`.

## `window.arti`

### `callTool(server, tool, args, opts?)`

The one call that matters. POSTs a single `tools/call` to the proxy, which validates
the app token, enforces the allowlist, resolves the named server, attaches the
viewer's upstream credential when needed, and forwards the call.

```ts
window.arti.callTool(server: string, tool: string, args?: object, opts?: { timeout_ms?: number }): Promise<any>
```

- `server` — a built-in server name (see [Built-in servers](#built-in-servers)) or one
  added via `ARTI_APP_MCP_SERVERS`.
- `tool` — the upstream tool name.
- `args` — the tool's arguments object (defaults to `{}`).
- `opts.timeout_ms` — how long the proxy waits on the upstream for this call.
  Default 60s. The server clamps it to `ARTI_APP_CALL_TIMEOUT_MAX` (90s by
  default, sized to stay under the edge's own read timeout); the 503 body names
  the timeout that applied. Upstream failures are always 503: Cloudflare replaces an
  origin 502 or 504 with its own page, which the sandboxed app cannot read. Calls that need longer than the cap should use a
  tool's own start/poll shape (e.g. a Snowflake statement handle).

**Returns** a Promise resolving to the upstream MCP result body (the parsed JSON the
server returned for `tools/call`). For MCP tools this is typically
`{ content: [{ type: "text", text: "…" }], … }`.

**Rejects** with an `Error` whose message is the proxy's `detail` (or
`arti tool call failed: <status>`) on any 4xx/5xx that isn't the OAuth handshake.
The error also carries the proxy's structured fields: `err.code`, `err.status`,
`err.server`, `err.tool`, and `err.retryAfter` when the body had one. Branch on
`err.code`, never on the message:

| `err.code` | Status | Meaning and recovery |
|---|---|---|
| `upstream_timeout` | 503 | The upstream did not answer within the call's timeout. Work it started may still be running; resume it (e.g. by handle) rather than re-running it, or pass a larger `timeout_ms`. In-process `arti/*` calls report the same code. |
| `upstream_response_too_large` | 503 | One response exceeded the 16 MiB read cap. Ask the tool for smaller pages. |
| `tool_error` | 503 | The server answered with a JSON-RPC error object (protocol-level: unknown method, malformed request). Retrying cannot help; `detail` carries the upstream code and message. Note that most MCP servers, Runlayer included, report a failed tool *execution* (unknown tool, bad arguments, SQL error) as a normal result with `isError: true`; that resolves, so check `result.isError` on success. |
| `upstream_error` | 503 | Any other upstream failure (HTTP 5xx, malformed body, connection error). |
| `proxy_busy` | 503 | This arti pod is already relaying its maximum number of upstream calls. Retry after `err.retryAfter` seconds. |
| `network_error` | 0 | Set client-side when `fetch` itself rejected: no response reached the page. From the sandboxed page this is what an edge error page without CORS headers looks like, as well as a real network failure. |
| `not_allowlisted` | 403 | `server/tool` is missing from `arti-app.json`. |
| `unknown_server` | 400 | The server is not configured on this arti. |
| `edge_timeout` | 502/504/524 | Set client-side when nginx or Cloudflare answered with a non-JSON error page in arti's place. Treat like `upstream_timeout`. |
| `bad-request`, `forbidden`, `slug-exists`, … | as REST | In-process `arti/*` tools answer with the same codes as `POST /api/artifacts`. |
| `budget_exceeded`, `rate_limited`, … | see below | `llm/complete` has its own codes; see [Errors](#errors) under `llm.complete`. |

**Embedded per-user (no code change).** When the same APP is served into a
`user`-mode [embed surface](../architecture/front-embed.md#per-user-mode-identity-user)
rather than opened at `/app`, the page ships with **no token** — the injected
bridge instead shows a "Connect as you" affordance and, on first
`window.arti.callTool`, runs the consent-popup + poll handshake transparently
before resolving the call. Your app code is identical either way; it just calls
`window.arti.callTool(...)`. One guideline: trigger the first tool call from a
**user gesture** (e.g. a click handler), since opening the consent popup needs a
user activation — otherwise the browser blocks it and the bridge falls back to
the visible "Connect as you" button.

**OAuth handshake** is transparent: if the proxy returns `401 authorization_required`
with an `authorize_url`, the bridge opens that URL in a popup, waits for the consent to
post back `arti_oauth: "done"`, then retries the call once
(`internal/artifacts/server.go:1696`). The `(server, tool)` pair must be in the
manifest's `tools` allowlist or the proxy returns 403.

```js
const res = await window.arti.callTool("notion", "search", { query: "roadmap" });
const text = res.content?.[0]?.text ?? "";
```

### Reading other artifacts {#reading-artifacts}

Sugar over `callTool("arti", …)` for the common case of an APP reading *other* arti
artifacts as the signed-in viewer. All of arti's own tools run **in-process**
(`internal/apps/apps.go`), reads and writes alike: same access as the viewer, **no
consent popup**, and they work in local dev where no OBO upstream is wired. You still
allowlist the underlying tools (`arti/read_artifact`, `arti/search_artifacts`, …) in the
manifest.

| Method | Returns | Wraps |
|---|---|---|
| `loadArtifact(ident, opts?)` | `{ text, contentType, sizeBytes, sha256, truncated }` | `callTool("arti","read_artifact",{ident, ...opts})`; `opts` may set `version`, `max_bytes` |
| `loadJSON(ident, opts?)` | parsed JSON | `loadArtifact` then `JSON.parse(text)` |
| `search(args)` | `{ artifacts, total }` | `callTool("arti","search_artifacts", args)`; `args`: `q`, `labels`, `scope`, `limit`, `offset`, `order_by`, `order_dir` |

`ident` is a slug or UUID — typically threaded in via a URL param
(`window.arti.params.data`) so one APP template renders many data artifacts without a
rebuild. `order_by` is `created|title|type|slug|version|creator|scope` (default
`created`), `order_dir` is `asc|desc` (default `desc`). Every other server still goes
through the OBO proxy. This is the "one app, swappable data" pattern; see the
[app developer recipe](../recipes/app-developer.md#reading-data-from-other-artifacts-template-data).

```js
const data = await window.arti.loadJSON(window.arti.params.data);   // load JSON as the viewer
const { artifacts } = await window.arti.search({ labels: ["dataset:demo"], limit: 50 });
```

### `window.arti.params` {#url-params}

The typed, coerced URL parameters the app declared in its manifest's `params` array,
resolved against the request's query string by `resolveAppParams`
(`internal/artifacts/appparams.go:45`). `{}` when nothing is declared.

Resolution is **lenient and never fails** (an embedded iframe must always render):

- A missing value falls back to the declared `default`, else the type's zero value
  (`""`, `0`, `false`).
- A malformed value (e.g. `count=NaN`, a bad boolean, an out-of-set enum) falls back
  the same way and is recorded.
- Coercion problems land in `window.arti.params._errors` — an array of
  `{ name, reason }`. A missing **required** param is a non-fatal note here, not a page
  error.
- Undeclared query keys are ignored here but remain readable via
  `new URLSearchParams(location.search)`.
- Caps: at most 32 declared params; each resolved value truncated to 8192 bytes.

### Host-compat shims

So unmodified Claude.ai / Cowork artifacts work without edits, the bridge also defines
(only if absent) thin wrappers over `llm.complete` (`internal/artifacts/server.go:1716`):

| Global | Shape | Notes |
|---|---|---|
| `window.arti.askClaude(prompt, opts)` | `Promise<string>` | Returns `""` on any failure. |
| `window.claude.complete(prompt, opts)` | `Promise<string>` | Same. |
| `window.cowork.askClaude(prompt, history)` | `Promise<{text}>` | `history` is prepended as prior messages. |

All three degrade gracefully (return empty) if the app's manifest doesn't allowlist
`llm.complete` — the proxy 403s and the shim swallows it. To use Claude with real error
handling, call `window.arti.callTool("llm", "complete", …)` directly.

## `arti-app.json` manifest

A JSON file at the **root of the package** (`ManifestPath = "arti-app.json"`,
`internal/apps/apps.go:43`). It declares the launch page, the URL-param contract, and —
critically — the exact `(server, tool)` allowlist. The proxy reads it fresh on every
call and rejects anything not listed (`internal/apps/apps.go:203`).

| Field | Type | Required | Meaning |
|---|---|---|---|
| `name` | string | no | Display name of the app. |
| `entry` | string | no | Authoritative launch page within the package. Falls back to the package `entry_point`, then `index.html` (`internal/artifacts/server.go:1554`). |
| `tools` | array | yes (for tool access) | The allowlist. Each item is `{ "server": string, "tool": string }`. A call is permitted only if its exact `(server, tool)` pair appears here. |
| `params` | array | no | URL-param contract; each item is an [`appParamSpec`](#param-spec). |

### `params[]` entry — `appParamSpec` {#param-spec}

Source: `internal/artifacts/appparams.go:14`.

| Field | Type | Meaning |
|---|---|---|
| `name` | string | Query-param name. Names starting with `_` are reserved (the `_errors` channel) and skipped. |
| `type` | string | One of `string`, `number`, `boolean`, `enum`. Empty ⇒ `string`. |
| `default` | any | Value used when the query omits this param (or coercion fails). |
| `values` | string[] | Allowed values when `type` is `enum`. |
| `required` | boolean | If true and missing, recorded in `_errors` (non-fatal). |

### Complete example

```json
{
  "name": "Roadmap Browser",
  "entry": "index.html",
  "tools": [
    { "server": "notion", "tool": "search" },
    { "server": "notion", "tool": "notion-fetch" },
    { "server": "linear", "tool": "list_issues" },
    { "server": "llm", "tool": "complete" },
    { "server": "arti", "tool": "add_artifact" },
    { "server": "arti", "tool": "read_artifact" }
  ],
  "params": [
    { "name": "team", "type": "string", "default": "platform" },
    { "name": "limit", "type": "number", "default": 25 },
    { "name": "compact", "type": "boolean", "default": false },
    { "name": "view", "type": "enum", "values": ["list", "board"], "default": "list" }
  ]
}
```

## Built-in servers {#built-in-servers}

The named MCP servers an APP may reach. App authors reference a server **by name
only**; its URL and auth policy live server-side (`appServers` map,
`cmd/arti-server/cmd_serve.go`), so an author can neither point at an arbitrary
endpoint nor embed a secret. Two servers are built in; operators add the rest
with `ARTI_APP_MCP_SERVERS` (see [Configuration](configuration.md#apps-mcp)).

| Server name | Auth mode | Upstream | Notes |
|---|---|---|---|
| `arti-self` | `none` | arti's own `/mcp` (loopback) | Built-in. Credential-free local verification; reachable even when auth is disabled. |
| `llm` | `service` | In-process Anthropic Messages API | Built-in. The single-Claude completion; see below. No OBO. |
| `arti` | in-process\* | arti's own MCP | The name is special: every arti tool, read and write, runs in-process as the viewer (no consent popup, works in local dev, needs no server entry). A write is stamped `written_via: app:<uuid>` with the viewer as `creator`. See [Reading other artifacts](#reading-artifacts). |
| anything else | `none` / `oauth` | operator's choice | Deployment configuration — e.g. entries for Notion, Slack, Linear, or an MCP gateway the organization runs. |

\* A deployment may still list `arti` in `ARTI_APP_MCP_SERVERS`, but the entry is unused:
the in-process branch sits ahead of the server lookup, so arti access does not depend on
it. See [Reading other artifacts](#reading-artifacts).

**Auth modes** (`ServerConfig.Auth`, `internal/apps/apps.go:49`):

- `none` — forwarded with no upstream credential. A 401 from such a server is a genuine
  rejection, surfaced as `503` `upstream_error` (no consent loop).
- `oauth` — the proxy attaches the viewer's per-user upstream Bearer via the OBO broker.
  If the viewer hasn't consented, the proxy returns `401 authorization_required` with an
  `authorize_url` and the bridge runs the popup handshake. Requires the OBO broker to be
  wired (else `501`).
- `service` — handled in-process with arti's own service key; only the `llm` server uses
  it. Requires `ANTHROPIC_API_KEY` (else `501`).

## `llm.complete`

The `llm` server exposes exactly one tool, `complete`: a single, tool-free,
non-streaming Claude call made in-process with arti's service key
(`internal/llm/llm.go`). Usage is metered against the Postgres budget ledger and
attributed to the viewer + app — but the viewer's identity is used only for budgeting,
not auth.

### Arguments (`CompleteInput`, `internal/llm/llm.go:58`)

| Field | Type | Default | Meaning |
|---|---|---|---|
| `prompt` | string | — | Convenience: a single user message. Provide this **or** `messages`. |
| `messages` | `{role, content}[]` | — | Multi-turn history. `role` is `user`/`assistant` (anything not `assistant` is treated as user). Still a single-shot, tool-free call. |
| `system` | string | — | System prompt. |
| `model` | string | server default | Alias or canonical id; must be in the allowlist. Aliases: `opus`→`claude-opus-4-8`, `sonnet`→`claude-sonnet-4-6`, `haiku`→`claude-haiku-4-5`. |
| `max_tokens` | number | 2048 | Output cap. Hard ceiling 10000; higher values are clamped. |
| `response_format` | string | — | `"json"` appends a system instruction to emit raw JSON (no fences). Parse defensively. |

Providing neither `prompt` nor non-empty `messages` returns a `400 bad_input`.

### Result

On success, the MCP result body:

```json
{ "content": [{ "type": "text", "text": "…model output…" }] }
```

### Errors

On failure the proxy returns the structured error's HTTP status with a body of
`{ "error": <code>, "detail": <msg>, "retry_after": <seconds> }`
(`internal/llm/llm.go:248`).

| Status | `error` code | When |
|---|---|---|
| 400 | `bad_input` | No prompt/messages, or model not in allowlist. |
| 429 | `rate_limited` | Anthropic 429 (`retry_after` ≈ 30s). |
| 429 | `budget_exceeded` | Viewer/app/org budget cap hit (`retry_after` set). |
| 503 | `budget_unavailable` | Budget ledger unreadable — the gate fails **closed**. |
| 503 | `overloaded` | Anthropic 529. |
| 503 | `upstream` | Any other Anthropic error. |
| 501 | (proxy) | `llm` server not configured (`ANTHROPIC_API_KEY` unset). |

The default model, allowlist, and budget caps are operator-configured — see
[Configuration → LLM](configuration.md#llm-built-in-completion).

```js
const r = await window.arti.callTool("llm", "complete", {
  prompt: "Summarize this page in one line.",
  model: "haiku",
  max_tokens: 200,
});
console.log(r.content[0].text);
```

## Security model in brief

- The served APP page runs in an **opaque-origin sandbox** (no `allow-same-origin`), so
  it cannot read `arti_session` or reach arti's other APIs directly
  (`setContentSecurityApp`, `internal/artifacts/server.go:1820`). Its only privileged
  channel is `window.arti.callTool` with the injected token.
- The proxy re-checks the email-domain allowlist on every call, so a revoked user can't
  keep driving tool calls until the token expires (`internal/apps/apps.go:159`).
- The injected token is app-scoped: it's rejected by the main auth middleware, so a
  leaked app token can't be replayed as a session credential on the REST/MCP routes.
- Framing is governed by `frame-ancestors` (`ARTI_APP_FRAME_ANCESTORS`); see
  [Configuration](configuration.md#apps-mcp).
