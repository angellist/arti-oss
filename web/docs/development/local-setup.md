---
title: Local setup
order: 1
summary: Get arti-server, the Next.js web sidecar, and the CLI running locally against a docker-compose Postgres and MinIO, with auth disabled.
---

# Local setup

Everything runs on your machine: `arti-server` (Go) on `:8095`, the Next.js
sidecar on `:3031`, Postgres on `:5436`, and MinIO on `:9210`. Auth is off by
default in dev — every write is attributed to `ARTI_LOCAL_EMAIL`.

## Prerequisites

| Tool | Version | Why |
| --- | --- | --- |
| Go | 1.25+ | `go.mod` declares `go 1.25.7`. Builds both binaries. |
| Docker | any recent | Runs the Postgres + MinIO compose stack. |
| Node | 20+ | The web sidecar (`node:20-alpine` in `web/Dockerfile`). |
| `goose` | latest | DB migrations (`db/Makefile`). |
| `sqlc` | latest | Regenerates `gen/sqlc` from SQL. |
| `oapi-codegen` | latest | Checked by `make setup`; see the [codegen](codegen.md) caveat — the OpenAPI spec is hand-maintained, not generated. |

Install the Go tools:

```sh
go install github.com/pressly/goose/v3/cmd/goose@latest
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest        # or: brew install sqlc
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
```

`make setup` verifies `sqlc`, `goose`, and `oapi-codegen` are on `PATH`, points
git at the tracked hooks (`make hooks`), and runs `go mod tidy`:

```sh
make setup
```

`make hooks` is the part that matters even on a fresh clone: it sets
`core.hooksPath` to `.githooks/`, so the pre-push gate runs before any push to
`main`. See [Build & test](build-test.md#the-pre-push-hook).

## Bring up Postgres + MinIO

```sh
make dev-up        # Postgres :5436, MinIO :9210 (console :9211)
```

This runs `deployments/docker-compose/docker-compose.yml`: a `postgres:16-alpine`
(DB `arti_dev`, user/pass `postgres`/`postgres`) and `minio/minio` with a
`minio-init` sidecar that creates the `arti-dev` and `arti-test` buckets. Tear
down with `make dev-down`; nuke the volumes with `make dev-reset`.

## Apply migrations

```sh
make migrate         # → arti_dev
make migrate-test    # → arti_test  (needed for integration tests)
```

Both shell out to `goose -dir migrations` against `db/migrations/`. The test
database is a separate DB on the same Postgres instance; create it once if it's
missing (the compose stack only auto-creates `arti_dev`):

```sh
make psql -C db   # then: CREATE DATABASE arti_test;
```

## Configure the environment

Copy the example env and source it before running the server:

```sh
cp .env.example .env
```

`.env.example` ships dev-safe defaults — read it for the full key list; the load-bearing ones:

| Key | Dev value | Notes |
| --- | --- | --- |
| `ARTI_ADDR` | `:8095` | Where the Go binary listens (`:8090` collides with couch on the dev box). |
| `ARTI_BASE_URL` | `http://localhost:3031` | Canonical hostname stamped into emitted URLs, and the CLI's default endpoint. Points at the Next dev server, which forwards `/api/*` + `/auth/*` back to `:8095` (`web/middleware.ts`). |
| `ARTI_DATABASE_URL` | `postgres://postgres:postgres@localhost:5436/arti_dev?sslmode=disable` | Dev Postgres. |
| `S3_ENDPOINT` | `http://localhost:9210` | MinIO. Unset in prod to hit AWS S3. |
| `S3_BUCKET` | `arti-dev` | Created by the compose `minio-init` sidecar. |
| `ARTI_AUTH_DISABLED` | `true` | Skip auth entirely — see below. |
| `ARTI_LOCAL_EMAIL` | `local@example.com` | Creator attributed to every write when auth is disabled. |

For the full config surface (Dex, scopes, budgets, app MCP servers), see the
[Configuration reference](../reference/configuration.md).

## Build and run

```sh
make build           # builds bin/arti-server and bin/arti
# or individually:
make build-server
make build-cli
```

Run the server (sourcing `.env` first so it picks up the dev config):

```sh
set -a && source .env && set +a
./bin/arti-server serve
# shortcut: `make server` builds then runs serve
```

`arti-server` has two subcommands: `serve` (above) and `migrate` (an in-process
goose runner, an alternative to `make migrate`). See `cmd/arti-server/main.go`.

Check it's up:

```sh
curl -s localhost:8095/healthz        # 204
curl -s localhost:8095/openapi.yaml   # the embedded spec
```

## Run the web sidecar

```sh
cd web
npm install
NEXT_PUBLIC_ARTI_AUTH_DISABLED=true PORT=3031 npm run dev
```

`npm run dev` is `next dev` (Next 16). Setting `NEXT_PUBLIC_ARTI_AUTH_DISABLED=true`
matches the server's `ARTI_AUTH_DISABLED=true` so the catalog page renders
instead of redirecting to `/login`. `web/middleware.ts` forwards `/api/*` and
`/auth/*` from the Next dev server back to `arti-server` on `:8095`, so visiting
`http://localhost:3031/` gives you a working catalog + viewer talking to the Go
API.

## Auth-disabled testing

The pair of `ARTI_AUTH_DISABLED=true` (server) + `NEXT_PUBLIC_ARTI_AUTH_DISABLED=true`
(FE) is the standard local loop. In this mode:

- No Dex, no Google, no `arti login` needed.
- Every request is attributed to `ARTI_LOCAL_EMAIL`.
- A request may override its identity with the `X-Arti-Local-Email` header — handy
  for emulating a second person (e.g. testing comment notifications) without a
  restart.

**Production deployments MUST leave both unset.** See
[Authentication & access](../architecture/auth.md) for the real auth path.

Point the CLI at your local server with either the env var or the flag (the flag
wins):

```sh
ARTI_BASE_URL=http://localhost:8095 ./bin/arti ls
./bin/arti --base-url http://localhost:8095 ls
```

To mint a real token without Google, run the server with `ARTI_TEST_MODE=true`
(it mounts `POST /auth/test`) and use `arti login --email you@example.com`.

## Verify end to end

```sh
./scripts/e2e-smoke.sh
```

Boots a fresh artifact and walks 27 assertions across REST, CLI, MCP, and the FE.
`make test-e2e` runs the Go e2e suite under `tests/e2e/`. See
[Build & test](build-test.md) for the full test matrix.

## Where to go next

- [Codebase map](codebase-map.md) — where everything lives.
- [Build & test](build-test.md) — the test targets and the pre-push gate.
- [Code generation](codegen.md) — sqlc, and why the OpenAPI spec is *not* generated.
- [Extending arti](extending.md) — adding an endpoint, an MCP tool, an app server, a migration.
