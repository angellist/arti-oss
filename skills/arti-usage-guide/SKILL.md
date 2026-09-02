---
name: arti-usage-guide
description: >-
  Use whenever you need to upload to, or fetch/read/search from, an arti artifact
  store from a hosted agent (no local CLI) — "arti up", "publish/save this as an
  artifact", "upload this file/report", "get/download artifact X", "read that arti
  link", "list/search artifacts". Covers the three ways to reach arti — the arti MCP
  tools, an API-key bearer + curl (headless, preferred for real files), and the
  interactive device flow — plus labeling/slug/versioning. For building an
  interactive APP see create-arti-app-artifact. (Local machine with the `arti`
  binary? see arti-cli-usage-guide — that path needs the CLI.)
---

# arti-usage-guide

**arti** is an artifact store (a REST API + an MCP server) for publishing,
versioning, sharing, and searching documents, reports, packages, and APP
artifacts. This guide is for a **hosted agent with no local `arti` binary** — if
you're on a local machine with the CLI, use arti-cli-usage-guide instead. Set
`ARTI_BASE_URL` to your instance (examples below use `https://arti.example.com`).
Three ways to reach arti; pick by what you have.

## 1. arti MCP tools (default when the `arti` connector is attached)

If your agent has the **arti** MCP connector, call its tools directly. Major methods:

| Tool | Does |
|---|---|
| `add_artifact` | create an artifact (or a new version with `named_slug`). Content is **inline** (`content` / `content_base64`) → **small text/JSON only** (see the limit below). |
| `append_artifact` | append text to a slug (atomic; auto-creates v1) |
| `read_artifact` | read content by id/slug (metadata + body) |
| `search_artifacts` | structured search: `type:` `label:` `scope:` `creator:` `slug:` + free text (ANDed), optional `order_by`/`order_dir` |
| `list_artifacts` / `list_artifact_versions` | enumerate the catalog / a slug's versions |
| `list_package_files` / `read_package_file` | entries of / one file inside a PACKAGE |
| `get_artifact` | cheap metadata + `size_bytes` (poll before reading a big one) |
| `update_artifact` | edit **metadata only** (title/description/labels/scopes/access/comments_enabled) — NOT content (content change = `add_artifact` with the same slug). Access edits (`allowed_access`, `allowed_write`) are per-DOCUMENT: they apply to every version of the slug, owner/admin only |
| `archive_artifact` | soft-delete |

**Key limit — never push large/binary content through MCP.** `add_artifact` takes
content inline, so a big file must be base64'd into the tool-call argument — this
burns tokens and truncates on real-size payloads (a ~260 KB HTML dashboard fails).
Anything beyond small text/JSON → use the API-key path below.

## 2. API key + curl (headless — the reliable path for real files)

If an **`ARTI_API_KEY`** env var is set (an arti upload key), upload/read over
HTTP with `curl` from the bash tool. The bytes stream straight from disk — they
never enter your token stream — so this is the right path for HTML reports, PDFs,
zips, anything non-trivial.

```bash
ARTI="${ARTI_BASE_URL:-https://arti.example.com}"
# UPLOAD (multipart; streams the file from disk)
curl -sS -XPOST "$ARTI/api/artifacts" \
  -H "Authorization: Bearer $ARTI_API_KEY" \
  -F file=@/tmp/workspace/report.html \
  -F title="Q3 Report" -F content_type=text/html -F artifact_type=TEXT \
  -F labels=report -F labels=auto-gen -F named_slug=q3-report
#   -> 201 { artifact_id, url, creator, ... }   ; report the url back
```

