---
name: arti-cli-usage-guide
description: >-
  Use the local `arti` command-line tool to upload, fetch, list, search, version, or
  archive arti artifacts from a LOCAL machine (dev box) — "arti up", "publish/upload
  this", "get/download artifact X", "list/search my artifacts", "shareable arti URL".
  This is the CLI path ONLY; on a hosted agent without the binary, use
  arti-usage-guide (MCP tools or an API key + curl).
---

# arti-cli-usage-guide (local `arti` CLI)

`arti` is a Go binary (built from this repository via `make build`, or
`go install github.com/angellist/arti-oss/cmd/arti@latest`) that wraps the arti REST
API and handles OAuth, content-type sniffing, package zipping, slug versioning,
and URL formatting. It runs on your **local machine**. On a hosted agent with no
`arti` binary, use arti-usage-guide instead (MCP tools or `ARTI_API_KEY` + curl).

## Auth
- OAuth PKCE via `arti login` (opens a browser). Token cached locally. On an auth
  error, tell the user to run `arti login` — don't try to bypass it.
- `arti whoami` prints the logged-in email. Caveat: it decodes the local token
  without a server call, so it can "lie" for a host the token isn't valid on — trust
  `arti ls` over `whoami`.
- `ARTI_BASE_URL` sets the endpoint (defaults to the value baked in at build time);
  one token file per host.

## Subcommands
```
arti add [FILE|DIR|-] [--slug NAME] [--title T] [--description D]
                      [--type text|package|app | --content-type MIME]
                      [--entry-point E] [--scope S] [--label L ...] [--ensure-new]
arti get  <UUID|SLUG> [-v N] [-q | -m] [--extract DIR]
arti ls   [UUID] [--type …] [--creator EMAIL] [--label L ...] [--limit N]
arti versions <SLUG> · arti url <UUID|SLUG> · arti search <QUERY> · arti rm <UUID|SLUG> [-y]
arti login | logout | whoami
```

### add — upload
- **File** → content-type sniffed by extension (`.md`→markdown, `.html`→html, `.zip`→PACKAGE). **Dir** → zipped deterministically → PACKAGE. **Stdin** → `arti add -`.
- `--slug NAME` = stable URL; re-uploading the same slug **auto-bumps the version**.
- `--type app` uploads a governed APP (dir/zip must contain `arti-app.json`) — to *build* one, see **create-arti-app-artifact**.
- **Output:** URL → stdout; `artifact_id`/slug/version → stderr. `URL=$(arti add f.md --slug s)` captures just the URL.

### get / ls / search / versions / url / rm
- `arti get <id>` — metadata→stderr, content→stdout; `-q` content-only, `-m` meta-only, `-v N` a version, `--extract DIR` unzip a PACKAGE, `<UUID>/path/in/zip` one file.
- `search <query>` = `field:value` filters (`type:` `label:` `scope:` `creator:` `slug:`, `*` = glob) + free text, all ANDed — quote it.
- `rm <id>` archives (soft). Interactive confirm by default; `-y` only when the user explicitly authorized it.

## Labeling (recommended)
A useful convention: give every `arti add` ≥1 `--label` (a `kind` floor such as
`report · study · analysis · research · design-doc · eval · run-record · dry-run ·
postmortem · memory · context`), plus a project label + `auto-gen` when they apply.
Reuse `--slug` within a session so versions accumulate under one URL. Adapt the
label vocabulary to your team.

## Don'ts
- **Don't** hand-build REST calls with curl when the CLI is present — `arti` handles OAuth/MIME/packaging. (curl is the *headless* path — see arti-usage-guide.)
- **Don't** `arti rm -y` without explicit confirmation. **Don't** run `arti login` for the user (needs a browser).
- **Don't** assume this works on a hosted agent — it needs the local `arti` binary.
