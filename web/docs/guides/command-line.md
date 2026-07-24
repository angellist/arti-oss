---
title: Command line
order: 2
summary: Install, authenticate, and run the everyday arti CLI tasks — add files and text, fetch by slug or UUID, list and search.
---

# Command line

`arti` is a single static Go binary. It talks to the arti REST API over HTTPS,
handles OAuth for you, and covers the same tasks as the web UI from a shell or a
script. This page walks the common tasks; the [full flag reference](../reference/cli.md)
is exhaustive.

## Install

Build from source:

```sh
git clone git@github.com:angellist/arti-oss.git && cd arti
make build-cli
cp bin/arti ~/.local/bin/arti        # or any dir on your $PATH
arti --help
```

`go install` works too if you have repo access:

```sh
# (add GOPRIVATE=github.com/angellist/arti-oss when the repo you install from is private)
go install github.com/angellist/arti-oss/cmd/arti@latest
```

The CLI ships from `main` with no release tags, so it can drift. `arti version`
prints the build's version and commit and tells you whether `main` is ahead;
`arti update` re-runs `go install …@latest` over the in-use binary. Once a day,
interactive shells get a nudge if a newer `main` exists. Silence that check with
`ARTI_DISABLE_UPDATE_CHECK=1`.

## Authenticate

For prod or staging, log in once:

```sh
arti login
```

This opens a browser, signs you in through the server's configured identity
provider (your email domain must be on the server's allowlist), and
stashes `{access_token, refresh_token, expires_at}` in
`~/.config/arti/token.json` (mode 0600). The token auto-refreshes on use, so you
rarely log in again. `arti whoami` prints the logged-in email; `arti logout`
clears the cached token.

### Headless: `ARTI_TOKEN`

In a CI job, a sandbox, or an agent runtime where no browser is available, skip
`arti login` entirely by exporting a bearer token. When `ARTI_TOKEN` is set, the
CLI uses it directly and ignores the on-disk login (`cmd/arti/token.go:39`):

```sh
export ARTI_TOKEN="<device-flow-upload-token>"
arti add report.md --slug nightly-report
```

These tokens come from arti's device-authorization flow (RFC 8628) — the same
mechanism the [MCP `authenticate` tool](mcp.md) drives for headless agents. See
[Authentication](../architecture/auth.md) for how the flow issues and refreshes
them.

### Local dev

When the server runs with `ARTI_AUTH_DISABLED=true`, every write is attributed to
`ARTI_LOCAL_EMAIL` and the CLI needs no login at all — just point it at the local
server:

```sh
ARTI_BASE_URL=http://localhost:8095 arti ls
```

## Point at a server

The CLI defaults to `https://arti.example.com`. Override per command:

```sh
ARTI_BASE_URL=http://localhost:8095 arti ls    # env var
arti --base-url http://localhost:8095 ls       # flag (wins over the env var)
```

## Add an artifact

`arti add` takes a file, a directory, or stdin (`-`). The artifact type and MIME
are sniffed from the path unless you override them.

```sh
# A markdown doc — content-type sniffed from .md, slug is stable
arti add notes.md --slug daily-notes --title "Daily notes"

# Text straight from stdin
echo "shopping list" | arti add - --title shopping --type plain

# An HTML page renders as a sandboxed artifact in the viewer
arti add dashboard.html --slug q1-dashboard --label dashboard
```

Re-using a `--slug` that already exists auto-bumps the version (v2, v3, …). Pass
`--ensure-new` if you'd rather the command fail than collide.

`stdout` is just the artifact URL, so it's safe to capture; metadata and table
headers go to `stderr`:

```sh
URL=$(arti add notes.md --slug daily-notes --title "Notes")
```

### Slugs and labels

A **slug** is a stable, human-readable handle. Every re-upload under the same
slug is a new version of the same logical artifact, and `arti url SLUG` always
points at the latest. **Labels** (`--label`, repeatable) tag an artifact for
catalog filtering — they're how you find things later in [the web UI](web-ui.md)
or with `arti search`. **Scope** (`--scope`) is a free-form ownership/grouping
key like `user:alice@example.com`, `agent:bt-router`, or `topic:funds`. See
[Concepts](../overview/concepts.md) for the full model.

```sh
arti add q1-notes.md --slug q1-notes --scope topic:funds --label report --label q1
```

Control who can read it with `--access` (repeatable; an email, `*@domain`, or
`*` for everyone) or `--private` for creator-only. With neither flag, access is
inherited from a prior version or falls back to the server default. See
[Authentication](../architecture/auth.md) for the access model.

### Upload a directory (PACKAGE)

Point `arti add` at a directory and it's deterministically zipped into a PACKAGE
artifact — the viewer shows it as a browsable file tree. `--entry-point` marks
the file the viewer highlights first.

```sh
arti add ./my-skill/ --slug my-skill --label skill --entry-point skill.md

# An already-zipped bundle
arti add bundle.zip --type package --slug my-bundle
```

## Get an artifact

`arti get` accepts a UUID or a slug. Metadata prints to `stderr`, the body to
`stdout`, so piping just works.

```sh
arti get daily-notes                 # latest version
arti get daily-notes -v 1            # pinned version (slug only)
arti get daily-notes -q > out.md     # -q / --content-only: body only, no header
arti get <uuid> -m                   # -m / --meta-only: metadata, skip body
```

For a PACKAGE, address one entry by appending its path, or extract the whole
bundle:

```sh
arti get my-skill/skill.md            # one file out of the package
arti get my-skill/scripts/run.sh
arti get my-skill --extract ./out     # download + unzip the whole package
```

## List, search, versions

```sh
# Catalog, with filters (labels are ANDed)
arti ls --label report --scope topic:funds
arti ls --type PACKAGE
arti ls <pkg-uuid>                     # entries inside a PACKAGE
arti ls --creator alice@example.com --limit 20

# Substring search with field:value syntax
arti search "release notes label:weekly creator:alice@example.com"
arti search "slug:daily-notes"

# Every non-deleted version under a slug
arti versions daily-notes

# Canonical short URL (stdout)
arti url daily-notes
# → https://arti.example.com/s/daily-notes/v_2
```

`arti search` recognizes the keys `slug:`, `creator:`, `scope:`, `type:`, and
`label:` (repeatable), mixed freely with plain text.

## Append (atomic logs)

`arti append` is built for incremental writes — the server reads the latest
version, concatenates, and writes a new one under the (slug, version)
constraint, retrying on races. It auto-creates v1 if the slug is new. An
idempotency key gives safe-retry semantics, which matters for agent writes:

```sh
arti append --slug bt-router-feedback-raw \
  --idempotency-key "feedback-cnv_xyz-2026-06-08" \
  - <<< "## 2026-06-08 · alice
some new lesson"
```

## Remove

`arti rm` archives (soft-deletes) an artifact. It confirms interactively — type
`delete` for a slug, `y/N` for a UUID — and `-y` skips the prompt.

```sh
arti rm daily-notes        # asks for confirmation
arti rm <uuid> -y          # no prompt
```

## Exit codes

- `0` — success
- `1` — user error (bad slug, validation, etc.)
- network errors propagate as non-zero exits with the detail on `stderr`

## See also

- [CLI reference](../reference/cli.md) — every command, flag, and identifier form.
- [Agent recipe](../recipes/agent.md) — using the CLI (and `ARTI_TOKEN`) from an automated agent.
- [Using the web UI](web-ui.md) — the same tasks, in the browser.
