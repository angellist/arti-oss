# arti

**An AI-first, human-friendly artifact workspace.** Agents produce more
documents than people can chase through chat threads and automated
pipelines — arti gives that output a durable home. The same store that
holds reports and docs for people to read also holds what agents run on:
**prompts, skills, memories, and knowledge**. Agents and scripts publish
and operate on artifacts over **MCP, REST, and the CLI**; humans get a
fast web workspace to **browse, read, comment, and share**. Storage like
this is a mundane primitive of the agent-harness ecosystem — but a
critical one, and arti is a solid, self-contained answer to it.

Under the surfaces is a deliberately simple **base layer for authoring,
storage, and retrieval** — versioned artifacts named by stable slugs and
URLs, full-text search (optional OpenSearch, Postgres fallback), and a
clean split of compute and state: the API server and web frontend are
**stateless and scale horizontally**, while all state lives in **Postgres
and any S3-compatible object store**. Self-hostable end to end, and built
to take on new access paths (a vector index for semantic retrieval, say)
without reworking the core.

- **Versioned by design** — a stable `slug` names an artifact; re-uploads
  auto-version. Full version history, and a side-by-side **compare view**
  with word-level diff highlights between any two versions.
- **Organize and find** — labels, scopes, and full-text **search** (optional
  OpenSearch, Postgres fallback) with `field:value` filters for type,
  creator, label, and slug globs.
- **A real reading experience** — rendered markdown/HTML/JSON, inline
  **comments** anchored to the text, and per-document **view controls**
  (width, text size, raw source). A **full-page view** (`?v=full`) serves
  any artifact chrome-free at a shareable URL.
- **Beyond single files** — multi-file `PACKAGE` bundles with a file-tree
  viewer, and sandboxed `APP` mini-sites that can call MCP tools through a
  governed proxy.
- **Write or draw, don't just upload** — **New** in the rail creates an
  artifact without a file: `/new/text` is a **markdown editor** with a
  formatting toolbar and a live preview rendered by the same component the
  viewer uses, and `/new/diagram` is a drag-and-drop **diagram** canvas for
  flowcharts and boxes-and-arrows. Both save as ordinary artifacts — only the
  content type differs — so either gets slugs, versions, diffs, comments and
  access control for free, and a diagram also exports to a standalone SVG.
- **One service, four surfaces** — every operation is available over REST,
  MCP, the CLI, and the web UI; fetch by UUID or slug (`/a/{uuid}`,
  `/s/{slug}`, `/s/{slug}/v_{N}`).

## This repository is a release-only snapshot

This repository is published in **periodic batches** from AngelList's
internal source tree. External pull requests are **not reviewed or
accepted**, and public issues are disabled — there is no support commitment.
You are welcome to **fork it** and maintain your own changes; see
[CONTRIBUTING.md](CONTRIBUTING.md). Vulnerabilities go through the private
path in [SECURITY.md](SECURITY.md).

## Quick start (local evaluation)

One command brings up Postgres, MinIO, and arti with authentication disabled
and migrations applied:

```sh
docker compose -f deployments/docker-compose/docker-compose.yml \
               -f deployments/docker-compose/docker-compose.app.yml up -d --build
```

Then open <http://localhost:8090>. Do not expose this profile beyond your
machine — every action runs as a local identity. (Port `8090` taken? Add a
third `-f` compose override remapping it — use `ports: !override`, since
Compose otherwise appends and keeps the colliding mapping; see the
self-hosting guide.)

## Self-hosting

Three supported profiles, documented as step-by-step checklists (with a
verification command after every step) in
[`web/docs/guides/self-hosting.md`](web/docs/guides/self-hosting.md):

1. **Local evaluation** — the quick start above.
2. **Single box** — the compose stack plus a real auth mode and your TLS
   proxy (Caddy, Traefik, nginx) in front; persistent volumes and backups.
3. **Retrofit** — you already run Postgres, an S3-compatible store, and an
   IdP; arti slots in. Each dependency maps to one configuration section and
   is verified independently.

### Set it up with your AI agent

This repository is built to be **driven by a coding agent** — Claude Code,
Cursor, Codex, or similar. Clone it, open your agent at the repo root, and
ask it to do the setup. A prompt like:

