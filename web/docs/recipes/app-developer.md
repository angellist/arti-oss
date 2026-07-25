---
title: As an arti APP developer
order: 3
summary: Build an interactive APP artifact — a self-contained HTML page that calls MCP tools and an LLM through arti's governed proxy, with no backend and no API keys of its own.
---

# As an arti APP developer

An **APP** is a self-contained HTML page (plus assets) that does real work —
calls Notion, Slack, Linear, arti itself, or an LLM — without a backend, without
API keys, and without ever touching the user's session. arti serves it
sandboxed and injects a bridge: your page calls `window.arti.callTool(server,
tool, args)`, and arti's governed proxy authorizes every call against a
manifest allowlist and runs it *as the signed-in viewer*.

If you've built a Claude.ai or Cowork artifact, the mental model is the same —
an HTML page that calls tools and an LLM — but the credentials and governance
live in arti, not in the page. For the security model behind all of this, read
[App serving](../architecture/app-serving.md); this page is how you build one.

## The model

```
your HTML  ──window.arti.callTool──▶  /api/apps/mcp  ──▶  MCP server (as the viewer)
   │                                       │
   └── runs in an opaque-origin sandbox    └── checks: viewer access · app scope · arti-app.json allowlist
       (no cookie, no API access)
```

Three facts shape everything you write:

1. **The page is credential-less.** arti serves it with a `sandbox` CSP and
   *no* `allow-same-origin`, so it runs in a unique opaque origin. It can't read
   the `arti_session` cookie or call arti's normal APIs. Its only capability is a
   per-viewer, app-scoped token (`app:<artifactID>`, ~12h) that arti injects and
   that works *only* against the apps proxy for *this one app*.
2. **You declare what you can call.** A bundled `arti-app.json` lists the exact
   `(server, tool)` pairs the app may invoke. Anything not listed is `403` at the
   proxy. Server URLs live server-side — you name a server, you never supply a URL.
3. **Calls run as the viewer.** OAuth-backed servers use on-behalf-of (OBO): the
   first call pops a one-time consent popup, then the tool runs with the viewer's
   own upstream permissions and audit trail.

## Project layout

An APP is a `PACKAGE`-shaped zip with one required extra file at the root:

```
my-app/
├── arti-app.json     # required: the tool allowlist + entry + param contract
├── index.html        # the entry page
└── assets/…          # optional CSS/JS/images
```

Relative asset references resolve correctly: arti injects a `<base href>`
pointing at the package-files API, so `<script src="assets/app.js">` works.

## arti-app.json

One JSON file declares the app's name, entry page, the tools it may call, and any
URL parameters it accepts. The parser is in `internal/apps/apps.go` (`name`,
`entry`, `tools`) and `internal/artifacts/appparams.go` (`params`).

```json
{
  "name": "Standup Digest",
  "entry": "index.html",
  "tools": [
    { "server": "slack",  "tool": "slack_search_public" },
    { "server": "linear", "tool": "list_issues" },
    { "server": "llm",    "tool": "complete" }
  ],
  "params": [
    { "name": "channel", "type": "string", "default": "eng" },
    { "name": "days",    "type": "number", "default": 1 },
    { "name": "mode",    "type": "enum", "values": ["brief", "full"], "default": "brief" }
  ]
}
```

