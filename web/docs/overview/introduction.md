---
title: Introduction
order: 1
summary: arti is AngelList's artifact service — versioned text, multi-file PACKAGE bundles, and interactive APP artifacts, reachable from a web UI, REST API, MCP server, and CLI over one Postgres table plus S3 blob storage.
---

# Introduction

arti stores **artifacts**: a blob of content plus metadata, addressable by a stable URL, versioned automatically, and reachable from four surfaces that all sit on the same service layer. An artifact is whatever you need to keep and share — a markdown report, a rendered HTML dashboard, a zipped skill bundle, an interactive single-page app, or a raw file attachment.

Almost anything you produce and want to keep can be an artifact: a generated report or dataset, a rendered HTML dashboard, an agent's run log or conversation transcript, an incident write-up, a skill definition, a design doc, or a scratch result you intend to iterate on. arti gives each one a canonical URL, an automatic version history, and uniform access control — whether it was created by a person in a browser or an agent over MCP — so the link keeps working and you can always retrieve an earlier version.

## What an artifact can be

Four types, all rows in one table:

- **TEXT** — markdown, HTML, or plaintext. The common case. Bodies ≤ 64 KiB live inline in Postgres; larger bodies spill to S3. Rendered in the viewer with the right chrome for its MIME type.
- **PACKAGE** — a multi-file bundle. Upload a directory; the CLI zips it deterministically and the server records a file manifest. Individual entries are fetchable without downloading the whole zip.
- **APP** — a PACKAGE that also ships an `arti-app.json` manifest declaring a tool allowlist. arti injects an app bridge at serve time so the app's JavaScript can call MCP tools (and an LLM) through arti's governed proxy — no backend, no API keys of its own. See [App serving](../architecture/app-serving.md).
- **ATTACHMENT** — a user-uploaded binary (a chat attachment, a PDF). Always S3-backed, never slugged or versioned, creator-only by default, hidden from the catalog for non-admins. See [Attachments](../architecture/attachments.md).

The exact type constants and rules live in `internal/store/pgstore/store.go`. Read [Concepts](concepts.md) for crisp definitions of every noun.

## The four surfaces

Every operation is available on every surface; the service layer in `internal/artifacts` is the single source of truth, so a thing written over MCP reads back identically over REST, the CLI, and the web.

| Surface | Endpoint | For |
|---|---|---|
| **Web UI** | `https://arti.example.com/` | Browse the catalog, view/render artifacts, share links, manage groups and roles. See [Web UI](../guides/web-ui.md). |
| **REST API** | `/api/artifacts/*` | Scripts and services. JSON in, content streamed back with its original MIME type. See [REST API](../reference/rest-api.md). |
| **MCP server** | `/mcp` (JSON-RPC 2.0) | Agents. The same operations exposed as MCP tools (`add_artifact`, `get_artifact`, `search_artifacts`, …). See [MCP guide](../guides/mcp.md). |
| **CLI** | `arti` (single static binary) | Humans at a terminal and shell automation. See [Command line](../guides/command-line.md). |

## Backed by one table plus blob storage

- **Postgres** holds every artifact row: UUID, slug, version, type, content type, scope, labels, access list, and a schema-less JSONB `metadata` column for type-specific extras (e.g. a PACKAGE's entry manifest). TEXT bodies ≤ 64 KiB are stored inline in the same row.
- **S3-compatible blob storage** (AWS S3 in prod, MinIO locally) holds everything else: large TEXT bodies, all PACKAGE/APP zips, and all ATTACHMENT files. A `blob_ref` on the row points at the object.

That is the whole storage model. See [Data model](../architecture/data-model.md) for the column-level detail and [System overview](../architecture/system-overview.md) for how the pieces wire together.

## When to use arti

Reach for arti when you have an **output that needs a durable, shareable, access-controlled link** — especially one produced programmatically:

- An agent or job that generates a report, log, dataset, or transcript and needs to hand back a link a human can open.
- A canonical, versioned **record** — a skill definition, a run log, an incident write-up. arti is a fine source of truth for these; each save is a new version, with the whole history preserved.
- A human-readable HTML dashboard or one-pager you want to share without standing up a service.
- A self-contained interactive tool (an APP) that needs to call MCP tools or an LLM without you shipping a backend or handing it credentials.
- **Scratch or work-in-progress state** you'll iterate on — saving repeatedly just stacks up versions, so you can keep many iterations and roll back to any of them.
- A skill bundle or any multi-file artifact you want versioned and fetchable entry-by-entry.

Reach for something else when:

- **You need continuous, fine-grained editing** — a document a team edits line-by-line throughout the day belongs in Notion or a Google Doc. arti has no editor and versions whole snapshots, not individual edits (it's happy to hold many iterations of the same thing, just not to edit them in place).
- **You need a code repository** — use git/GitHub. arti versions are append-only snapshots, not branches or diffs.
- **You need a long-lived structured record other systems join against** — that belongs in a real schema, not a JSONB metadata blob.

## Where to go next

- **[Concepts](concepts.md)** — the nouns: artifact, slug, version, type, scope, labels, and the URL forms.
- **[Quickstart](quickstart.md)** — upload and share your first artifact in five minutes, web and CLI.
- **Recipes by role** — task-oriented walkthroughs:
  - [For agents](../recipes/agent.md) — writing artifacts over MCP.
  - [For API consumers](../recipes/api-consumer.md) — driving arti from scripts and services.
  - [For app developers](../recipes/app-developer.md) — building interactive APP artifacts.
  - [For content authors](../recipes/content-author.md) — publishing reports and docs.
