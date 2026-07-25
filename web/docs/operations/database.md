---
title: Database & migrations
order: 4
summary: Postgres is managed with goose migrations under db/migrations, applied automatically by an init container (arti-server migrate) on every deploy and locally via make migrate / make migrate-test; the migrations ship inside the api image so the schema is always paired with the code that expects it.
---

# Database & migrations

arti's schema is Postgres, managed by [goose](https://github.com/pressly/goose).
Migrations are plain SQL files under `db/migrations`, applied forward-only in
production. They ship *inside the api image* and run automatically on every
deploy, so the running code and the schema move together.

## Migration files

One file per migration, numbered and named: `db/migrations/NNNN_name.sql`.
Current set:

```
0001_baseline.sql              0008_oauth_obo.sql
0002_mcp_oauth.sql             0009_llm_usage.sql
0003_allowed_access.sql        0010_user_groups.sql
0004_comments.sql              0011_rbac.sql
0005_scopes_add.sql            0012_device_auth.sql
0006_idempotency_keys.sql      0013_device_token_unique_active.sql
0007_comment_author_profile.sql
```

Each file has both a `-- +goose Up` and a `-- +goose Down` section. The `Up`
section is what runs on deploy; `Down` exists for local reset and is not used in
prod (deploys are forward-only). Example header (`0012_device_auth.sql`):

```sql
-- 0012_device_auth.sql
-- RFC 8628 Device Authorization Grant for headless agents.

-- +goose Up
CREATE TABLE device_auth ( … );

-- +goose Down
DROP TABLE device_auth;
```

> `db/queries/*.sql` is a *different* thing — those are the sqlc query
> definitions that generate `gen/sqlc/`, not migrations. Editing a query means
> rerunning `make generate`, not adding a migration. See
> [Codegen](../development/codegen.md) (sqlc + oapi-codegen).

## How migrations apply in production

The api `Dockerfile` copies the migration directory into the image:

```dockerfile
COPY db/migrations /app/db/migrations
```

Deployments should run `arti-server migrate` before the main server process
starts — as a Kubernetes init container, a compose `depends_on` step, or a
release hook:

```yaml
initContainers:
  - name: migrate
    image: <your arti api image>
    command: ["/app/arti-server", "migrate"]
    # ARTI_DATABASE_URL from your database-credentials secret
```

`arti-server migrate` (`cmd/arti-server/cmd_migrate.go`) opens the DB and runs
`goose.Up` against `db/migrations` (`Dir` default `db/migrations`, resolved
relative to the image WORKDIR `/app`):

```go
goose.SetBaseFS(os.DirFS("."))
return goose.Up(db, c.Dir)
```

Because it's an *init* container, a failing migration keeps the pod in `Init`
and **halts the rollout** — the old replicas stay live until migrate succeeds.
This is the intended safety behavior: a broken migration can't take prod down,
it just blocks the new version from coming up. See the
[Runbook](runbook.md#deploy-stuck-in-init-migration-failing) for recovery.

## Local development

The dev database is Postgres in docker-compose
(`deployments/docker-compose/docker-compose.yml`), brought up by `make dev-up`:

```
make dev-up      # Postgres on :5436, MinIO on :9210 (console :9211)
```

It exposes **two databases on the same instance**: `arti_dev` (your runtime DB)
and `arti_test` (used by integration tests). Default credentials are
`postgres`/`postgres`.

### Apply migrations

```sh
make migrate        # goose up against arti_dev
make migrate-test   # goose up against arti_test
make migrate migrate-test   # both, as the quickstart does
```

These delegate to `db/Makefile`, which calls goose with the right URL:

```
DEV_URL  = postgres://postgres:postgres@localhost:5436/arti_dev?sslmode=disable
TEST_URL = postgres://postgres:postgres@localhost:5436/arti_test?sslmode=disable
```

CI applies `make migrate-test` before integration tests (against a
`postgres:16-alpine` sidecar).

### psql access

```sh
make psql                 # psql into arti_dev
make -C db psql-test      # psql into arti_test
```

In the cluster, exec into the server pod and use `ARTI_DATABASE_URL`, or connect
with credentials from the `arti-db-credentials` k8s Secret.

### Reset (destructive)

```sh
make -C db reset          # goose reset on arti_dev (runs every Down)
make -C db reset-test     # goose reset on arti_test
make dev-reset            # nuke the docker-compose volumes entirely
```

`make -C db dump-schema` writes `db/schema.sql` from `arti_dev` for inspection.

## Adding a migration safely

1. Create the next-numbered file: `db/migrations/0014_my_change.sql` with
   `-- +goose Up` and `-- +goose Down` sections. Keep the number monotonic.
2. If the change adds/edits a query, also update `db/queries/*.sql` and run
   `make generate` — CI fails if `gen/sqlc/` drifts.
3. Apply locally: `make migrate migrate-test`.
4. Run the suite: `make test` (integration tests run against `arti_test`).
5. **Write migrations to be backward-compatible with the currently-running
   code.** During a deploy the init container migrates *before* new pods start,
   while old pods are still serving the old schema expectations. Prefer additive
   changes (new nullable column, new table, new index). For drops/renames, do it
   in two deploys: first stop using the column in code, then drop it later.
6. Index-heavy migrations run synchronously in the init container and block the
   rollout while they run — be deliberate about adding large indexes on big
   tables.
7. `Down` is for local reset only; prod never runs it. To undo a prod migration,
   ship a *new* forward migration.

See [Local setup](../development/local-setup.md) for the full dev bootstrap and
[Deploying](deploying.md) for how the migrate step fits into the pipeline.
