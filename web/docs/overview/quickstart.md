---
title: Quickstart
order: 3
summary: Upload and share your first artifact in five minutes, by web or CLI — covering slugs, auto-versioning, and fetching the content back.
---

# Quickstart

Two paths to the same outcome: a stored artifact with a canonical, shareable link. Pick the web UI if you have a file in front of you, the CLI if you live in a terminal or script things.

## How an upload flows

![How an upload flows](./quickstart.svg)

You hand content to `arti-server`. It writes the metadata row (and small TEXT bodies) to Postgres, pushes large bytes and bundle files to S3, and returns a canonical link. Anyone who opens that link reads it back through the same server. That's the whole loop — the rest is detail.

## Web path

1. Open your arti server (e.g. `https://arti.example.com/`) and sign in with
   your organization's identity provider.
2. Use the upload control to add a file — markdown, HTML, plaintext, or a zip bundle.
3. Optionally set a **slug** (a stable name), a **title**, a **scope** (e.g. `topic:funds`), and **labels** (e.g. `report`).
4. Submit. You land on the viewer at the artifact's URL — `/s/<slug>` if you gave a slug, otherwise `/a/<uuid>`. Copy that URL to share.

See the [Web UI guide](../guides/web-ui.md) for the catalog, filtering, and the viewer in depth.

## CLI path

Install the binary and authenticate once (full detail in [Command line](../guides/command-line.md)):

```sh
make build-cli                       # from a checkout of the repo
cp bin/arti ~/.local/bin/arti        # any dir on your $PATH
arti login                           # opens a browser, Google OAuth
```

### 1. Upload

```sh
# A markdown doc with a stable slug. MIME is sniffed from the .md extension.
arti add notes.md --slug daily-notes --title "Daily notes"
```

`arti add` prints the canonical URL to **stdout** (metadata goes to stderr), so it's pipe-friendly:

```sh
URL=$(arti add notes.md --slug daily-notes --title "Daily notes")
echo "$URL"
# → https://arti.example.com/s/daily-notes
```

You can also pipe from stdin or upload a whole directory (auto-zipped into a PACKAGE):

```sh
echo "shopping list" | arti add - --title shopping --content-type plain
arti add ./my-skill/ --slug my-skill --label skill --entry-point skill.md
```

### 2. Version

Re-upload to the same slug and arti assigns the next version automatically — you don't pick the number:

```sh
arti add notes.md --slug daily-notes     # becomes v2
arti versions daily-notes                # lists v1, v2, …
```

### 3. Share and fetch

```sh
arti url daily-notes                      # canonical link (latest)
# → https://arti.example.com/s/daily-notes/2

arti get daily-notes -q > out.md          # body only (latest), to a file
arti get daily-notes -v 1 -q              # pin a version
arti get my-skill/skill.md                # one entry out of a PACKAGE
arti get my-skill --extract ./out         # extract a whole PACKAGE
```

`/s/daily-notes` always points at the latest version; `/s/daily-notes/2` pins version 2. Both forms — and the legacy `v_2` spelling — are explained in [Concepts → URL forms](concepts.md#url-forms).

## Next

- **[Command line](../guides/command-line.md)** — every CLI subcommand, flag, and the stdout/stderr conventions.
- **[Web UI](../guides/web-ui.md)** — catalog, search, filtering, and the viewer.
- **[Concepts](concepts.md)** — slugs, versions, scope, labels, and the artifact types in detail.
