---
title: As a content author
order: 4
summary: Publish reports and docs to arti — pick TEXT vs PACKAGE, name slugs and labels for findability, version cleanly, and share links for review with comments.
---

# As a content author

You have a report, a design doc, an analysis, or a rendered HTML page, and you
want it at a stable link your team can read, review, and find later. This is the
publish-and-share workflow.

The two ways in are the [web UI](../guides/web-ui.md) (drag-drop, browse, edit
metadata) and the [CLI](../guides/command-line.md) (`arti add`, scriptable). Use
whichever fits; the model is identical.

## Pick a type

| Your content | Type | Why |
|---|---|---|
| A markdown / text / single HTML doc | `TEXT` | Versioned, slugged, full-text searchable, renders inline. |
| A multi-file bundle (HTML site, report with assets) | `PACKAGE` | A zip served as files; the entry page renders, links resolve. |
| A single binary (PDF, image, csv) you just want hosted | `ATTACHMENT` | Stored as-is, previewed inline when the browser can. |

A self-contained HTML page can go in as `TEXT` (`content_type: text/html`). Reach
for `PACKAGE` when you have separate CSS/JS/image files. If your page is
interactive and needs to call tools or an LLM, that's an **APP** — see
[As an arti APP developer](app-developer.md).

## Name it for findability

Two pieces of metadata do the work of making something discoverable months later:

- **Slug** — the stable, human-readable name in the URL
  (`/s/<slug>`). Reuse the same slug to publish a new version of the same
  document; pick a fresh slug for a genuinely new one. Kebab-case, specific:
  `q2-cr-efficiency`, not `report` or `report-final-2`.
- **Labels** — repeatable tags you filter and search on. Establish a convention
  and stick to it: a `kind` label (`report`, `study`, `design-doc`, `postmortem`),
  a project label, and `auto-gen` when a tool produced it. **Always add at least
  one label** — an unlabeled artifact is hard to find in a busy catalog.

```sh
arti add q2-cr-efficiency.md \
  --slug q2-cr-efficiency \
  --title "Q2 CR Efficiency Study" \
  --label report --label cr-efficiency
```

Later, find it by label or text:

```sh
arti search "efficiency" --label report
arti ls --label cr-efficiency --type MARKDOWN
```

## Versioning

Versioning is automatic and slug-based: publish to a slug that already exists and
arti creates the next version, keeping the old ones. The URL `/s/<slug>` always
resolves to the latest version you can read; pin a specific one with
`/s/<slug>/<N>`.

```sh
# v1
arti add report.md --slug q2-cr-efficiency --title "Q2 CR Efficiency Study"
# edits later → v2, same slug
arti add report.md --slug q2-cr-efficiency --title "Q2 CR Efficiency Study"
```

List the history:

```sh
arti versions q2-cr-efficiency
```

For an append-only document (a running log, a changelog) use `arti append
--slug …` instead of re-uploading the whole body — it adds to the current
version's content and is safe to retry. Want to assert "this is brand new, don't
silently version onto an existing slug"? Add `--ensure-new` and you'll get an
error instead if the slug is taken.

## Share

`arti url <slug>` (or the **Copy link** action in the web UI) gives the canonical
link:

```
https://arti.example.com/s/q2-cr-efficiency
```

That link opens the **full-page view** — the document rendered on its own, the
way a reader wants it (markdown formatted, HTML rendered, PDF inline). Append
`/<N>` to link a specific version so a reference doesn't drift when you publish an
update.

Who can open it is controlled by access patterns. By default an artifact is
readable by everyone authenticated; narrow it with `--access` (email-glob
patterns) or lock it to yourself with `--private` at upload time.

## Review with comments

The full-page view carries a **comments overlay** so reviewers can leave inline
feedback on the rendered document without you wiring anything up — it's injected
by arti when the page is served. Share the link, collect comments in place, and
publish a new version when you've addressed them. The comment thread lives with
the artifact, so the next reader sees the discussion alongside the doc.

## Quick reference

| Task | Command |
|---|---|
| Publish a doc | `arti add file.md --slug <slug> --title "…" --label <kind>` |
| New version | re-run `arti add` with the same `--slug` |
| Append to a log | `arti append --slug <slug> entry.md` |
| Get the link | `arti url <slug>` |
| Find it again | `arti search "<text>" --label <kind>` / `arti ls --label <kind>` |
| See versions | `arti versions <slug>` |

## Related

- [Web UI guide](../guides/web-ui.md) — browsing, uploading, editing metadata, comments.
- [Command-line guide](../guides/command-line.md) — the full authoring workflow on the CLI.
- [Concepts](../overview/concepts.md) — slugs, versions, labels, scopes, access.
- [As an arti APP developer](app-developer.md) — when your page needs to be interactive.
