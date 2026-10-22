---
title: Codebase map
order: 2
summary: Where everything lives — the Go packages under internal/, the two cmd/ binaries, the Next.js web app, the OpenAPI spec, the db/ and gen/ layout, and the deploy directories.
---

# Codebase map

`github.com/angellist/arti-oss` is one Go module (`go 1.25`) plus a Next.js app.
`arti-server` owns the API and reverse-proxies the web sidecar; the CLI is a
separate binary. The whole system is described conceptually in
[System overview](../architecture/system-overview.md); this page is the
concrete "where does X live, and how is it wired together" reference. When you
need to add a surface, start here and then read [Extending arti](extending.md).

```
arti/
├── cmd/
│   ├── arti-server/        # the Go server binary (serve, migrate, reindex, doctor)
│   └── arti/               # the CLI binary
├── internal/               # all server-side packages (see table below)
├── gen/sqlc/               # sqlc-generated DB code (committed)
├── db/{migrations,queries} # goose migrations + sqlc query sources
├── web/                    # Next.js 16 App Router (catalog + viewer + help)
├── angellist/              # internal deployment data (absent from the public tree)
├── deployments/            # docker-compose (local + full-app profile)
├── scripts/                # e2e-smoke.sh and friends
├── config.example.yaml     # starting point for the optional ARTI_CONFIG_FILE
├── Dockerfile              # server image (api, test targets)
└── web/Dockerfile          # web sidecar image
```

## How it's wired

`cmd/arti-server` is the single front door. There is one `chi` router
(`root := chi.NewRouter()` in `cmd_serve.go`) and **everything** hangs off it — the REST API, the MCP
JSON-RPC endpoint, the apps proxy, the OBO broker, the comments embed, the
external embed surfaces, and the reverse proxy to the Next.js app. There is no
separate API gateway; the binary you deploy is the whole back end.

**Config is read in exactly one place.** `internal/config` is the only package
that reads the environment or the optional `ARTI_CONFIG_FILE` YAML. `config.go`
holds the typed `Config` struct (with `yaml:"…"` tags), `Defaults()` in
`load.go` holds the defaults, and `bindings()` in `load.go` maps each environment
variable onto a field. Each `arti-server` subcommand calls `config.Load` with the
requirements it needs (a database, a bucket, a search endpoint) and hands each
package only its own section. If you need a new knob, add the field, its
default, and its binding there, and thread it through. The defaults and meanings
are documented in [Configuration](../reference/configuration.md); local values
live in `.env` / `.env.example`.

**Requests are grouped by auth model**, and the grouping is the security model:

| Group | Where | Auth | What's mounted |
| --- | --- | --- | --- |
| Public | top of the router in `cmd_serve.go` | none, or each handler's own token check | `/healthz`, `/openapi.yaml`, `/.well-known/*`, the `/auth/cli/*`, `/auth/device/*`, `/auth/google/login`, `/oauth/*` (MCP OAuth) flows, the comments embed (`commentsSvc.MountEmbed`), the apps proxy (`appsSvc.MountProxy`), the OBO callback, and the external embed surfaces (`embed.New(...).Mount`). These authenticate themselves with scoped/shared-secret tokens, not the session middleware. |
| Authed | the `root.Group(...)` with `authMiddleware` | `auth.RequireAuth` (Dex OIDC in prod / HS256 signer for CLI + test mode) + upload-scope enforcement + RBAC checks | the artifacts REST API (`artifacts.Mount`, `MountShareAdmin`), comments (`commentsSvc.Mount`), `admin`, notification settings, `groups`, `roles`, API keys (`keysSvc.Mount`), and the `/mcp` JSON-RPC server. |
| APP pages | the next `root.Group(...)` | `auth.RequireAuthOrRedirect` (a browser is sent to `/auth/login`) | `artifacts.MountApp` — APP artifacts served full-page at `/app/…`. |
| Share links | a `root.Group(...)` mounted only when `ARTI_SHARE_ENABLED` | the share token in the path, plus a per-IP rate limit | `svc.MountShare` — `/share/…`. |
| Everything else | `root.Handle("/*", proxy)` at the end of the route block | n/a (delegated) | a `httputil.NewSingleHostReverseProxy` to `ARTI_WEB_URL` — the Next.js sidecar. Empty `ARTI_WEB_URL` disables the proxy (API-only). |

