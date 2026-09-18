---
title: Self-hosting
order: 8
summary: The three setup profiles — local evaluation, single box, and retrofit into an existing environment — as machine-followable checklists with a verification step after each action.
---

# Self-hosting arti

arti needs three dependencies: **Postgres** (metadata), an **S3-compatible
object store** (content bytes), and — for real deployments — an **identity
provider or SSO proxy** (login). This guide is written as checklists with an
explicit verification after every step, so a human or their AI assistant can
drive an install and know exactly where it stands. Your main tool throughout
is [`arti-server doctor`](../reference/configuration.md): run it after every
configuration change.

## Profile 1 — local evaluation (nothing required)

1. `docker compose -f deployments/docker-compose/docker-compose.yml -f deployments/docker-compose/docker-compose.app.yml up -d --build`
2. Verify: `curl -fsS http://localhost:8090/healthz` returns 204, and
   http://localhost:8090 renders the catalog in a browser.

Authentication is off; every action is attributed to a local identity. Do
not expose this profile beyond your machine.

If `8090` is already taken on your machine, remap it with a small compose
override — note the `!override` tag: without it Compose *appends* to the
`ports` list and keeps the colliding mapping:

```yaml
# docker-compose.localport.yml (add with a third -f flag)
services:
  server:
    ports: !override
      - "8091:8090"
    environment:
      ARTI_BASE_URL: http://localhost:8091
```

## Profile 2 — single box

The evaluation profile plus real auth and TLS:

1. Bring up the evaluation profile and confirm it works (above).
2. Pick an auth mode and configure it (next section). Set
   `AUTH_ALLOWED_EMAILS` (nobody is admitted until you do) and
   `ARTI_ADMIN_EMAILS` (nobody is an admin until you do), remove
   `ARTI_AUTH_DISABLED`, and set a strong `JWT_SIGNING_KEY` (≥32 bytes).
3. Put your TLS proxy (Caddy, Traefik, nginx) in front of `:8090`, set
   `ARTI_BASE_URL` to the public https URL and `ARTI_COOKIE_SECURE=true`.
4. Persist the `postgres` and `minio` volumes; back up Postgres and the
   bucket (all content is reproducible from those two).
5. Verify: `arti-server doctor` passes; a browser login round-trips; and
   `curl -fsS https://<your-host>/healthz` returns 204.

## Profile 3 — retrofit into an existing environment

Each dependency maps to one configuration section and is verified
independently — do them in order and run doctor between steps.

1. **Database**: set `ARTI_DATABASE_URL` to your Postgres DSN (a dedicated
   database; the app needs plain DML plus `CREATE TABLE` for migrations).
   Run `arti-server migrate`, then `arti-server doctor` — the database check
   must report a migration version.
2. **Object store**: set `S3_BUCKET` (+ `S3_REGION`), and for anything that
   isn't AWS S3 set `S3_ENDPOINT` (bare `host[:port]`, no scheme or path).
   Credentials: static keys via `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`,
   or leave them empty on AWS to use the ambient chain (IRSA, instance
   role, shared credentials file). TLS is on by default; `S3_USE_SSL=false`
   is for local MinIO only. Verify: doctor's object-store check performs a
   real write/read/delete probe.
   - **AWS S3**: endpoint empty; prefer the ambient chain over static keys.
   - **MinIO**: `S3_ENDPOINT=host:9000`, static keys.
   - **Cloudflare R2**: `S3_ENDPOINT=<account>.r2.cloudflarestorage.com`,
     static keys, `S3_REGION=auto`.
   - Backblaze B2, DigitalOcean Spaces, Wasabi, Garage, Ceph RGW: static
     keys + their documented endpoint.
3. **Identity**: pick ONE —
   - **`ARTI_AUTH_MODE=oidc`** (recommended): register an OAuth client at
     your IdP (Okta, Auth0, Entra ID, Google, Keycloak, Dex, authentik…)
     with redirect URI `<ARTI_BASE_URL>/auth/callback`; set
     `AUTH_DEX_ISSUER_URL` (the issuer URL, exact — including any path),
     `AUTH_OIDC_CLIENT_ID`, `AUTH_OIDC_CLIENT_SECRET`. If you gate on
     groups, set `AUTH_REQUIRED_GROUPS` and check your IdP emits a groups
     claim (`AUTH_OIDC_GROUPS_CLAIM` if it's not named `groups`; Okta emits
     it on request, Entra needs a groups claim configured, plain Google
     OIDC has none). Verify: doctor's issuer check passes, then a browser
     login round-trips.
   - **`ARTI_AUTH_MODE=proxy`**: you already run an authenticating proxy
     (oauth2-proxy, Cloudflare Access, Pomerium, Authelia…) that injects
     `X-Auth-Request-Email`/`-Groups`. Hard requirement: arti must not be
     network-reachable except through that proxy, and the proxy must
     **overwrite** those headers rather than pass them through — this mode
     tells arti to believe them, so a request that reaches arti directly is
     whoever it claims to be.

   The mode you pick decides whether those headers are believed at all.
   Under `oidc` (and `disabled`) arti ignores them everywhere, including
   the MCP OAuth authorization endpoint, and identifies users only from the
   session cookie it signed itself — so running arti directly on the
   network, as this recommended path does, does not hand anyone an identity
   for the asking. You do not need a proxy to use MCP OAuth safely.
