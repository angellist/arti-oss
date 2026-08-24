---
title: Searching the catalog
order: 2
summary: Open the search bar (Search item, the / shortcut, or type-to-focus) and the full free-text + field:value operator grammar — phrases, booleans, wildcards, slug/label/scope/creator/type filters, negation — with examples.
---

# Searching the catalog

Search filters the catalog from a **reveal-on-demand bar** at the top of the
listing. Free text and `field:value` operators mix freely in one box; press
**Enter** to run the query. The result is an ordinary catalog view, so the URL
is shareable and the [version / archived toggles](#version-and-archived-toggles)
apply to it.

## Opening search

The box isn't shown until you ask for it, so the default listing stays clean.
Three ways to open it:

- Click **⌕ Search** in the left rail.
- Press `/` anywhere on the catalog.
- Start typing while the bar is already open — focus jumps to the box.

Press **Enter** to search. **✕ close** (or clearing the query) collapses the bar
and returns to the plain listing. Opening a search from a rail chip — a label,
scope, or the `👤 Owned by Me` toggle — reveals the bar too, pre-filled with the
matching token.

## Free-text query syntax

Plain words match across **title, description, and content**. Multiple terms are
ANDed by default.

| Syntax | Matches | Example |
|---|---|---|
| `word word` | all terms (AND) | `vr deployment` |
| `"exact phrase"` | the phrase verbatim | `"vr deployment"` |
| `field:term` | one field — `title`, `description`, or `content` | `title:deployment` |
| `field:"a phrase"` | a phrase within a field | `title:"vr deployment"` |
| `a OR b` · `a AND b` · `NOT a` | boolean operators | `raft OR kafka` |
| `( … )` | grouping | `(raft OR kafka) AND vacuum` |
| `stem*` | prefix wildcard | `deploy*` |

## Structured filters

These match **metadata exactly** (not full text). Repeat or combine them; prefix
any with `-` to exclude.

| Filter | Matches | Example |
|---|---|---|
| `slug:` | exact slug, or a glob with `*` | `slug:smoke-test` · `slug:arti*` |
| `label:` | a label — repeatable (ANDed), globs allowed | `label:memory` · `label:mem*` |
| `scope:` | a scope (the value may itself contain `:`) | `scope:topic:funds` |
| `creator:` | the uploader's email | `creator:alice@example.com` |
| `type:` | artifact type | `type:TEXT` · `type:PACKAGE` · `type:APP` · `type:ATTACHMENT` |
| `content_type:` | exact MIME content type | `content_type:text/markdown` |
| `-<filter>` | exclude (negate) — `label` / `scope` / `creator` / `type` | `-label:archived` |

> Typing `slug:foo` is a **search** (it keeps the toggles and collapses to the
> latest version per slug). Clicking a slug in the results is a **drill-in** — a
> dedicated view of that one slug's full version history.

## Combining

Everything composes in the single box:

```text
title:"release notes" label:weekly -label:archived
(raft OR kafka) AND vacuum type:TEXT
slug:pr-review-knowledge* creator:alice@example.com
```

## Excluding an artifact's body from search

An artifact labelled **`index:skip-fulltext`** is indexed by metadata only. It
stays findable by `title:`, `description:`, `label:`, slug and creator, and it
still appears in the catalog — but its **body never enters the free-text index**,
so a plain word or `content:` query can't match on its contents.

This is for machine-state artifacts: bodies that are large, rewritten on a
schedule, and meaningless as a search hit. The skill publisher's state doc is the
motivating case — a few hundred KB of sha256 hashes and UUIDs, a new version
every day, where searching a bare hash used to return the state file.

The label is read at **index time**, so it applies from the next time the
artifact is indexed: editing labels re-indexes that version immediately, and
`arti-server reindex` re-applies it across every stored version. Labels are
per-version, so a slug with history needs the label on each version you want
purged. Removing the label puts the body back on the next index.

## Version and archived toggles

Two checkboxes sit under the search box and apply to whatever is listed —
search results or the full catalog:

- **latest version only** (on by default) collapses each slug to its newest
  version. Turn it off to list every matching version.
- **show archived** includes soft-deleted (archived) versions, shown grayed out.

Both are reflected in the URL, so a search *and* its toggles are shareable.

## See also

- [Using the web UI](web-ui.md) — the rest of the catalog, viewer, and upload.
- [Concepts](../overview/concepts.md) — slugs, versions, scopes, labels, types.
- [Command line](command-line.md) and [Using the API](api.md) — the same query
  grammar, from a shell or programmatically.
