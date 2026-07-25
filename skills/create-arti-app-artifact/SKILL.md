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

- **`llm` server, `complete` tool** — a built-in single-completion call (arti's service key, no agent loop), backed by the model the operator configures. `max_tokens` is capped by the operator.
- **Any MCP server the operator configured** (e.g. `notion`, `slack`, `github`, `arti`, …) — per-user OAuth (on-behalf-of); the **first** call to one pops a consent window (needs a user gesture).

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
reads other artifacts **in-process as the signed-in viewer, no consent popup** for arti's
read tools; writes still go on-behalf-of.

- Allowlist arti's read tools + declare a `params` entry for the data pointer (see the manifest above).
- In the page:
  ```js
  const data = await window.arti.loadJSON(window.arti.params.data);   // read + parse a JSON artifact
  // or: const raw = await window.arti.loadArtifact(idOrSlug);
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
- `window.arti.callTool(server, tool, args) → Promise<result>` — the one primitive (raw MCP result; on 401 opens the OAuth popup, waits, retries).
- `window.arti.loadArtifact(idOrSlug)` / `loadJSON(idOrSlug)` / `search(opts)` — sugar over arti's read tools (in-process, popup-free).
- `window.arti.params` — the typed, defaulted URL-param object (`{}` if none declared).
- Host-compat shims (so unmodified assistant-authored HTML artifacts run as-is): `window.claude.complete` and similar askLLM helpers → `llm/complete`, degrading to `""`/`{text:""}` if `llm` isn't allowlisted.

### ⚠️ The one gotcha: the bridge loads AFTER your `<script>`
Top-level code runs **before** `window.arti` exists. **Touch the bridge lazily** — inside event handlers, or gate on `DOMContentLoaded`. Never freeze a capability flag at load:
```js
// BAD:  const hasAI = !!window.arti;                       // may be false — bridge not loaded yet
// GOOD: function hasAI(){ return !!(window.arti && window.arti.callTool); }
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
- **Don't** wrap the files in a top-level folder inside the zip — `index.html` + `arti-app.json` at the zip root.
- **Don't** base64 a real-size zip through an MCP tool call — curl the zip from disk (arti-usage-guide).
- **Don't** serve the app from `/s/<slug>` (that's the viewer) — the live app is `/app/<slug>`.
