---
title: CLI
order: 3
summary: Every arti CLI command and flag — login/auth, add, append, edit, access, get, list/search, versions, url, rm, and the env vars (ARTI_BASE_URL, ARTI_TOKEN) that drive them.
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

`arti login` · `logout` · `whoami` · `add` · `append` · `edit` · `access` · `get` ·
`rm` · `ls` · `versions` · `url` · `search` · `version` · `update` (and the hidden
`token`).

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
| `--allow-type-change` | bool | With `--slug`: allow this version to change the document's `artifact_type` or content type. Without it such a version is refused (409), so a markdown document can't silently become an HTML one. |
| `--access` | string[] | Read-access pattern, repeatable; glob-on-email; `*` = everyone. Defaults to client config, then server `*`. |
| `--private` | bool | Creator-only read (sends empty `allowed_access`). Mutually exclusive with `--access`. |
| `--write-access` | string[] | Write-access pattern, repeatable; the subset of readers allowed to push new versions / append / edit. Absent = writers follow readers. Server unions these into `--access`. |
| `--write-private` | bool | Only you (the creator) may write; readers stay read-only (sends empty `allowed_write`). Mutually exclusive with `--write-access`. |
| `--compress` | string | `auto` (default) \| `gzip` \| `none`. `auto` gzips textual uploads (HTML/JS/JSON/text) with `Content-Encoding: gzip` so Cloudflare's WAF can't `403` inline `<script>`; already-compressed uploads (zip/image/PDF) are sent as-is. `gzip` forces it; `none` disables it. |

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
| `--private` | bool | Creator-only read (explicit empty list). Mutually exclusive with `--access`. |
| `--write-access` | string[] | Override the write list, repeatable (else inherit). |
| `--write-private` | bool | Creator-only writes (explicit empty list). Mutually exclusive with `--write-access`. |
| `--compress` | string | `auto` (default) \| `gzip` \| `none`. Append bodies are always text, so `auto` gzips them (`Content-Encoding: gzip`) to keep appended `<script>` clear of the WAF. |

An access pair that differs from the slug's current one applies to **all
versions of the document** (slug owner or admin only) — same rule as
[`arti access`](#arti-access) and the PATCH endpoint.

```sh
echo "new entry" | arti append --slug changelog
arti append --slug log line.txt --auto-key
arti append --slug thread entry.md --idempotency-key "$(uuidgen)"
```

### `arti edit`

Edit an artifact's metadata **in place, without creating a new version** — the same
`PATCH /api/artifacts/{id}` the web editor and the MCP `update_artifact` tool use. This
is how you fix a doc that was published bare: labels and description are the two fields
browse and search surface, and re-publishing just to add a label would mint a pointless
version (and, for a PACKAGE or ATTACHMENT, require the original bytes). Content and
artifact type stay immutable — use `arti add` with the same `--slug` to change the body.
Access lives in [`arti access`](#arti-access).

With no flags it prints the editable fields and writes nothing.

| Positional | Meaning |
|---|---|
| `ident` | UUID (that exact version) or slug (its latest version, or `--version`) |

| Flag | Short | Type | Meaning |
|---|---|---|---|
| `--version` | `-v` | int | Version to edit (slug only; default: latest). |
| `--title` | | string | Replace the title. Cannot be empty. |
| `--description` | | string | Replace the description; `--description ''` clears it. |
| `--label` | | string[] | **Replace** the label set, repeatable. Omit to keep current. |
| `--clear-labels` | | bool | Remove all labels. Mutually exclusive with `--label`. |
| `--scope` | | string[] | **Replace** the scope set, repeatable. Omit to keep current. |
| `--clear-scopes` | | bool | Remove all scopes. Mutually exclusive with `--scope`. |
| `--comments` / `--no-comments` | | bool | Turn commenting on/off for the whole document. Owner or admin only. |

`--label` and `--scope` replace rather than merge (the API has no add/remove verb), so
carry forward what you want to keep — including a structural label like `skill` or
`kind:*`, which type-scoped listings filter on.

Title, description, labels and scopes are **per-version**: the edit lands on the one
version `ident` resolves to, and siblings keep theirs. The comment switch is
**per-document** — it applies to every version of the slug.

```sh
arti edit my-doc                                     # show what's editable
arti edit my-doc --title 'Q3 treasury review'
arti edit my-doc --description 'what changed and why'
arti edit my-doc --description ''                    # clear the description
arti edit my-doc --label report --label treasury     # replaces the label set
arti edit my-doc --scope a:bt-auto-route
arti edit my-doc -v 2 --label report                 # a specific version
arti edit my-doc --no-comments                       # owner/admin only
```

### `arti access`

Show or edit an artifact's access control **without creating a new version**.
With no flags it prints the current read/write lists; with flags it PATCHes
them. Access is a property of the document: the edit applies to **every
version of the slug** (archived versions included), and changing it requires
being the slug's owner (its earliest version's creator) or an admin.

| Positional | Meaning |
|---|---|
| `ident` | UUID or slug |

| Flag | Type | Meaning |
|---|---|---|
| `--access` | string[] | Replace read access, repeatable: an email, `*@domain`, `group:<name>`, or `*` = everyone. Omit to keep current. |
| `--private` | bool | Creator-only read (explicit empty list). Mutually exclusive with `--access`. |
| `--write-access` | string[] | Replace the write list, repeatable — the subset of readers who may push versions / append / edit. The server unions it into read access. Omit to keep current. |
| `--write-private` | bool | Creator-only writes; readers stay read-only. Mutually exclusive with `--write-access`. |

Flags you omit leave that side untouched — a `--write-access` edit doesn't
resend read access, and vice versa.

```sh
arti access my-doc                                  # show current access
arti access my-doc --access '*@example.com'         # domain-readable, all versions
arti access my-doc --access alice@x --access group:eng
arti access my-doc --private                        # creator-only
arti access my-doc --write-access group:eng         # readers stay; group may write
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