So the request path is: ingress → `arti-server` → either a Go handler (API / MCP
/ embed / proxy surface) or, by fall-through, the Next.js app. In prod the
public OIDC-protected routes sit behind oauth2-proxy on the ingress, which
forwards `X-Auth-Request-Email` / `-Groups` headers that `IngressLoginHandler`
trusts; see [Authentication & access](../architecture/auth.md). The Next.js side
has its own `web/middleware.ts` and `web/next.config.ts`, but the authoritative
request routing for everything under `/api`, `/auth`, `/mcp`, `/oauth` is the Go
router above — the FE only ever talks to the API through it.

`cmd_serve.go` is therefore the file to read first: the route registration block
is the live index of every surface and which auth model guards it.

## `cmd/` — the two binaries

### `cmd/arti-server/`

| File | Role |
| --- | --- |
| `main.go` | Kong entry; four subcommands: `serve`, `migrate`, `reindex`, `doctor`. |
| `cmd_serve.go` | The whole server wiring — loads `internal/config`, opens the pool, the chi router, every route mount, auth middleware, the apps proxy, the OBO broker, the request logger, graceful shutdown. The map of everything. |
| `cmd_migrate.go` | In-process goose runner (alternative to `make migrate`). |
| `cmd_reindex.go` | Backfills the OpenSearch index from Postgres. |
| `cmd_doctor.go` | Validates the configuration and probes each dependency. |
| `feproxy.go`, `ratelimit.go`, `gzip_body.go`, `trailing_slash.go` | Server middleware: the FE reverse proxy and its loop guards, per-IP rate limits, gzip request bodies, trailing-slash redirects. |
| `openapi.go` + `openapi.yaml` | The OpenAPI spec, `//go:embed`-ed and served at `/openapi.yaml`. Hand-maintained — see [Code generation](codegen.md). |

`cmd_serve.go` is the file to read first. The route registration block (from
`root := chi.NewRouter()`) is the index of every API surface and which auth
model guards it.

### `cmd/arti/`

The CLI. One file per command group: `cmd_add.go`, `cmd_append.go`,
`cmd_edit.go`, `cmd_access.go`, `cmd_owner.go`, `cmd_block.go`, `cmd_map.go`,
`cmd_login.go`, `cmd_version.go`, `cmd_update.go`, `cmd_misc.go` (get/ls/search/
versions/rm/url/whoami/logout and the rest). `defaults.go` holds the
deployment's default base URL. `client.go` is the HTTP client against the REST
API; `token.go` / `selfupdate.go` handle the cached token and the
`arti update` self-updater. See the [CLI reference](../reference/cli.md) and the
[command-line guide](../guides/command-line.md).

## `internal/` — server packages

