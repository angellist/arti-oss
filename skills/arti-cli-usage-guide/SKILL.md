---
name: arti-cli-usage-guide
description: >-
  Use the local `arti` command-line tool to upload, append to, fetch, list, search,
  version, or archive arti artifacts from a LOCAL machine (dev box) — "arti up",
  "publish/upload this", "append this to X", "get/download artifact X",
  "list/search my artifacts", "shareable arti URL", "who can see this artifact".
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
arti add    [FILE|DIR|-] [--slug NAME] [--title T] [--description D]
                         [--type text|package|app | --content-type MIME]
                         [--entry-point E] [--scope S] [--label L ...] [--ensure-new]
                         [--access GLOB ... | --private] [--write-access GLOB ... | --write-private]
arti append --slug NAME [FILE|-] [--separator S] [--idempotency-key K | --auto-key]
                         [--title T] [--content-type MIME] [--scope S] [--label L ...]
                         [--access GLOB ... | --private] [--write-access GLOB ... | --write-private]
arti access <UUID|SLUG> [--access GLOB ... | --private] [--write-access GLOB ... | --write-private]
arti get    <UUID|SLUG> [-v N] [-q | -m] [--extract DIR]
arti ls     [UUID] [--type …] [--creator EMAIL] [--label L ...] [--limit N] [--include-archived]
arti versions <SLUG> · arti url <UUID|SLUG> · arti search <QUERY> · arti rm <UUID|SLUG> [-y]
arti login | logout | whoami · arti version | update
```

### add — upload
- **File** → content-type sniffed by extension (`.md`→markdown, `.html`→html, `.zip`→PACKAGE). **Dir** → zipped deterministically → PACKAGE. **Stdin** → `arti add -`.
- `--slug NAME` = stable URL; re-uploading the same slug **auto-bumps the version**.
- `--type app` uploads a governed APP (dir/zip must contain `arti-app.json`) — to *build* one, see **create-arti-app-artifact**.
- **Output:** URL → stdout; `artifact_id`/slug/version → stderr. `URL=$(arti add f.md --slug s)` captures just the URL.

### append — add to an existing slug without re-uploading the whole body
Use for accumulating logs, running notes, or per-event records under one URL. The
*server* reads the latest version, concatenates `--separator` (default `\n\n`) + your
content, and writes the next version — so it's atomic and safe against concurrent
writers, unlike `get` → edit → `add`. Text only (`text/*` and structured text).
- Auto-creates v1 if the slug doesn't exist; **on that path `--title` and
  `--content-type` are required**. Later appends ignore them unless overriding.
- `--scope` / `--label` / `--access` are **inherited from the prior version** when
  omitted — pass them only to change something.
- **Retries:** pass `--idempotency-key K` (24h server-side dedup window) whenever a
  retry could double-fire — key it to the logical event (e.g. an upstream comment id),
  not the attempt. `--auto-key` derives `sha256(slug + content)` instead, which
  dedups identical bodies but *not* re-sends of a changed body; the two are mutually
  exclusive. This matters more for agents than for humans: an interrupted run that
  re-executes will otherwise append twice.

### Access control (`add`, `append`, and the `access` command)
Patterns match reader email or a group: `--access '*@example.com'`,
`--access alice@example.com`, `--access group:eng`, `--access '*'` = everyone.
- `--private` = creator-only (sends an explicit empty list). Mutually exclusive with `--access`.
- `--write-access` (a subset of readers who may push new versions / append / edit) and
  `--write-private` (creator-only writes, readers stay read-only) — on `add`, `append`,
  and `access` alike. Absent = writers follow readers.
- **Access is per-DOCUMENT, not per-version:** an access pair that differs from the
  slug's current one applies to every version (archived included), and only the slug's
  owner (its earliest version's creator) or an admin may change it. Re-sending the
  current pair is always a legal no-op.
- **`arti access <slug>`** shows the current read/write lists; with flags it edits them
  in place — no new version. This is the way to fix visibility on something already
  published (`arti access my-doc --access group:eng --write-private`).
- Omitting all access flags on `add` falls back to `default_access` in
  `~/.config/arti/config.json` (`{"default_access": ["*@example.com"]}`), then to the
  server default. On `append`, omitting inherits from the prior version.
- **Don't guess** at access for someone else's data — if the user hasn't said who should
  see it and it looks sensitive, ask.

### get / ls / search / versions / url / rm
- `arti get <id>` — metadata→stderr, content→stdout; `-q` content-only, `-m` meta-only, `-v N` a version, `--extract DIR` unzip a PACKAGE, `<UUID>/path/in/zip` one file.
- `search <query>` = `field:value` filters (`type:` `label:` `scope:` `creator:` `slug:`, `*` = glob) + free text, all ANDed — quote it.
- `rm <id>` archives (soft). Interactive confirm by default; `-y` only when the user explicitly authorized it. Archived artifacts are hidden from `ls` until `--include-archived`.

### version / update — the binary itself
`arti version` prints the build and checks main for a newer one; `arti update` runs
`go install …@latest` and copies the result over the running binary (needs a Go
toolchain + repo access). **Caveat:** a locally built binary is often *unstamped*, and
then `version` reports `v0.0.0-…` and cannot compare against main at all — a
"can't compare" answer means unknown, not up-to-date. Rebuild from the repo
(`make build-cli`) if a missing flag suggests the binary is behind this guide.

## Labeling (recommended)
A useful convention: give every `arti add` ≥1 `--label` (a `kind` floor such as
`report · study · analysis · research · design-doc · eval · run-record · dry-run ·
postmortem · memory · context`), plus a project label + `auto-gen` when they apply.
Reuse `--slug` within a session so versions accumulate under one URL. Adapt the
label vocabulary to your team.

## Don'ts
- **Don't** hand-build REST calls with curl when the CLI is present — `arti` handles OAuth/MIME/packaging. (curl is the *headless* path — see arti-usage-guide.)
- **Don't** emulate `append` with `get` → edit → `add`: that read-modify-write races, and a concurrent writer's entry is lost. Use `append`.
- **Don't** `arti rm -y` without explicit confirmation. **Don't** run `arti login` for the user (needs a browser).
- **Don't** assume this works on a hosted agent — it needs the local `arti` binary.
