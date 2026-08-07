---
title: Configuration
order: 5
summary: Every environment variable arti-server and the web frontend read — with defaults and meaning, grouped by area.
---

# Configuration

`arti-server` is configured through environment variables (it's k8s-friendly),
loaded by the typed `internal/config` package. The Next.js frontend reads a few
of its own. This page lists every variable and its default.

Structured, non-secret settings can also come from an optional YAML file named
by `ARTI_CONFIG_FILE`. Precedence is: built-in defaults → YAML file →
environment variables. Secret-bearing settings (credentials, keys, tokens —
including `ARTI_DATABASE_URL`) are environment-only; putting one in the YAML
file fails startup as an unknown key, as does any typo'd key.

`ARTI_DATABASE_URL` and `S3_BUCKET` are **required** (the server refuses to start without
them). Everything else has a default. For a runnable starting point see `.env.example` in
the repo root.

After any configuration change, run **`arti-server doctor`**: it validates the
configuration and probes each dependency (database + migration status, object-store
bucket with a write/read/delete probe object, issuer discovery), failing with the exact
variable name and fix. `--config-only` skips network checks;
`--print-effective-config` prints the resolved configuration with secret fields omitted.

## Server & addressing

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_ADDR` | `:8090` | TCP address the Go binary listens on. |
| `ARTI_BASE_URL` | `http://localhost:8090` | Canonical hostname stamped into every emitted artifact URL and used to build OAuth/metadata absolute URLs. |
| `ARTI_WEB_URL` | `""` | Upstream Next.js URL to reverse-proxy all non-API routes to. Empty → no FE proxy (the binary serves API/MCP only). |
| `ARTI_COOKIE_SECURE` | `false` | Set the `Secure` flag on `arti_session`. Must be `true` behind HTTPS, `false` for localhost. |

## Auth {#auth}

Interactive login is selected by `ARTI_AUTH_MODE`: `oidc` (arti runs the
authorization-code flow itself against any compliant issuer — PKCE + nonce,
callback at `/auth/callback`), `proxy` (a trusted reverse proxy injects
identity headers; the legacy default when unset), or `disabled` (local dev).
The HS256 signing key powers sessions, the CLI pair flow, and test-mode
tokens. See the [auth architecture](../architecture/auth.md).

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_AUTH_MODE` | `""` | `oidc` \| `proxy` \| `disabled`. Empty behaves like `proxy`. `oidc` requires the issuer URL and client ID and fails startup without them. |
| `AUTH_OIDC_CLIENT_ID` | `""` | OAuth client ID arti is registered as at the issuer (`oidc` mode). |
| `AUTH_OIDC_CLIENT_SECRET` | `""` | That client's secret. Environment-only. |
| `AUTH_OIDC_SCOPES` | `openid,email,profile` | Scopes requested during built-in login. |
| `AUTH_OIDC_GROUPS_CLAIM` | `groups` | ID-token claim carrying group membership (providers disagree on the name). |
| `AUTH_DEX_ISSUER_URL` | `""` | Dex OIDC issuer. Empty → OIDC auth disabled (the verifier is nil). |
| `AUTH_JWT_AUDIENCE` | mode-dependent | Expected `aud` claim on **raw ID-token bearer auth** (interactive login verifies against the client ID and ignores this). Unset it resolves to `AUTH_OIDC_CLIENT_ID` in `oidc` mode — what Google, Okta, Entra, Auth0 and every other standard issuer put in `aud` — and to `auth` otherwise, the Dex-behind-oauth2-proxy audience. Set it only to override. |
| `AUTH_REQUIRED_GROUPS` | `""` | Comma-separated groups a user must belong to. Empty → no group requirement. |
| `ARTI_IDP_GROUPS_MAX_AGE` | `336h` | How long a login-captured IdP group snapshot stays valid for `idp:<name>` access grants. Each interactive login (oidc or proxy) refreshes the snapshot; grants stop resolving once it ages past this bound (fail closed), so **`idp:` grants require an interactive login at least this often**. `0` disables `idp:` resolution entirely. |
| `AUTH_ALLOWED_EMAILS` | `""` | Comma-separated email allowlist; applied on every auth path (OIDC, proxy, test-mode, device, OBO re-check). Each entry is either a **bare domain** (`example.com`, admitting everyone there) or a **full address** (`you@gmail.com`, admitting exactly that person) — mix freely. **Empty admits nobody** (fail closed; the server still starts, but no interactive login can succeed — `doctor` says so). Against a consumer IdP, always list addresses: a bare `gmail.com` admits every Google account on the internet. **Renamed from `AUTH_ALLOWED_DOMAINS`** when the list stopped being domain-only; the old name is refused at startup with a pointer to this one, rather than being ignored into an empty (admit-nobody) allowlist. |
| `ARTI_ADMIN_EMAILS` | `""` | Comma-separated; the lockout-proof `ADMIN` floor (re-asserted on boot). Gates hard-delete and cross-creator archive. Empty → no administrators until configured. |
| `ARTI_SERVICE_SECRET` | `""` | Optional service-to-service shared secret (sent as `X-Arti-Service-Secret`). Empty → s2s auth off. |
| `JWT_SIGNING_KEY` | `dev-key-not-for-prod` | HS256 key for CLI-pair, test-mode, device, and MCP-OAuth tokens. **The server refuses to boot** (unless `ARTI_AUTH_DISABLED=true`) if this is empty, the in-repo dev default, or shorter than 32 bytes — provision a strong secret in prod. |
| `ARTI_TEST_MODE` | `false` | Enable `POST /auth/test` to mint HS256 tokens without Dex. **Dev only.** |
| `ARTI_AUTH_DISABLED` | `false` | Skip auth entirely; every request is attributed to `ARTI_LOCAL_EMAIL`. **Never enable in prod.** |
| `ARTI_LOCAL_EMAIL` | `local@example.com` | Identity used when auth is disabled. In this mode a request may override it with the `X-Arti-Local-Email` header. |

### Device authorization grant (RFC 8628)

Headless agents obtain a human-approved, upload-scoped token. See the
[device-flow endpoints](rest-api.md#device).

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_DEVICE_TOKEN_TTL` | `24h` | Access-token lifetime for device tokens. |
| `ARTI_DEVICE_TOKEN_MAX_TTL` | `720h` | Ceiling on how long a refreshable token family stays valid (30 days). |
| `ARTI_DEVICE_MAX_UPLOAD_BYTES` | `26214400` | Per-request POST body cap for upload-scoped tokens (25 MiB). Also applies to [API-key](#api-keys) uploads. |
| `ARTI_DEVICE_CODE_RPM` | `10` | Per-IP rate limit (requests/min) on the public `POST /auth/device/code`. `0` disables. |
| `ARTI_OAUTH_REGISTER_RPM` | `10` | Per-IP rate limit (requests/min) on the public `POST /oauth/register` (MCP dynamic client registration). `0` disables. |

### API keys {#api-keys}

Self-serve, upload-scoped bearer keys minted at `POST /api/keys`. See the
[API-key endpoints](rest-api.md#api-keys) and the [Using the API](../guides/api.md#api-keys-self-serve)
guide. Upload enforcement (allowlist + 25 MiB body cap) is shared with the device flow
above.

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_API_KEY_MAX_TTL` | `8760h` | Ceiling on a minted key's lifetime (365 days). The request's `ttl_days` is clamped to `[1 day, this]`; default 90 days. |
| `ARTI_API_KEY_RPM` | `10` | Per-IP rate limit (requests/min) on the mint route `POST /api/keys`. `0` disables. |

## Storage (S3 / MinIO)

Real S3 in prod, MinIO in dev. An empty `S3_ENDPOINT` targets AWS.

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_DATABASE_URL` | *(required)* | Postgres connection string. |
| `S3_BUCKET` | *(required)* | Blob bucket name. |
| `S3_ENDPOINT` | `""` | S3-compatible endpoint (scheme is stripped). Empty → AWS. |
| `S3_REGION` | `us-east-1` | |
| `S3_USE_SSL` | `false` | Use TLS to reach the endpoint. |
| `AWS_ACCESS_KEY_ID` | `""` | Access key (MinIO: `minioadmin` in dev). |
| `AWS_SECRET_ACCESS_KEY` | `""` | Secret key. |

## Apps & MCP

The governed proxy for APP artifacts and the upstream MCP servers they may reach. See the
[APP SDK](app-sdk.md#built-in-servers).

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_APP_MCP_SERVERS` | `""` | JSON map `name → { resource_url, auth, scope }` of upstream MCP servers, merged over the built-in defaults (`arti-self`, `llm`). A bad value fails startup. |
| `ARTI_APP_FRAME_ANCESTORS` | `'self'` | CSP `frame-ancestors` source list for served APP HTML — which origins may iframe an arti app (runtime, arti-server). Other artifact types keep `X-Frame-Options: SAMEORIGIN`. Kept in sync with `ARTI_CATALOG_FRAME_ANCESTORS` (the build-time catalog-viewer default). |

## OBO broker

arti as an OAuth client for the user→arti→upstream on-behalf-of leg.

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_OBO_CALLBACK_BASE` | `""` | Externally-reachable base URL for `…/oauth/obo/callback`. Empty → falls back to `ARTI_BASE_URL` (correct in every deployed env + dev). |
| `ARTI_OBO_ENC_KEY` | `""` | Master key for the OBO broker's envelope encryption (state token + at-rest token/secret columns). Empty → derived from `JWT_SIGNING_KEY`. Provision a dedicated high-entropy value in prod. |

## LLM (built-in completion) {#llm-built-in-completion}

The in-process `llm.complete` tool and the upload modal's metadata auto-fill. Empty
`ANTHROPIC_API_KEY` → the `llm` server returns `501`. Budget caps of `0` mean no cap; the
budget gate fails **closed** if the ledger is unreadable. See
[llm.complete](app-sdk.md#llmcomplete).

| Variable | Default | Meaning |
|---|---|---|
| `ANTHROPIC_API_KEY` | `""` | Service key for the Anthropic Messages API. Empty → completion disabled. |
| `ARTI_LLM_DEFAULT_MODEL` | `claude-sonnet-4-6` | Default model (aliases `opus`/`sonnet`/`haiku` accepted). |
| `ARTI_LLM_ALLOWED_MODELS` | `claude-opus-4-8,claude-sonnet-4-6,claude-haiku-4-5` | Comma-separated allowlist of callable models. |
| `ARTI_LLM_RPM_PER_VIEWER` | `30` | Requests-per-minute cap per viewer. |
| `ARTI_LLM_TPH_PER_VIEWER` | `300000` | Tokens-per-hour cap per viewer. |
| `ARTI_LLM_TPH_PER_APP` | `1000000` | Tokens-per-hour cap per app. |
| `ARTI_LLM_TPD_PER_ORG` | `0` | Tokens-per-day cap per org (0 = no cap). |

## Embed surfaces

Serving arti artifacts into external cross-site iframes, authenticated by per-surface
shared secrets. Empty disables the `/embed/*` routes. See
[front embed](../architecture/front-embed.md).

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_EMBED_SURFACES` | `""` | JSON map `name → surface` (`{ secret, origin, identity, email, slug_allow, shell, shell_slug }`). `origin` is a string or a list of ancestor origins. `identity` is `"service"` (default; `email` required) or `"user"` (tool calls run as the real viewer via a consent handshake; `email` must be omitted). A malformed surface fails startup. See [Embed surfaces](../architecture/front-embed.md). |

## Notifications

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_SLACK_BOT_TOKEN` | `""` | Bot user OAuth token (`xoxb-…`) for the "Arti" Slack app; empty → comment-notification DMs disabled. Requires bot scopes `users:read.email`, `chat:write`, `im:write`. |

## Web frontend (Next.js)

Read by the Next.js app, not `arti-server`.

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_API_URL` | `http://localhost:8095` | Server-only base URL the FE proxies/SSR-fetches to (the Go binary). `web/next.config.ts`, `web/lib/arti.ts`. |
| `ARTI_CATALOG_FRAME_ANCESTORS` | `'self'` | CSP `frame-ancestors` for catalog/viewer pages — which origins may iframe the viewer (e.g. an internal tool's side panel embedding `/s/<slug>?v=full`). **Build-time only**: Next bakes `headers()` into the build, so this is read at `next build`, not on the running pod — set it as a build arg to override; the baked fallback lives in `web/tenant-defaults.ts`. Kept in sync with `ARTI_APP_FRAME_ANCESTORS` (the runtime arti-server knob). `web/next.config.ts`. |
| `NEXT_PUBLIC_ARTI_AUTH_DISABLED` | unset | When `true`, the FE skips the login redirect (pairs with `ARTI_AUTH_DISABLED` for local UI testing). `web/middleware.ts`. |

## Migration tool

`arti-server migrate` reads only:

| Variable | Default | Meaning |
|---|---|---|
| `ARTI_DATABASE_URL` | *(required)* | Postgres connection string. |

## See also

- [REST API](rest-api.md) and [MCP tools](mcp-tools.md) — what these settings gate.
- [Deploying](../operations/deploying.md) and [Connections](../operations/connections.md).