| Field | Meaning |
|---|---|
| `name` | Display name (optional). |
| `entry` | Launch HTML file, relative to the zip root. This is authoritative for the launch page. |
| `tools` | The allowlist. Each entry is `{ "server": "...", "tool": "..." }`. **Only listed pairs are callable**; everything else is `403`. |
| `params` | Optional URL-parameter contract (see [URL params](#url-params)). |

### params spec

Each entry in `params` (`appParamSpec`):

| Key | Notes |
|---|---|
| `name` | Param name. Names starting with `_` are reserved and skipped. |
| `type` | `string` (default), `number`, `boolean`, `enum`. |
| `default` | Value used when the query omits the param (or the value is malformed). |
| `values` | Allowed values when `type` is `enum`. |
| `required` | A missing required value is a non-fatal note in `_errors`, never a page error. |

Limits: at most 32 params; a single value over 8 KiB is truncated. Coercion is
lenient — a missing or malformed value falls back to the default and the page
always renders.

## The injected bridge: window.arti

arti injects `window.__ARTI_APP__` (config + token) and a small shim that
defines `window.arti` (`appBridgeJS` in `internal/artifacts/server.go`). You
write against `window.arti` — never construct the token or endpoint yourself.

### window.arti.callTool(server, tool, args)

POSTs to `/api/apps/mcp` with the injected bearer token and returns a Promise of
the MCP tool result (the raw result body). On a `401 authorization_required` it
transparently opens the OBO consent popup and retries once.

```js
const result = await window.arti.callTool("linear", "list_issues", {
  teamId: "ENG",
  first: 20,
});
// result is the tool's MCP result JSON; shape depends on the tool.
```

On failure it throws an `Error` whose message is the proxy's `detail`. Wrap calls
in `try/catch`.

### window.arti.params

A typed object of the URL params you declared in `arti-app.json`, coerced
server-side. `{}` when you declare none. A `_errors` array records any coercion
problems.

```js
const { channel, days, mode, _errors } = window.arti.params;
// e.g. /app/standup-digest?channel=design&days=7&mode=full
// → { channel: "design", days: 7, mode: "full", _errors: [] }
```

Undeclared query keys aren't in `params`, but you can still read them via
`new URLSearchParams(location.search)`.

### Reading data from other artifacts (template + data)

An APP can keep its **template + code** in one artifact and load its **data**
from *separate* artifacts at runtime — pointed to by a URL param. You never
re-assemble template + data into one artifact: ship new data (or change the
param), and the app stays put.

Allowlist arti's read tools and read another artifact **as the signed-in
viewer**. These run in-process (same access as the viewer, **no consent
popup**), so they work even in local dev where no OBO upstream is wired:

```json
"tools": [
  { "server": "arti", "tool": "read_artifact" },
  { "server": "arti", "tool": "search_artifacts" }
]
```

```js
// ?data=<slug-or-uuid>  →  load that artifact's JSON, as the viewer
const data = await window.arti.loadJSON(window.arti.params.data);

// or get the raw payload + metadata
const { text, contentType, sizeBytes, truncated } =
  await window.arti.loadArtifact(window.arti.params.data, { max_bytes: 65536 });

// discover sibling data artifacts (newest first) for a picker
const { artifacts } = await window.arti.search({
  labels: ["dataset:demo"], order_by: "created", order_dir: "desc", limit: 50,
});
```

`order_by` is one of `created | title | type | slug | version | creator | scope`
(default `created`) and `order_dir` is `asc | desc` (default `desc`) — so
`order_by:"created"` gets the latest. These sugar helpers wrap
`callTool("arti", …)`; only arti's **read** tools short-circuit in-process —
writes and other servers still go through the OBO proxy. Access is the viewer's
own: the app reads exactly what the viewer can already read in arti, nothing
more.

### Calling the built-in LLM

The `llm` server's one tool, `complete`, is an in-process Claude call (arti's
service key, budgeted per viewer/app/org — no OBO, no popup). Allowlist
`{ "server": "llm", "tool": "complete" }`, then:

```js
const r = await window.arti.callTool("llm", "complete", {
  prompt: "Summarize these standup notes in 3 bullets:\n" + notes,
  model: "haiku",        // alias or canonical id; omit for the default
  max_tokens: 512,
});
const text = r.content?.[0]?.text ?? "";
```

`complete` arguments (`CompleteInput`, `internal/llm/llm.go`):

| Arg | Notes |
|---|---|
| `messages` | `[{role, content}]` conversation; **or** use `prompt`. |
| `prompt` | Shorthand for a single user message. |
| `system` | System prompt (optional). |
| `model` | Alias (`opus`/`sonnet`/`haiku`) or canonical id (`claude-opus-4-8`, `claude-sonnet-4-6`, `claude-haiku-4-5`). Empty → the server default. |
| `max_tokens` | Output cap. |
| `response_format` | `"json"` to force raw JSON output. |

The result shape mirrors the Anthropic Messages API: `{ "content": [{ "type":
"text", "text": "…" }] }`. On limits it returns an error object with
`error` (`rate_limited`, `budget_exceeded`, …) and an optional `retry_after`.

For drop-in compatibility, the bridge also defines `window.arti.askClaude(prompt,
opts)` and host shims `window.claude.complete(prompt, opts)` and
`window.cowork.askClaude(prompt, history)` — these route to `llm.complete` and
return `""`/`{text:""}` if the app didn't allowlist it. Prefer `callTool`
directly in new apps.

## Available servers

Two servers are built into the `appServers` map
(`cmd/arti-server/cmd_serve.go`); the deployment adds the rest via
`ARTI_APP_MCP_SERVERS`:

| Server | Auth | What it is |
|---|---|---|
| `llm` | service | Built-in Claude completion (`complete`). |
| `arti-self` | none | Local credential-free arti MCP; resolves only under `ARTI_AUTH_DISABLED` (local dev). |
| `arti` | oauth | arti's own MCP — let an app persist its own state (e.g. `add_artifact`/`read_artifact` under a fixed slug). Operator-configured; reads run in-process as the viewer. |
| *your servers* | none / oauth | Whatever upstream MCP servers the deployment configures (Notion, Slack, Linear, an internal gateway, …). |

`oauth` servers route through the OBO broker: the call runs as the viewer,
and the first one triggers a one-time consent popup. The exact tool names per
server come from that server's MCP — see [MCP tools
reference](../reference/mcp-tools.md) for arti's own.

