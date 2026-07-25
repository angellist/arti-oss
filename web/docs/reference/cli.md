---
title: CLI
order: 3
summary: Every arti CLI command and flag — login/auth, add, append, get, list/search, versions, url, rm, and the env vars (ARTI_BASE_URL, ARTI_TOKEN) that drive them.
---

# CLI

`arti` is the command-line client (`cmd/arti/`, built with
[Kong](https://github.com/alecthomas/kong)). It wraps the [REST API](rest-api.md):
create and version artifacts, append to slugs, fetch content, search the catalog. Output
is pipe-friendly — content and URLs go to **stdout**, metadata and tables to **stderr**.

```sh
arti add report.md --slug q3-report --label report
arti get q3-report -q | less
```

## Global flags & environment

| Flag | Env | Default | Meaning |
|---|---|---|---|
| `--base-url` | `ARTI_BASE_URL` | build default (`cmd/arti/defaults.go`) | Server base URL |

Additional environment variables:

| Env | Meaning |
|---|---|
| `ARTI_TOKEN` | Bearer token used directly, **taking precedence over the on-disk login** — set it and you can skip `arti login` (e.g. a device-flow upload token, or a self-serve `arti_upload_…` [API key](../guides/api.md#api-keys-self-serve), in a sandbox). `cmd/arti/token.go:39` |
| `ARTI_DISABLE_UPDATE_CHECK` | Disables the throttled "newer build available" nudge. `cmd/arti/selfupdate.go` |

**Auth resolution** (`loadToken`, `cmd/arti/token.go:36`): `ARTI_TOKEN` if set, else the
saved login at `~/.config/arti/token.json` (`$XDG_CONFIG_HOME/arti/token.json`, mode
`0600`). Commands that need auth fail with a "not logged in" error when neither is
present. The HTTP client has a 30s timeout and sends `Authorization: Bearer <token>`.

An optional `~/.config/arti/config.json` supplies a default access list:

```json
{ "default_access": ["*@example.com", "bob@example.com"] }
```

## Commands

`arti login` · `logout` · `whoami` · `add` · `append` · `get` · `rm` · `ls` ·
`versions` · `url` · `search` · `version` · `update` (and the hidden `token`).

### `arti login`

OAuth login via the PKCE pair flow. Opens a browser to `<base>/auth/google/login` with a
one-time `cli_code`; after authenticating, explicitly confirm the displayed code, then
the CLI polls `POST /auth/cli/exchange` and saves the returned token. Waits up to 5 minutes.

| Flag | Type | Meaning |
|---|---|---|
| `--email` | string | Skip the browser and mint a test-mode token for this email. Requires the server to run with `ARTI_TEST_MODE=true`; dev only. |

```sh
arti login                               # browser PKCE flow
arti login --email alice@example.com   # dev/test-mode only
```

### `arti logout`

Delete the local token at `~/.config/arti/token.json`.

### `arti whoami`

Print the logged-in email (from the token's `email` field, or decoded from the JWT).

### `arti add`

Create an artifact, or a new version of an existing slug. The artifact type and content
type are usually inferred (markdown vs plain text; a directory or `.zip` becomes a
PACKAGE). An explicit `--type` wins.

| Positional | Meaning |
|---|---|
| `file` | A file, a directory (auto-zipped to PACKAGE), or `-` / omitted for stdin |

| Flag | Type | Meaning |
|---|---|---|
| `--slug` | string | Named slug. Re-adding the same slug creates a new version unless `--ensure-new`. |
| `--title` | string | Title (default: filename stem, first markdown heading, or `stdin`). |
| `--description` | string | Description. |
| `--type` | string | `text` \| `package` \| `app` — usually inferred. |
| `--content-type` | string | MIME override. |
| `--entry-point` | string | PACKAGE `entry_point` override. |
| `--scope` | string[] | Scope, repeatable (e.g. `a:bt-auto-route`). |
| `--label` | string[] | Label, repeatable. |
| `--ensure-new` | bool | With `--slug`: fail (409) if the slug already exists — no auto-versioning. |
| `--access` | string[] | Read-access pattern, repeatable; glob-on-email; `*` = everyone. Defaults to client config, then server `*`. |
| `--private` | bool | Creator-only read (sends empty `allowed_access`). Mutually exclusive with `--access`. |
| `--write-access` | string[] | Write-access pattern, repeatable; the subset of readers allowed to push new versions / append / edit. Absent = writers follow readers. Server unions these into `--access`. |
| `--write-private` | bool | Only you (the creator) may write; readers stay read-only (sends empty `allowed_write`). Mutually exclusive with `--write-access`. |

Prints the artifact id (and `slug (v#)` when named) to stderr and the URL to stdout.

```sh
arti add README.md --slug docs --title "Docs"
arti add ./build --slug app-build --type package      # zips the directory
cat notes.md | arti add - --slug notes                 # from stdin
arti add report.pdf --private                          # creator-only read
# readable by the whole domain, but only the eng group may edit:
arti add --slug spec --access '*@example.com' --write-access 'idp:engineering'
arti add --slug memo --access '*' --write-private       # world-readable, creator-only writes
```

### `arti append`

Append text to a slug as a new version, atomically. Auto-creates v1 if the slug is
absent (then `--title` and `--content-type` are required). Inherits scope/label/access
from the prior version when not overridden.

| Positional | Meaning |
|---|---|
| `file` | File to append, or `-` / omitted for stdin |

| Flag | Type | Meaning |
|---|---|---|
| `--slug` | string | **Required.** Slug to append to. |
| `--separator` | string | Joiner between prior body and new content (default `"\n\n"`; `--separator=''` for none). |
| `--idempotency-key` | string | Dedup key (24h server-side TTL). Recommended for agent retries. |
| `--auto-key` | bool | Compute the key as `sha256(slug + content)`. Mutually exclusive with `--idempotency-key`. |
| `--title` | string | Required on auto-create; otherwise an override. |
| `--description` | string | Override. |
| `--content-type` | string | Required on auto-create; `text/*` and `structured-text` only. |
| `--scope` | string[] | Override, repeatable (else inherit). |
| `--label` | string[] | Override, repeatable (else inherit). |
| `--access` | string[] | Override, repeatable (else inherit). |

```sh
echo "new entry" | arti append --slug changelog
arti append --slug log line.txt --auto-key
arti append --slug thread entry.md --idempotency-key "$(uuidgen)"
```

### `arti get`

Fetch by UUID or slug. Append `/path/in/zip` to read a single file from a PACKAGE.

| Positional | Meaning |
|---|---|
| `ident` | **Required.** UUID or slug; `UUID/path/in/zip` for one PACKAGE entry. |

| Flag | Short | Type | Meaning |
|---|---|---|---|
| `--version` | `-v` | int | Specific version (slug only). |
| `--content-only` | `-q` | bool | Content to stdout, no metadata header. |
| `--meta-only` | `-m` | bool | Metadata only. |
| `--extract` | | string | PACKAGE: unzip to this directory (zip-slip protected). |

```sh
arti get <uuid>                 # content on stdout, meta on stderr
arti get docs -v 3 -q           # version 3, content only
arti get <uuid>/index.html      # one file from a PACKAGE
arti get <uuid> --extract ./out # unzip the whole PACKAGE
```

### `arti rm`

Archive (soft-delete). By UUID: that version. By slug: **all versions** under the slug.

| Positional | Meaning |
|---|---|
| `ident` | **Required.** UUID or slug. |

| Flag | Short | Type | Meaning |
|---|---|---|---|
| `--yes` | `-y` | bool | Skip the confirmation prompt. |

### `arti ls`

List the catalog, or — when given a UUID — the entries inside that PACKAGE.

| Positional | Meaning |
|---|---|
| `ident` | Optional. A PACKAGE UUID lists its entries; empty lists the catalog. |

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--type` | string | | `TEXT` \| `PACKAGE` (filter) |
| `--creator` | string | | Creator email |
| `--scope` | string | | Scope filter |
| `--label` | string[] | | Label filter, repeatable |
| `--limit` | int | 50 | |
| `--offset` | int | 0 | |
| `--include-archived` | bool | false | |

### `arti search`

Substring search over title, description, and slug.

| Positional | Meaning |
|---|---|
| `query` | **Required.** Search string. |

| Flag | Type | Default |
|---|---|---|
| `--scope` | string | |
| `--label` | string[] | |
| `--limit` | int | 50 |
| `--offset` | int | 0 |
| `--include-archived` | bool | false |

### `arti versions`

List every version of a slug. Positional `slug` (required).

### `arti url`

Print the canonical URL for a UUID or slug. Positional `ident` (required).

### `arti version`

Print the built version + commit, then check `origin/main` and warn if the local build is
stale.

### `arti update`

Update to the latest `main` build via `go install
github.com/angellist/arti-oss/cmd/arti@latest` (fetched directly from the repo,
skipping the Go proxy), then replaces the running binary if it differs.
Requires the Go toolchain and repo access.

### `arti token` (hidden)

Print the current bearer access token. Debugging aid; not shown in `--help`.

## See also

- [REST API](rest-api.md) — the endpoints these commands call.
- [Configuration](configuration.md#auth) — server-side `ARTI_TEST_MODE`, device-flow, and
  auth env vars.
- [Command-line guide](../guides/command-line.md) — task-oriented walkthroughs.
