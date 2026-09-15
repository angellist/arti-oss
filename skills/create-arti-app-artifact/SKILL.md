---
name: create-arti-app-artifact
description: >-
  Use when the user wants to BUILD an arti APP artifact — a self-contained HTML page,
  served in a sandboxed iframe, that calls MCP tools and/or an LLM (`llm.complete`)
  through arti's governed proxy, and can load its data from SEPARATE artifacts at
  view-time (template + data). Trigger on "create/build an arti app", "make an HTML
  app that calls MCP/an LLM on arti", "turn this artifact into an arti app". Build it
  in your workspace and upload via arti-usage-guide (hosted) or arti-cli-usage-guide
  (local).
---

# create-arti-app-artifact

An **arti APP** is a zip (`index.html` + an `arti-app.json` manifest). arti serves its
entry HTML in a **sandboxed iframe** and injects a small bridge so the page can call
**only the MCP tools the manifest allowlists** — routed through the arti proxy, which
attaches credentials server-side. The page never sees an API key or OAuth token. Two
call targets:

- **`llm` server, `complete` tool** — a built-in single-completion call (arti's service key, no agent loop), backed by the model the operator configures. `max_tokens` has a default and a hard ceiling, both set by the operator.
- **The `arti` server** — arti's own tools, reads and writes, dispatched in-process as the signed-in viewer. No consent window, and no server entry required.
- **Any other MCP server the operator configured** (e.g. `notion`, `slack`, `github`, …) — per-user OAuth (on-behalf-of); the **first** call to one pops a consent window (needs a user gesture).

## The two pieces
```
index.html      # the page — self-contained: inline CSS/JS
arti-app.json   # manifest: name + entry + tool allowlist (+ optional params)
```

### `arti-app.json`
```json
{ "name": "My App", "entry": "index.html",
  "tools": [ { "server": "llm",  "tool": "complete" },
             { "server": "arti", "tool": "read_artifact" },
             { "server": "arti", "tool": "search_artifacts" } ],
  "params": [ { "name": "data", "type": "string", "default": "" } ] }
```
A tool NOT listed is 403'd by the proxy. `entry` is the served HTML. `params` (optional) declares URL query inputs (below).

## Template + data (keep the app tiny; load data from other artifacts)
An APP can be **just template + code** and load its data from **separate** artifacts at
view-time — so agents stop re-baking template+data into one giant artifact. The bridge
reaches other artifacts **in-process as the signed-in viewer, no consent popup** — every
arti tool, reads and writes alike.

- Allowlist the arti tools you need + declare a `params` entry for the data pointer (see the manifest above). The sugar helpers below always call server `arti`, so allowlist that name even on an instance that also registers the loopback `arti-self` server.
- In the page:
  ```js
  const data = await window.arti.loadJSON(window.arti.params.data);   // read + parse a JSON artifact
  // loadArtifact returns an object, not the body string:
  const { text, contentType, sizeBytes, sha256, truncated } = await window.arti.loadArtifact(idOrSlug);
  const rows = await window.arti.search({ labels:["dataset:demo"], order_by:"created", order_dir:"desc" });
  ```
- Deep-link / embed with the pointer: `…/app/<slug>?data=sales-q1`. Switching datasets should **re-render in place** (call your `render(slug)` again = loadJSON + rebuild), NOT `location.reload()`.
- `search_artifacts` honors an explicit `order_by` (routes through Postgres); free-text without it uses relevance. Empty `q` is allowed when `labels`/`scope` is given (label-only enumeration).

## Pin the exact tool identifiers first (no runtime search)
The manifest is a static allowlist — resolve the **native** tool name before writing it.
Ask your arti operator (or list the tools your arti instance exposes) for each server you
need, and record the **server slug** (the lowercase prefix arti registers the server
under) and the **native tool name** (what the upstream server itself registers — some
gateways surface a `<slug>_<tool>` *alias* that differs from the native name). Pin
`{ "server":"<slug>", "tool":"<native>" }` and call `callTool("<slug>","<native>",…)`
with the native name. (See *Troubleshooting: Unknown tool*.)

## The injected bridge (what the page calls)
arti injects this just before `</body>` at serve time:
- `window.arti.callTool(server, tool, args, opts?) → Promise<result>` — the one primitive (raw MCP result; on 401 opens the OAuth popup, waits, retries). `opts.timeout_ms` sets this call's budget — see *Timeouts and errors* below.
- `window.arti.loadArtifact(idOrSlug, opts?)` → `{text, contentType, sizeBytes, sha256, truncated}`; `opts` may carry `{version, max_bytes}`.
- `window.arti.loadJSON(idOrSlug, opts?)` → the artifact parsed as JSON (throws on bad JSON). `window.arti.search(opts)` → `{artifacts, total}`. Both are sugar over arti's read tools (in-process, popup-free). A write via
`callTool("arti","add_artifact",…)` is in-process too, stamped `written_via: app:<uuid>`
with the viewer as `creator`.
- `window.arti.params` — the typed, defaulted URL-param object (`{}` if none declared).
- Host-compat shims (so unmodified assistant-authored HTML artifacts run as-is): `window.claude.complete` and similar askLLM helpers → `llm/complete`, degrading to `""`/`{text:""}` if `llm` isn't allowlisted.

### ⚠️ The one gotcha: the bridge loads AFTER your `<script>`
Top-level code runs **before** `window.arti` exists. **Touch the bridge lazily** — inside event handlers, or gate on `DOMContentLoaded`. Never freeze a capability flag at load:
```js
// BAD:  const hasAI = !!window.arti;                       // may be false — bridge not loaded yet
// GOOD: function hasAI(){ return !!(window.arti && window.arti.callTool); }
```

## Timeouts and errors (`callTool`)
Every call has a budget the proxy holds the upstream to. Pass it per call when the default is wrong for the tool:
```js
const r = await window.arti.callTool("warehouse", "query",
  { statement, wait_seconds: 80 }, { timeout_ms: 85000 });
```
- **Default 60s; the operator sets the cap** (`ARTI_APP_CALL_TIMEOUT_MAX`, 90s by default). A larger `timeout_ms` is clamped, and the error `detail` names the cap. The cap has to sit under whatever edge fronts arti (a CDN or ingress read timeout), so work longer than the cap must use the tool's own start/poll shape (a statement handle you poll and page).
- **A rejected call is an `Error` with structured fields:** `err.code`, `err.status`, `err.server`, `err.tool`, `err.retryAfter`. Branch on `err.code`, never on `err.message`:

| `err.code` | status | what to do |
|---|---|---|
| `upstream_timeout` | 503 | The upstream did not answer within the budget. The work may still be running upstream — **resume by handle, do not re-run**; or pass a larger `timeout_ms`. |
| `upstream_response_too_large` | 503 | One response exceeded the 16 MiB read cap. Ask for smaller pages. |
| `proxy_busy` | 503 | This arti process is relaying its maximum number of concurrent calls. Wait `err.retryAfter` seconds, retry. |
| `tool_error` | 503 | The server answered with a JSON-RPC error object (protocol level). Retrying cannot help; `detail` carries the upstream code and message. |
| `upstream_error` | 503 | Any other upstream failure (HTTP 5xx, malformed body, connection error). |
| `not_allowlisted` | 403 | `(server, tool)` is missing from `arti-app.json`. Fix the manifest, re-publish. |
| `unknown_server` | 400 | The server is not configured on this arti. |
| `network_error` | 0 | `fetch` itself failed: the network is down, or an edge error page without CORS headers replaced arti's answer. |
| `edge_timeout` | 502/504/524 | The edge answered in arti's place. Treat like `upstream_timeout`. |
| `bad-request`, `forbidden`, `slug-exists`, `not-found`, … | as REST | In-process `arti/*` tools answer with the same codes as `POST /api/artifacts`. |
| `budget_exceeded`, `rate_limited`, `overloaded`, `upstream` | 429 / 503 | `llm/complete`'s own codes (`err.retryAfter` on 429). |

- **A failed tool *execution* does not reject.** Most MCP servers report an unknown tool, bad arguments or a query error as a **200 result with `isError: true`** and the message in `content[0].text`. Always check `r.isError` after a successful `await`.
- **Large results:** the proxy buffers each response (16 MiB cap) and relays a bounded number of calls per process (`ARTI_APP_CALL_MAX_INFLIGHT`) — page through big tables rather than pulling them whole, and pass `{max_bytes}` to `loadArtifact` for big artifacts.

```js
let r;
try {
  r = await window.arti.callTool("warehouse", "query", args, { timeout_ms: 85000 });
} catch (e) {
  if (e.code === "upstream_timeout") return resumeByHandle();       // never re-run the statement
  if (e.code === "proxy_busy") { await sleep((e.retryAfter || 1) * 1000); return retry(); }
  throw e;                                                         // e.message is human-readable; show it
}
if (r.isError) throw new Error(r.content?.[0]?.text || "tool failed");
```

## Calling the LLM (`llm.complete`)
```js
const res = await window.arti.callTool("llm","complete",{ system:"Be concise.",
  prompt:userText, max_tokens:500, response_format:"json" });
const text = res.content?.[0]?.text ?? "";
```
JSON mode is best-effort — parse defensively (strip ```json fences, slice `{`…`}`).

## Uploading the app
Build `index.html` + `arti-app.json` into a folder, **zip them at the root** (no wrapping dir), then upload as an `APP`:
```bash
# headless (ARTI_API_KEY): stream the zip from disk — never base64 it through a tool call
ARTI="${ARTI_BASE_URL:-https://arti.example.com}"
curl -sS -XPOST "$ARTI/api/artifacts" -H "Authorization: Bearer $ARTI_API_KEY" \
  -F file=@/tmp/workspace/app.zip -F artifact_type=APP -F content_type=application/zip \
  -F named_slug=my-app -F title="My App" -F labels=app -F labels=auto-gen
#   -> 201 {url}; open the LIVE app at /app/<slug>  (NOT /s/<slug>, the read-only viewer)
```
- The proxy reads `arti-app.json` from inside the zip; its `entry` is served (don't pass an entry_point separately for an APP).
- Re-upload with the same `named_slug` to publish a new version.
- On a local machine, `arti add ./app-dir --type app --slug …` (see arti-cli-usage-guide). The arti MCP `add_artifact` with a base64'd zip also works for **tiny** apps, but base64 through a tool call truncates on real-size zips — prefer curl-from-disk. See arti-usage-guide for the upload paths.

## Sandbox constraints (design around these)
- **Opaque origin**, no `allow-same-origin`: the page can't read cookies or reach arti's other APIs — only the bridge. That's the governance guarantee.
- **No `<form>` submit** (`allow-forms` off) — wire buttons/inputs with JS.
- **Relative asset paths** resolve via an injected `<base href>` to the package files (`./style.css`, `./logo.png` work). Keep it self-contained (inline CSS/JS; fonts via one `<link>`).

## URL params + iframe embedding
Declare params in `arti-app.json` (types `string|number|boolean|enum`, optional `default`);
arti coerces the request query and hands the page `window.arti.params` — lenient
(missing/malformed → default, with a `{name,reason}` in `params._errors`; unknown keys
dropped). Cross-origin embed: `<iframe src="…/app/<slug>/<ver>?q=…">` — the embedder's
origin must be on the operator's `ARTI_APP_FRAME_ANCESTORS` allowlist (otherwise the
browser refuses it — ask the operator to add your origin).

## Troubleshooting: `Unknown tool`
`callTool` returns `{isError:true, …"Unknown tool: '<server>_<tool>'"}` even though the
name looked right? The bridge passes the `tool` arg **straight to the upstream server**
(it does NOT prepend the slug), so the page must call the **native** tool name — not a
gateway alias that some tool-search surfaces expose. Fix: get the authoritative name from
the server's own tool list, use it verbatim in both the manifest and `callTool`, re-zip,
re-publish. Nearby-but-distinct failures: **403 / access denied** = an authorization
policy gate (app calls run as the end user via on-behalf-of — stricter than your session),
not a naming problem; **stale cache** = the iframe served the previous version
(hard-reload; confirm the served package).

## Don'ts
- **Don't** put a tool in the page that isn't in `arti-app.json` (proxy 403s it). **Don't** guess a `(server,tool)` pair — pin the **native** name from the server's tool list.
- **Don't** hardcode API keys or call MCP servers / an LLM directly — everything goes through `window.arti.callTool`.
- **Don't** read `window.arti` at script top-level — lazily, in handlers.
- **Don't** treat `loadArtifact`'s return as the body string — read `.text` (or use `loadJSON`).
- **Don't** classify failures by `err.message` — use `err.code`. **Don't** re-run a call that timed out when the tool can resume by handle; the work is still running upstream.
- **Don't** assume a resolved call succeeded — check `r.isError`.
- **Don't** wrap the files in a top-level folder inside the zip — `index.html` + `arti-app.json` at the zip root.
- **Don't** base64 a real-size zip through an MCP tool call — curl the zip from disk (arti-usage-guide).
- **Don't** serve the app from `/s/<slug>` (that's the viewer) — the live app is `/app/<slug>`.