- **Endpoint is `/api/artifacts`** — there is **no** `/api/v1/artifacts` (a wrong path 500s, not 404s).
- **Uploading HTML or JavaScript? Gzip the body — always, even one file.** Cloudflare's WAF inspects request bodies and false-matches its `<script>` rule on inline HTML/JS, so a plain upload can `403` before it reaches arti. Send the JSON body gzipped with `Content-Encoding: gzip` (the server decompresses it; the `413` cap is on the decompressed size). PACKAGE/APP zips are already compressed — no need.

  ```bash
  # HTML/JS: gzip the JSON body so the WAF can't match <script>
  jq -n --arg c "$(base64 < /tmp/workspace/report.html)" \
    '{artifact_type:"TEXT",content_type:"text/html",named_slug:"q3-report",title:"Q3 Report",labels:["report"],content_base64:$c}' \
    | gzip | curl -sS -XPOST "$ARTI/api/artifacts" \
        -H "Authorization: Bearer $ARTI_API_KEY" \
        -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
        --data-binary @-
  ```
- `-F file=@<path>` streams raw bytes — never base64/inline the file. (For **HTML/JS**, prefer the gzipped-JSON form above; the multipart body carries the raw `<script>` and can trip the WAF.)
- `artifact_type`: `TEXT` (text file + a `text/*` `content_type`) · `ATTACHMENT` (binary: PDF/image/…) · `PACKAGE` (a `.zip`) · `APP` (a zip with `arti-app.json`).
- `named_slug` → stable `/s/<slug>` URL + auto-versions on re-upload; omit for a one-off `/a/<uuid>`.
- **Read / list** the same way: `GET /api/artifacts/<uuid>` or `/api/artifacts/by-slug/<slug>[/raw]` (content) · `…/<uuid>/meta` (metadata) · `…/<uuid>/files/<path>` (a PACKAGE file) · `GET /api/artifacts/search?q=…`.
- The key is **upload-scoped**: create/append/read, **≤25 MiB**, attributed to the key's owner; it **cannot** delete/edit-metadata/admin (those `403` by design). Never echo `$ARTI_API_KEY`. `401` = missing/expired/revoked (tell the user); `413` = over 25 MiB.

## 3. Device flow (interactive — no key, and a human can approve)

No `ARTI_API_KEY` and no arti MCP connector? Mint a short-lived upload token (RFC 8628;
needs a human to approve in a browser):

```bash
ARTI="${ARTI_BASE_URL:-https://arti.example.com}"
curl -sS -XPOST "$ARTI/auth/device/code" -d '{"duration":"short"}'   # -> verification_uri_complete, device_code
# SHOW the human verification_uri_complete → they sign in (via your IdP) + Approve
curl -sS -XPOST "$ARTI/auth/device/token" -d '{"device_code":"<…>"}' # 428 pending → 200 { access_token }
export ARTI_TOKEN=<access_token>   # then use it exactly like ARTI_API_KEY in path 2 (Bearer)
```

Upload-scoped, ≤25 MiB, attributed to the approving human. Prefer a standing
`ARTI_API_KEY` for unattended agents (device tokens are short-lived and need a human).

## Labeling convention (recommended)

A useful convention: give every upload **at least one `kind` label**
(`-F labels=<kind>` / MCP `labels`) — e.g. `report · study · analysis · research ·
design-doc · eval · run-record · dry-run · postmortem · changelog · memory ·
context`. Add a **project** label and `auto-gen` (agent-made) when they apply, and
reuse a slug within a task so versions accumulate under one stable URL. Adapt the
label vocabulary to your team.

## Which guide next

- Building an interactive **APP** (calls MCP tools / an LLM through the arti proxy) → **create-arti-app-artifact**.
- On a **local machine** with the `arti` binary → **arti-cli-usage-guide**.

## Don'ts

- **Don't** upload HTML/JS with a plain (uncompressed) body — the Cloudflare WAF `403`s inline `<script>`. Gzip the body (`Content-Encoding: gzip`), always. (MCP `add_artifact` is exempt — it goes through the OBO proxy, not the public WAF — but is small-text-only anyway.)
- **Don't** base64 a large/binary file through an MCP `add_artifact` call — use `ARTI_API_KEY` + `curl` (streams from disk).
- **Don't** hit `/api/v1/artifacts` — it's `/api/artifacts`.
- **Don't** upload unlabeled, and **never** echo the key.
- **Don't** assume the `arti` CLI exists on a hosted agent — use the HTTP paths above.