> Set up arti from this repo. Read `AGENTS.md` first. I want **[local
> evaluation / a single-box deployment / a retrofit into my environment]**.
> My infrastructure: Postgres at **[DSN / "none, use the compose one"]**,
> object store **[AWS S3 / MinIO / R2 / B2 / "none"]**, login via **[my IdP
> (Okta, Google, Entra, Keycloak, …) / my authenticating proxy / disabled,
> local only]**. Configure it, run `arti-server doctor` until it passes,
> then verify a login and an upload round-trip.

The repo meets the agent halfway:

- A root [`AGENTS.md`](AGENTS.md) briefs it on the three profiles, the
  configuration model, and the verification loop.
- **Each infrastructure choice maps to one configuration section** —
  Postgres (`ARTI_DATABASE_URL`), any S3-compatible store (static keys or
  the ambient IAM chain), and one of the three auth modes — and each is
  verified independently by `arti-server doctor`, so the agent can retrofit
  one dependency at a time.
- Every configuration error **names the key at fault**, and the self-hosting
  docs are checklists with a verification command after every step — an
  agent (or you) always knows exactly where the setup stands.

### Your main tool: `arti-server doctor`

```sh
arti-server doctor                          # config, DB, object store, issuer — end to end
arti-server doctor --config-only            # no network, config sanity only
arti-server doctor --print-effective-config # resolved config, secrets omitted
```

Run it after every configuration change and iterate until it passes.

## Configuration

Resolution order: built-in safe defaults → optional YAML file named by
`ARTI_CONFIG_FILE` (see [`config.example.yaml`](config.example.yaml)) →
environment variables. Secrets are environment-only by design. Full
reference: [`web/docs/reference/configuration.md`](web/docs/reference/configuration.md).

Defaults are fail-closed: an empty domain allowlist admits nobody, there are
no default administrators, and weak or placeholder signing keys refuse to
boot when auth is enabled.

**Authentication** (`ARTI_AUTH_MODE`):

| Mode | What it is |
|---|---|
| `oidc` (recommended) | arti runs the OIDC authorization-code flow itself. Works with any compliant IdP — Okta, Auth0, Entra ID, Google, Keycloak, Dex, authentik. No IdP? Run Dex in one container (the compose stack ships one). |
| `proxy` | Trust identity headers from your authenticating reverse proxy (oauth2-proxy, Cloudflare Access, Pomerium, …). Only safe when arti is unreachable except through that proxy. |
| `disabled` | Local development only; refuses to start unless the configuration looks local. |

**Storage**: any S3-compatible store — AWS S3 (static keys or the ambient
IAM chain), MinIO, Cloudflare R2, Backblaze B2, and friends. TLS to the
store is on by default. Postgres holds all metadata; the store holds only
content bytes.

## Building from source

```sh
make setup && make dev-up          # tools check; Postgres + MinIO (+ OpenSearch, Dex)
make migrate migrate-test          # apply migrations
make build                         # ./bin/arti-server and ./bin/arti
make lint && make test             # the same gates CI runs
cd web && npm ci && npm test && npm run build
```

The full verification battery — including the Docker build and an
end-to-end smoke across REST, CLI, MCP, and the web UI
(`./scripts/e2e-smoke.sh`) — runs in [CI](.github/workflows/ci.yml) from a
clean machine.

## Security boundaries

- Put TLS in front (your proxy) and set `ARTI_BASE_URL` +
  `ARTI_COOKIE_SECURE=true` for any non-local deployment.
- `proxy` mode trusts identity headers — arti must not be reachable except
  through the authenticating proxy.
- Uploaded HTML renders under a sandboxing CSP; `APP` artifacts reach only
  the MCP upstreams the operator configures, through a consenting,
  viewer-scoped proxy.
- Access control: artifacts are creator-owned with optional `allowed_access`
  grants; admins are only the emails in `ARTI_ADMIN_EMAILS`.

## Documentation

The full docs render in-app at `/help` (source in
[`web/docs/`](web/docs/)): overview and quickstart, guides (web UI, CLI,
API, MCP, search, self-hosting), recipes (agents, API consumers, app
developers), reference (configuration, CLI, REST, MCP tools, app SDK),
architecture, and operations.

## License

[Apache-2.0](LICENSE). Copyright AngelList.
