---
title: Concepts
order: 2
summary: The handful of ideas the rest of the docs build on — artifacts, slugs, versions, the four types, scope, labels, access, and arti's URL forms.
---

# Concepts

Here are the core ideas the rest of arti is built on. Each one is a plain concept with a rule or two worth remembering; read them once and the guides, the API, and the recipes will all make sense. The terms here are the same ones the product and the code use.

## Artifact {#artifact}

An artifact is the thing arti stores: a piece of content plus the metadata that describes it. The moment you create one it gets a permanent **UUID** that never changes, along with a content type (its MIME type), a creator, an optional [slug](#slug), a [version](#version) number, [scopes](#scope), [labels](#labels), and an [access list](#access).

Where the content actually lives depends on its size. A small text body — up to 64 KiB — is kept in the database alongside the metadata. Anything larger, and every bundle, app, or uploaded file, is stored in blob storage, with the database row pointing at it. You never have to think about which; arti picks based on the content.

## Slug {#slug}

A slug is an optional, human-readable name for an artifact — `q3-board-deck` instead of a UUID. Without one, an artifact is reachable only by its UUID. With one, it gets a friendly URL, and it becomes the name for a *line of versions*.

The rule to remember: uploading again under an existing slug never overwrites anything — it adds the next version. You don't choose the version number; arti always assigns the next one up. (A slug is unique while it's in use, so one name always points at one line of versions.)

ATTACHMENT artifacts are the exception — they're always slugless, and therefore single-version. See [Artifact types](#artifact-types).

## Version {#version}

Every upload under a slug is a new version, numbered from 1 and counting up. Versions are whole snapshots, not diffs or branches: each one is the complete content at that moment, so an old link always resolves to exactly what it captured. Reading without asking for a specific version gives you the latest; `arti versions <slug>` lists them all.

`arti append` is the one operation that *adds to* a version instead of replacing it. It reads the current body, tacks your new content onto the end, and writes the result as the next version — handy for a running log. If the slug doesn't exist yet, it starts at version 1.

## URL forms {#url-forms}

Every artifact has a canonical URL. Which form you use depends on what you want — a specific artifact, the latest under a name, or one pinned version:

| Form | Resolves to | When to use it |
|---|---|---|
| `/a/<uuid>` | the exact artifact, by UUID | Always works, including for slugless artifacts. |
| `/s/<slug>` | the **latest** version under `<slug>` | The "always current" link to share. |
| `/s/<slug>/<N>` | version `N` under `<slug>` | Pin to a specific version. |
| `/s/<slug>/v_<N>` | version `N` under `<slug>` | Older form of the above, still accepted. New URLs use the bare number. |

Both `/s/daily-notes/2` and `/s/daily-notes/v_2` resolve to version 2; a segment that isn't a real version simply falls back to the latest. `arti url <ident>` prints the canonical link for any artifact.

Files inside a PACKAGE or APP are addressable on their own, too — fetch a single entry with `arti get <slug>/<path/in/zip>` (or the matching REST route) without downloading the whole bundle.

## Artifact types {#artifact-types}

Every artifact is one of four types, and the type decides how arti stores and serves it:

| Type | Stored as | Slug / versioned | In the catalog | What it is |
|---|---|---|---|---|
| **TEXT** | inline if ≤ 64 KiB, else blob | yes | yes | markdown, HTML, or plaintext — the common case. |
| **PACKAGE** | zip + file manifest | yes | yes | a multi-file bundle; entries are fetchable one at a time. |
| **APP** | zip (same as PACKAGE) | yes | yes | a PACKAGE that also ships an `arti-app.json` manifest and gets an app bridge at serve time. |
| **ATTACHMENT** | blob only | no (slugless, single-version) | hidden for non-admins | a user-uploaded file; visible only to its creator by default. |

PACKAGE and APP are stored and served the same way — an APP is just a PACKAGE that additionally declares a tool allowlist and gets the bridge injected, letting its JavaScript call MCP tools and an LLM through arti's governed proxy with no backend of its own. See [App serving](../architecture/app-serving.md) for how that works and [Attachments](../architecture/attachments.md) for the creator-only rules.

## Scope {#scope}

Scope is a free-form label for *what an artifact belongs to* — a way to group and filter, kept separate from who can access it. arti doesn't enforce any format: a scope is just a string, and you pick the convention that fits your tool or workflow.

A few conventions are in use today:

- `a:<…>` — produced by an **agent**
- `u:<…>` — belongs to a **user** (there's no fixed format here yet; it may be formalized later)
- `app:<…>` — owned by a specific **application**, for example `app:couch`

Those are conventions, not rules — beyond them, scope is whatever you decide. An artifact can carry more than one, and scope is a first-class filter when you list or search (`--scope` on the CLI, `scope:` in search).

## Labels {#labels}

Where scope says what an artifact belongs to, labels are how you'd find it in a pile — free-form tags like `report`, `weekly`, `dashboard`, or `skill`. Add as many as you want (one `--label` per tag). When you filter by several at once, arti returns only the artifacts that carry all of them.

## Access {#access}

Every artifact has an access list of glob patterns matched against email addresses. The default, `{*}`, means anyone who can reach arti may read it. An empty list means creator-only — the rule ATTACHMENTs are locked into. Group and role permissions layer on top of this; see [Authentication & access](../architecture/auth.md).

## The four surfaces {#surfaces}

arti has one core behind four front doors. Whatever you create through one, you can read back through the others — an artifact written by an agent over MCP looks identical from the CLI, the REST API, and the web.

| Surface | Where | What it's for |
|---|---|---|
| **Web UI** | `/` | Browse the catalog, view and render artifacts, share links, manage groups and roles. See [Web UI](../guides/web-ui.md). |
| **REST API** | `/api/artifacts/*` | Scripts and services — JSON metadata, content streamed back with its MIME type. See [REST API](../reference/rest-api.md). |
| **MCP server** | `/mcp` | Agents — artifact operations exposed as MCP tools. See [MCP guide](../guides/mcp.md) and the [tool reference](../reference/mcp-tools.md). |
| **CLI** | `arti` | A terminal and shell automation. See [Command line](../guides/command-line.md). |

## Next

- **[Quickstart](quickstart.md)** — put these to work: upload and share an artifact.
- **[Data model](../architecture/data-model.md)** — the schema behind these nouns.
- **[System overview](../architecture/system-overview.md)** — how the surfaces and storage fit together.