4. **Allow and administer**: `AUTH_ALLOWED_EMAILS=yourdomain.com` (empty
   admits nobody) and `ARTI_ADMIN_EMAILS=you@yourdomain.com`.
   Entries may be **full addresses** as well as domains, and on a consumer
   IdP they must be: `AUTH_ALLOWED_EMAILS=you@gmail.com` gates on you,
   whereas `gmail.com` admits every Google account on the internet. Mix
   them freely — `you@gmail.com,yourdomain.com`. Doctor's `access gate`
   check spells out what each entry admits.
5. Verify end to end: `arti-server doctor` all green → start the server →
   log in → upload something with the `arti` CLI → open its URL.

## No IdP at all?

Run one container: the compose stack's Dex service is a working example
(static users, or federate GitHub/Google). Point `oidc` mode at it. That is
the supported no-IdP path — arti deliberately has no local passwords.

## Connect your own MCP servers {#mcp-servers}

APP artifacts call tools through arti's governed proxy. Which servers an app may
reach is deployment configuration, so a self-hosted arti talks to whatever MCP
servers you run. Nothing in arti depends on a particular vendor or gateway.

Three servers are built in and need no configuration:

| Name | What it is |
|---|---|
| `arti` | arti's own tools, run in-process as the viewer. Reads and writes both. |
| `arti-self` | arti's own `/mcp` over loopback, with no credential. For local verification. |
| `llm` | The built-in Claude completion. Needs `ANTHROPIC_API_KEY`. |

Add your own through `ARTI_APP_MCP_SERVERS`, a JSON map of `name →
{ resource_url, auth, scope }`. The entries merge over the built-ins. A
malformed value stops the server at startup.

```json
{
  "internal-tools": { "resource_url": "https://mcp.example.internal/mcp", "auth": "none" },
  "vendor":         { "resource_url": "https://mcp.vendor.example/mcp",   "auth": "oauth", "scope": "read write" }
}
```

An app author then writes `callTool("internal-tools", "list_things", {…})` and
never sees the URL. The address and the auth policy stay server-side, so an app
can neither point at another endpoint nor carry a secret, and it reaches a server
only when its own `arti-app.json` allowlist names that server and tool.

**Use `auth: "none"`** when the upstream needs no per-user identity — a server on
your own network, or one the edge authenticates for you. arti forwards the call
with no credential. A 401 from such a server is a real rejection and reaches the
app as `503 upstream_error`, with no consent loop.

**Use `auth: "oauth"`** when each call must run as the person looking at the page.
arti brokers a per-user token through its OBO broker, and the viewer's first call
to that server opens a one-time consent popup.

### What an `oauth` upstream must support {#oauth-upstream}

The broker is standard OAuth 2.1 with no vendor-specific code. The MCP server, or
the authorization server it names, must offer all of:

1. **Protected-resource metadata** (RFC 9728) at the resource URL, naming its
   authorization server.
2. **Authorization-server metadata** (RFC 8414) at that issuer.
3. **Dynamic client registration** (RFC 7591). arti registers itself once per
   authorization server and shares that registration across its pods. An issuer
   whose metadata has no `registration_endpoint` is refused: there is no way to
   configure a pre-registered client.
4. **Authorization code with PKCE**, plus refresh tokens if consent is to outlive
   one access token.
5. **Resource indicators** (RFC 8707). arti sends the configured `resource_url`.

The redirect URI arti registers and sends is `<ARTI_BASE_URL>/oauth/obo/callback`.
Set `ARTI_OBO_CALLBACK_BASE` when the browser reaches arti at some other address.
Set `ARTI_OBO_ENC_KEY` in production: the broker encrypts stored tokens with it,
and it otherwise derives from `JWT_SIGNING_KEY`.

An MCP gateway that fronts several vendors is one entry like any other, and so is
a single-purpose server of your own.

### Verify

1. Start the server. A malformed `ARTI_APP_MCP_SERVERS` stops it with the parse
   error.
2. Upload a small APP whose `arti-app.json` allowlists one tool on the new server,
   open it, and call that tool.
3. Read the structured failure code rather than the message: `unknown_server`
   (400) means the name is not configured, `not_allowlisted` (403) means the
   manifest does not name it, and `upstream_error` or `upstream_timeout` (503)
   come from the server itself. For an `oauth` server the first call answers 401
   with an `authorize_url`, and the injected bridge opens the consent popup.

The app-side call and the full error table are in the
[APP SDK](../reference/app-sdk.md#built-in-servers); the surrounding variables are
in [Configuration](../reference/configuration.md#apps-mcp).