## A minimal complete APP

`index.html`:

```html
<!doctype html>
<meta charset="utf-8">
<title>Standup Digest</title>
<main>
  <h1>Standup Digest</h1>
  <button id="go">Generate</button>
  <pre id="out">Pick a channel via ?channel=… then click Generate.</pre>
</main>
<script>
  const out = document.getElementById("out");
  document.getElementById("go").onclick = async () => {
    out.textContent = "Working…";
    try {
      const { channel, days } = window.arti.params;
      const msgs = await window.arti.callTool("slack", "slack_search_public", {
        query: `in:#${channel} after:${days}d`,
      });
      const notes = JSON.stringify(msgs);
      const r = await window.arti.callTool("llm", "complete", {
        prompt: "Summarize this team's standup chatter in 3 bullets:\n" + notes,
        model: "haiku",
        max_tokens: 400,
      });
      out.textContent = r.content?.[0]?.text ?? "(no output)";
    } catch (e) {
      out.textContent = "Error: " + e.message;
    }
  };
</script>
```

`arti-app.json`:

```json
{
  "name": "Standup Digest",
  "entry": "index.html",
  "tools": [
    { "server": "slack", "tool": "slack_search_public" },
    { "server": "llm",   "tool": "complete" }
  ],
  "params": [
    { "name": "channel", "type": "string", "default": "eng" },
    { "name": "days",    "type": "number", "default": 1 }
  ]
}
```

## Upload it

Zip the directory (with `arti-app.json` at the root) and upload as type `app`.
With the CLI, point it at the directory:

```sh
arti add ./my-app --type app --slug standup-digest --title "Standup Digest" --label app
```

Or via the REST API, send the zip bytes with `"artifact_type": "APP"` (see
[As an API consumer](api-consumer.md#package-app)). arti requires
`arti-app.json` at the zip root for an APP and stamps the manifest `entry` as the
launch page.

View it full-screen at `/app/<slug>` (or `/app/<uuid>`, or `/app/<slug>/<version>`).

## Embedding in another page

By default an APP can be framed only by `'self'` (the
`ARTI_APP_FRAME_ANCESTORS` env var sets the CSP `frame-ancestors` allowlist;
`X-Frame-Options` is dropped for APPs so `frame-ancestors` is authoritative).
To embed an APP in another trusted origin, an operator adds that origin to
`ARTI_APP_FRAME_ANCESTORS`. Pass the
APP's declared params as query string on the iframe `src`:

```html
<iframe src="https://arti.example.com/app/standup-digest?channel=design&days=7"></iframe>
```

## Gotchas

- **Allowlist every tool.** A `(server, tool)` not in `arti-app.json` is `403`.
  Add `llm.complete` explicitly if you use the LLM or any host shim.
- **Don't expect a cookie or same-origin.** The page is opaque-origin by design.
  Persist state through the `arti` server (`add_artifact`) or your params, not
  `document.cookie` or arti's normal APIs.
- **Handle the consent popup path.** First call to an `oauth` server pops a
  window; `callTool` handles the retry, but your UI should tolerate the delay.
- **The injected token is per-viewer and short-lived (~12h).** It's not part of
  your uploaded bytes; reloading mints a fresh one. Never read, copy, or persist
  `window.__ARTI_APP__.token`.

## Related

- [App SDK reference](../reference/app-sdk.md) — full `window.arti` / `arti-app.json` surface.
- [App serving](../architecture/app-serving.md) — the sandbox, scoped token, proxy, and OBO model.
- [MCP tools reference](../reference/mcp-tools.md) — arti's own MCP tools.
- [As an API consumer](api-consumer.md) — uploading the package via REST.