| Package | What it does |
| --- | --- |
| `internal/artifacts` | The service layer + chi REST handlers. CRUD, search, aggregates, multipart upload, content-type detection, versioning/archiving. REST and MCP both call this `Service`. `server.go` holds `Mount` (the route table) and the handlers. |
| `internal/config` | The typed runtime configuration: defaults, the optional `ARTI_CONFIG_FILE` YAML, environment overrides, and per-command requirements. The only package that reads the environment. See [Configuration](../reference/configuration.md). |
| `internal/auth` | All authentication: Dex OIDC verification and the built-in `oidc` login, the HS256 test-mode/CLI signer, the device flow, MCP OAuth, `RequireAuth`, scope enforcement, the email allowlist. See [Authentication & access](../architecture/auth.md). |
| `internal/apikeys` | Self-serve and admin-minted API keys (`arti_{scope}_…`): mint, list, revoke, and the bearer check. Only `sha256(key)` is stored. |
| `internal/credusage` | Records where each credential is used from, counted in memory and flushed on a timer; flags a credential seen somewhere new. |
| `internal/mcp` | The hand-rolled JSON-RPC 2.0 MCP server (`initialize`, `tools/list`, `tools/call`) exposing artifact ops as tools. Mounted at `/mcp`. See [MCP tools reference](../reference/mcp-tools.md). |
| `internal/mcpclient` | The *outbound* MCP client — forwards a single `tools/call` to a remote Streamable-HTTP MCP server (e.g. a Runlayer proxy). Used by the apps proxy. |
| `internal/apps` | The governed app-serving proxy for APP artifacts: authenticates sandboxed pages via scoped bearer tokens, enforces the `arti-app.json` tool allowlist on every call, resolves upstream servers, forwards with per-user OBO creds. See [App serving](../architecture/app-serving.md). |
| `internal/obo` | The OAuth 2.1 on-behalf-of token broker — arti acting as an OAuth client so an app's call carries the *viewer's* token to an upstream provider, with per-user consent. Tokens stored encrypted in Postgres. |
| `internal/llm` | The built-in non-agentic Claude completion service behind the `llm.complete` app tool — one Anthropic Messages call, model alias resolution, output cap, usage recorded to the ledger for budget gating. |
| `internal/comments` | Per-artifact-version commenting: threads with text-quote or pin anchors, open/resolved status, replies, Slack notifications, scoped embed tokens for the in-page overlay. See [Comments](../architecture/comments.md). |
| `internal/embed` | Cross-site iframe serving of artifacts on configured external surfaces (e.g. Front), authenticated by a per-surface shared secret on the public ingress. See [Embed surfaces](../architecture/front-embed.md). |
| `internal/slacknotify` | Slack DM notifications for comment and credential events — resolves email → Slack user ID (cached), fire-and-forget after the write commits. |
| `internal/notifysettings` | Decides at send time whether a notification may go to a person: their own per-category choices under one deployment-wide admin switch (Settings → Notifications). |
| `internal/groups` | User-group admin API — named collections of member emails used in access grants. |
| `internal/roles` | HTTP admin API for role/assignment management (read/write role permission sets, assign to people and groups). Gated by `MANAGE_ROLES`. |
| `internal/rbac` | The role/permission *vocabulary* — opaque permission keys (`ManageRoles`, `ManageUserGroups`, `ManageArtifacts`, `ManageSkills`, `ManageAPIKeys`, `ManageConnectors`, `AssertActor`, `ReadOrgShared`, `UseArtifacts`) and built-in roles (`ADMIN`, `USER`). Intentionally dependency-free to avoid import cycles; storage lives in `pgstore`, the HTTP surface in `roles`. |
| `internal/admin` | Admin-only operational endpoints for debugging prod without DB access (gated by `MANAGE_ARTIFACTS`), plus the App Connectors CRUD (`MANAGE_CONNECTORS`) and the document block list. |
| `internal/httperr` | The one definition of "the request failed only because the caller went away", shared by the HTTP surfaces (the 499 rule). |
| `internal/mdtext` | The server's markdown configuration, kept in step with the viewer's (`web/lib/markdown.tsx`). |
| `internal/paging` | The page bounds every list endpoint shares (default 50, max 500). |
| `internal/pkgzip` | PACKAGE zip handling — builds the per-entry manifest (path/size/SHA256) on upload, reads single entries on fetch, detects the entry point, skips macOS metadata junk. |
| `internal/store/pgstore` | The Postgres read/write surface. Composes the `gen/sqlc` queries with a `blob.Store`, applies the inline-vs-S3 policy (TEXT ≤ 64 KiB inline; everything else an S3 blob with a `blob_ref`), and resolves access control (groups, roles, negations). See [Data model](../architecture/data-model.md). |
| `internal/store/blob` | The `blob.Store` interface + an S3 implementation (`minio-go`, for AWS S3 or RustFS) and an in-memory one for tests. Bytes only; metadata lives in Postgres. |
| `internal/store/opensearch` | The optional OpenSearch client: index setup (`EnsureIndex`), sync-on-write indexing, full-text search. A nil client means "use the Postgres fallback". See [OpenSearch](../operations/opensearch.md). |

## `web/` — the Next.js sidecar

App Router, TypeScript, Next 16. `npm run dev` to develop ([Local setup](local-setup.md)).

