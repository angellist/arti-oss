# arti — notes for coding agents

<!-- This file ships in the PUBLIC repository (replacing the internal
     AGENTS.md at export time). It is written for the operator's coding
     agent — Claude Code, Cursor, Codex, or similar — driving a setup. -->

arti is a self-hostable artifact service: a Go server (`arti-server`, REST +
MCP + OAuth) with a Next.js UI (`web/`), Postgres for metadata, and any
S3-compatible store for content. Humans browse and comment; agents upload
and read via CLI, REST, or MCP.

## Setting arti up

Three supported profiles — pick by what the operator already has:

1. **Local evaluation** (nothing needed): full app, auth off —
   `docker compose -f deployments/docker-compose/docker-compose.yml -f deployments/docker-compose/docker-compose.app.yml up -d --build`,
   then browse http://localhost:8090.
2. **Single box**: the same compose services plus a chosen auth mode and the
   operator's TLS proxy in front. Persist the `postgres` and `minio` volumes.
3. **Retrofit** (existing Postgres / S3-compatible store / IdP): follow
   `web/docs/guides/self-hosting.md` — each dependency maps to one config
   section and is verified independently.

## The configuration model

- Resolution order: built-in defaults → optional YAML file named by
  `ARTI_CONFIG_FILE` (start from `config.example.yaml`) → environment
  variables. Full reference: `web/docs/reference/configuration.md`.
- Secrets (DSN, keys, tokens) are **environment-only**; putting one in the
  YAML file fails startup as an unknown key. Any typo'd key also fails
  startup — trust the error text, it names the field.
- Required: `ARTI_DATABASE_URL`, `S3_BUCKET`. Defaults are fail-closed:
  empty domain allowlist admits nobody; no default admins.
- Login: `ARTI_AUTH_MODE=oidc` (arti runs the OIDC authorization-code flow
  itself; register a client at the IdP with redirect URI
  `<ARTI_BASE_URL>/auth/callback`), `proxy` (trusted reverse-proxy headers),
  or `disabled` (local only).

## Your main tool: `arti-server doctor`

Run it after every configuration change. It checks config parse, signing-key
strength, base-URL/cookie coherence, database + migration state, the object
store (write/read/delete probe), and issuer discovery — each failure names
the exact variable and, where possible, the fix command. Iterate until it
prints `all checks passed`, then start the server.

- `arti-server doctor --config-only` — no network, config sanity only.
- `arti-server doctor --print-effective-config` — the resolved configuration
  (secret fields structurally omitted); use it whenever "what is it actually
  using?" comes up.
- `arti-server migrate` — apply database migrations (doctor will tell you).

## Verifying a change works

`make dev-up` starts local Postgres/MinIO/Dex; `make lint` and `make test`
are the repo gates; `web/` tests run with `cd web && npm test`. A local Dex
issuer ships in the compose stack (static user `admin@example.com` /
`password`) for exercising `oidc` mode end to end.
