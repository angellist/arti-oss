---
title: System overview
order: 1
summary: arti-server (Go) is the single front door — it owns auth, the REST + MCP API, the apps proxy, the OBO broker and embed surfaces, stores metadata in Postgres and bytes in S3, and reverse-proxies a Next.js web sidecar for everything else.
---

# System overview

arti is an artifact store: a place to upload a document, a zip, an interactive app,
or a file, get back a stable URL, and version, search, share, and comment on it.
Everything is fronted by one Go binary, `arti-server`.

## Components

- **arti-server** — the Go front door. Terminates auth and serves the REST API,
  the MCP server, the apps proxy, the OBO broker, embed surfaces, and the
  artifact/app/file serving routes. Anything it doesn't own it reverse-proxies to
  the web sidecar. It is a single process: all the API surfaces below are mounted
  on the same router, with no separate API gateway.
- **Web sidecar** — a Next.js app: the catalog UI, the artifact viewer, the upload
  modal. It renders the browser experience; it is *not* the API.
- **Postgres** — source of truth for artifact metadata, comments, OAuth/device
  tokens, groups, RBAC, and the LLM usage ledger. Small TEXT artifacts also live
  inline here.
- **S3 / MinIO** — bytes. Every PACKAGE/APP/ATTACHMENT and any TEXT artifact over
  the inline cap is stored as a blob, with the Postgres row holding a pointer to
  it. Real S3 in prod, MinIO in dev.
- **CLI (`arti`)** — a Go binary for uploading/fetching from a terminal or a
  headless agent. Authenticates via browser login or the device flow.

## The request path

Two ingresses sit in front of the pod, and the router splits requests into two
worlds: the authed API group, and everything-else proxied to the web sidecar.

![arti system architecture](./system-overview.svg)

### Ingress

- **ProtectedIngress** runs Google OAuth via oauth2-proxy → Dex. By the time a
  request reaches the pod it already carries trusted `X-Auth-Request-Email` /
  `-Groups` headers (see [Authentication & access](auth.md)). This is the path for
  humans in a browser and for normal API callers.
- **PublicIngress** has no SSO. A deliberate allowlist of routes is mounted on it
  — the device-flow endpoints, the comments embed API, the apps proxy, and embed
  surfaces — each of which does its *own* token- or secret-based auth. These exist
  precisely because their callers (sandboxed pages, headless agents, cross-site
  iframes) cannot send the session cookie.

### Inside arti-server

The router registers routes in three tiers plus a fallback:

1. **Public routes** — health, the OpenAPI spec, `/.well-known/*`, and the
   CLI/device/MCP-OAuth/OBO-callback endpoints.
2. **Public-but-self-authed groups** — the comments embed API, the apps proxy, and
   embed surfaces. Each verifies its own scoped token or shared secret and sets
   CORS; none relies on the session cookie.
3. **The authed group** — guarded by the auth middleware plus upload-scope
   enforcement. This is where the real API lives: the REST CRUD, the comments API,
   admin/groups/roles, and the MCP server at `/mcp`.
4. **The FE reverse proxy** — anything unmatched is forwarded to the web sidecar.
   With no sidecar URL configured, there is no proxy (API-only).

So the same origin serves the API and the web UI: the catalog page is the
sidecar; `POST /api/artifacts` is arti-server; `/mcp` is arti-server; `/app/...`
and `/embed/...` are arti-server.

## The API surfaces

All of these are arti-server, distinguished by mount point and auth model:

- **REST API** — CRUD on artifacts, search, aggregates, comments. Session- or
  bearer-authed. A hand-maintained OpenAPI spec is served at `/openapi.yaml`
  (note it lags the code). See [Data model](data-model.md) and the
  [REST API reference](../reference/rest-api.md).
- **MCP server** — a minimal JSON-RPC 2.0 MCP server mounted at `/mcp` inside the
  authed group, exposing artifact operations as tools. Same auth as REST.
- **Apps proxy** — the governed back end for APP artifacts: a sandboxed app's JS
  calls MCP tools through a scoped-token proxy that re-checks the app's declared
  tool allowlist on every call. See [App serving](app-serving.md).
- **OBO broker** — arti acting as an OAuth client so an app's tool call can carry
  the *viewer's* token to an upstream MCP server (Notion, Slack, Linear, or an
  MCP gateway in front of them), with per-user consent.
- **Embed surfaces** — serve any artifact full-page into a cross-site iframe,
  authenticated by a per-surface shared secret. See [Embed surfaces](front-embed.md).

## Where to go next

- [Data model](data-model.md) — the artifact table, versioning, slugs, the
  metadata-in-Postgres / bytes-in-S3 split, PACKAGE manifests.
- [Authentication & access](auth.md) — OAuth, the session cookie, the CLI +
  device flows, scoped tokens.
- [App serving](app-serving.md), [Attachments](attachments.md),
  [Embed surfaces](front-embed.md), [Comments](comments.md) — the specialized
  serving surfaces.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