```
web/
├── app/                    # routes
│   ├── page.tsx            # / — the catalog
│   ├── a/[uuid]/           # /a/<uuid> — view by UUID
│   ├── s/[slug]/           # /s/<slug>[/<ver>] — view by slug + version
│   ├── browse/             # /browse — browse all, with content-type filters
│   ├── apps/               # /apps — the apps portal
│   ├── new/[kind]/         # /new/<kind> — create a document in the browser
│   ├── diagrams/new/       # /diagrams/new
│   ├── admin/blocked/      # /admin/blocked/<ident> — admin review of a blocked document
│   ├── login/              # /login
│   ├── settings/           # /settings — appearance, notifications, keys, users,
│   │                       # roles, groups, connectors, blocks, archived
│   ├── roles/ groups/ archived/  # redirects to their /settings pages
│   └── help/               # /help and /help/[...slug] — these docs
├── components/             # ArtifactViewer, CatalogTable, UploadModal,
│                           # CommentsLayer, RolesManager, GroupsManager, SideNav…
├── lib/                    # arti.ts (client SDK), commentsOverlay.ts,
│                           # markdown.tsx, help-docs.ts, help-links.ts,
│                           # types.ts, viewer.ts, *-context.tsx…
├── docs/                   # the /help corpus (this directory)
├── public/                 # static assets, incl. rendered help-diagrams/
├── embed/                  # comments-embed.ts (bundled to public/ at build)
├── middleware.ts           # FE-side middleware
└── next.config.ts          # Next config (the API/auth routing is server-side)
```

Notable modules: `lib/arti.ts` is the FE's typed client against the REST API;
`lib/commentsOverlay.ts` is the comment-rendering engine; `lib/mentionMenu.ts` is the
composer's `@` typeahead (and the twin of `internal/comments/mentions.go`, which
decides who a mention actually notifies); `components/CommentsLayer.tsx` mounts it; `lib/help-docs.ts` loads and orders these help pages from frontmatter.
The web UI is documented in the [Web UI guide](../guides/web-ui.md).

## `api/openapi/` — the spec, actually elsewhere

There is **no** `api/openapi/` directory. The OpenAPI 3.0 spec lives at
`cmd/arti-server/openapi.yaml`, embedded with `//go:embed` (`cmd/arti-server/openapi.go`)
and served at `/openapi.yaml`. It is hand-maintained, not generated — see
[Code generation](codegen.md). The [REST API reference](../reference/rest-api.md)
documents the endpoints.

## `db/` and `gen/`

```
db/
├── migrations/   # 0001_baseline.sql … 0042_read_org_shared.sql (goose)
├── queries/      # api_keys, app_settings, artifacts, comments, credential_usage,
│                 # device_auth, idempotency, map_entries, mcp_oauth,
│                 # share_links (.sql; sqlc sources)
└── Makefile      # migrate, migrate-test, reset, dump-schema, psql
gen/sqlc/          # sqlc output (committed): *.sql.go, models.go, db.go
sqlc.yaml          # sqlc config: queries=db/queries, schema=db/migrations, out=gen/sqlc
```

Migrations are the schema source of truth; sqlc reads them plus `db/queries` to
generate `gen/sqlc`. See [Code generation](codegen.md) and [Data model](../architecture/data-model.md).

## Deploy + ops directories

| Path | What |
| --- | --- |
| `angellist/` | Everything specific to the AngelList deployment — k8s manifests, infrastructure code, encrypted per-env config. Application code never imports from it, the tree builds with it deleted, and it is absent from the public repository. |
| `deployments/docker-compose/` | Local Postgres + RustFS (+ Dex, OpenSearch), and the full-app evaluation profile. |
| `Dockerfile` / `web/Dockerfile` | Server image (targets `api`, `test`) and the web sidecar image. |

For how this all deploys, see the operations section: [Deploying](../operations/deploying.md).

## See also

The conceptual architecture pages explain *why* each piece exists and how the
flows work end to end; this map is the *where*. Read them together:

- [System overview](../architecture/system-overview.md) — the big picture and request flow.
- [Authentication & access](../architecture/auth.md) — the auth models the route groups above enforce.
- [Data model](../architecture/data-model.md) — what `pgstore` + `blob` persist and the inline-vs-S3 policy.
- [App serving](../architecture/app-serving.md) · [Comments](../architecture/comments.md) · [Embed surfaces](../architecture/front-embed.md) — the non-REST surfaces.
- [Extending arti](extending.md) — the step-by-step for adding a new surface, package, or migration.
