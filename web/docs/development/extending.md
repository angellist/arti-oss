---
title: Extending arti
order: 5
summary: How to add a REST endpoint, an MCP tool, an APP MCP server, a DB migration, and a help page — with pointers to the existing patterns to copy.
---

# Extending arti

Each of these follows an existing pattern in the tree. Copy the nearest sibling
rather than inventing a shape. Read [Codebase map](codebase-map.md) first so the
file references land.

## A new REST endpoint

The REST API is hand-written — there is no spec-to-handler codegen
([Code generation](codegen.md)). Four steps:

1. **Service method.** Add the logic to the `Service` in
   `internal/artifacts/service.go` (or the relevant package). REST and MCP both
   call the same `Service`, so put business logic here, not in the handler.
2. **Handler + route.** Add an `httpXxx` handler method in
   `internal/artifacts/server.go` and register it in `Mount`
   (`internal/artifacts/server.go:764`), e.g.
   `r.Get("/api/artifacts/{id}/whatever", svc.httpWhatever)`. Match the existing
   handlers' shape: parse params, call the service, encode JSON, map errors to
   status codes.
3. **Mount point + auth.** `Mount` is already called inside the authed group in
   `cmd/arti-server/cmd_serve.go:496`, so a route added there inherits
   `RequireAuth` + upload-scope enforcement. If your route needs a *different*
   auth model (public, embed-token, app-token), mount it in the matching group
   in `cmd_serve.go` instead — see how the comments embed and apps proxy are
   mounted (`cmd_serve.go:299`, `:472`).
4. **Spec.** Hand-edit `cmd/arti-server/openapi.yaml` to add the path, params,
   and response schema. It's embedded and served at `/openapi.yaml`; keep it in
   sync with the handler. Document it in the
   [REST API reference](../reference/rest-api.md) too.

No `make generate` step is involved for REST.

## A new MCP tool

The MCP server (`internal/mcp/server.go`) is a hand-rolled JSON-RPC 2.0 server
that wraps the same `artifacts.Service`. Adding a tool is two edits in that file
plus an implementation:

1. **Declare it** in the `tools/list` response — append a tool descriptor (name,
   `Description`, `InputSchema`) to the slice built under the `case "tools/list":`
   block (`internal/mcp/server.go:81`). Copy the shape of `add_artifact` /
   `get_artifact` (`server.go:122` onward); the description is the model-facing
   doc, so write it carefully.
2. **Dispatch it** in the `tools/call` switch (`server.go:289`) — add a
   `case "your_tool": return s.toolYour(ctx, p.Arguments)`.
3. **Implement** `toolYour` as a method that unmarshals the arguments, calls the
   `Service`, and returns `toolReply(payload)`. Follow `toolGet` / `toolList`.

Keep parity with REST: a tool should call the same `Service` method the
equivalent REST handler does. See the [MCP tools reference](../reference/mcp-tools.md).

## A new APP MCP server

APP artifacts call MCP tools through the governed apps proxy
(`internal/apps/`, see [App serving](../architecture/app-serving.md)). The set of
upstream servers an app may reach is the `appServers` map built in
`cmd/arti-server/cmd_serve.go:307` and passed to `apps.New(...)`
(`cmd_serve.go:469`).

To add one, add an entry to that map keyed by the name apps will reference in
their `arti-app.json`:

```go
"linear": {
    Name:        "linear",
    ResourceURL: "https://mcp.example.com/linear/mcp",
    Auth:        "oauth",
    Scope:       "mcp:linear",
},
```

- `Auth: "none"` for an unauthenticated upstream (e.g. `arti-self`, which points
  the proxy back at arti's own `/mcp`).
- `Auth: "oauth"` routes through the OBO broker (`internal/obo/`) so the call
  carries the *viewer's* token, with per-user consent. The `ResourceURL` /
  `Scope` come from however the upstream MCP server (or MCP gateway/proxy)
  registers OAuth clients.
- `Auth: "service"` is the built-in `llm` server (`internal/llm/`), wired
  separately.

The map is overridable at runtime via the `ARTI_APP_MCP_SERVERS` env var
(merged over the defaults at `cmd_serve.go:396`) — see the
[Configuration reference](../reference/configuration.md). An app still only
reaches a server if its `arti-app.json` allowlist names it; the proxy re-checks
that allowlist on every tool call.

## A new DB migration

Migrations live in `db/migrations/` and run with goose ([Code generation](codegen.md),
[Data model](../architecture/data-model.md)).

1. **Create the file** with the next sequence number and a short slug:
   `db/migrations/0014_my_change.sql`. Numbering is strictly sequential —
   `0001_baseline.sql` … `0013_device_token_unique_active.sql` today.
2. **Write both directions** using goose pragmas:

   ```sql
   -- 0014_my_change.sql
   -- Why this change exists (one or two lines).

   -- +goose Up
   ALTER TABLE artifacts ADD COLUMN my_col TEXT;

   -- +goose Down
   ALTER TABLE artifacts DROP COLUMN my_col;
   ```

3. **Apply it** to both databases: `make migrate && make migrate-test`.
4. **Regenerate** if you also touched `db/queries/` (sqlc reads the migrations as
   its schema): `make generate`, then use the new method from
   `internal/store/pgstore`. Commit the `gen/sqlc/` diff with your migration.

`make migrate-test` is required before integration tests will pass against the
new schema ([Build & test](build-test.md#test-tiers)).

## Help docs

These help pages *are* the docs you're reading — they live in `web/docs/` and
render at `/help`. Keeping them current is part of shipping a change.

Each page is one markdown file under a section directory
(`overview/`, `architecture/`, `guides/`, `recipes/`, `reference/`,
`operations/`, `development/`). The loader (`web/lib/help-docs.ts`) reads it as:

- **Slug** = `<section>/<filename-without-.md>`, served at `/help/<slug>`.
- **Frontmatter** drives nav: `title`, `order` (position within the section),
  `summary`. Then an H1 with the visible title.

```
---
title: My new page
order: 6
summary: One concrete sentence describing the page.
---

# My new page
```

Section order and labels are the `SECTIONS` array in
`web/lib/help-docs.ts`; pages within a section sort by `order`. Cross-link with
relative markdown links (`[CLI reference](../reference/cli.md)`) — they're
rewritten to `/help/...`.

### Adding a diagram

1. Write the Mermaid source next to the page: `web/docs/<section>/<name>.mmd`.
2. Reference it from the markdown as `![alt text](./<name>.svg)` — do **not** use
   a fenced ```mermaid block for help diagrams that must work without
   JavaScript.
3. Render it: `make docs-diagrams` (needs `mmdc`) writes
   `web/public/help-diagrams/<section>/<name>.svg`. Commit both the `.mmd` and
   the `.svg`.

Markdown artifacts and help pages support fenced `mermaid` blocks in the
browser. Prefer committed SVGs for help docs: they are server-rendered and
work without JavaScript.

`make docs-check` (part of `make lint`) fails if any `.mmd` lacks a committed
`.svg`, so diagrams can't silently rot — see
[Build & test](build-test.md#help-diagrams-docs-diagrams-docs-check).
