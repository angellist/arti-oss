---
title: Authentication & access
order: 3
summary: Humans authenticate via Google OAuth → Dex at the protected ingress and get an arti_session cookie; CLIs and headless agents get bearer tokens via the pair and RFC 8628 device flows; self-serve API keys give upload-only programmatic access; scoped app/embed tokens are minted for sandboxed pages and rejected on session routes.
---

# Authentication & access

The auth middleware guards the authed API group. It accepts several credential
shapes and admits a request if *any* of the configured paths validate. Two gates
apply on every path: an **email-domain allowlist** and, when configured,
**required group membership**.

![Authentication paths](./auth.svg)

## The two gates

- **Email allowlist** — the address, or its domain, must be in the allowlist
  config (`AUTH_ALLOWED_EMAILS`; entries are bare domains or full addresses,
  empty admits nobody). Applied on every path.
- **Required groups** — matched against the groups the proxy forwards or the
  OIDC groups claim. Providers that emit groups as `name@domain` (e.g. Dex)
  have the suffix stripped before matching, so the config can stay short
  (`engineers` matches `engineers@example.com`).

## How a request is admitted

The middleware tries credentials in order, admitting on the first that validates:

1. **Service secret** — a shared-secret header (`X-Arti-Service-Secret`) for
   service-to-service callers (e.g. the couch FE proxy), compared constant-time.
   Identity becomes a service principal.
2. **Token extraction** — a bearer is pulled from, in order, the `Authorization:
   Bearer` header, the `arti_session` cookie, then the forwarded
   `X-Auth-Request-Access-Token` header. (Cookie-before-forwarded-token precedence
   matters: the arti-signed cookie proves the login-time group gate that the
   forwarded proxy token lacks.) No token → `401`.
3. **API key** — a token with the `arti_` prefix is looked up as a self-serve
   [API key](#scoped-credentials) (SHA-256 match against a non-revoked, unexpired
   row) and run through the domain gate. A bad key is `401`; it does not fall through.
4. **OIDC bearer** — verified against Dex (JWKS). A domain/group rejection is `403`.
5. **HS256 bearer** — the in-process signer behind the `arti_session` cookie,
   CLI-pair, and device tokens; app- and embed-scoped tokens are rejected here (below).

Crucially, the auth middleware **does not trust the `X-Auth-Request-Email` identity
header** — that admission path was removed to close an identity-spoofing hole (a
public-ingress caller could forge the header). The forwarded
`X-Auth-Request-Access-Token` is used only as a last-resort token *source* and is then
cryptographically verified like any other bearer. `X-Auth-Request-Email` is trusted
**only** on the two ProtectedIngress handlers where oauth2-proxy overwrites it: the
browser/CLI login handler (which mints the cookie) and the MCP `/oauth/authorize`
handler. Neither issues a credential from the navigation alone: both render an
arti-served page whose same-origin POST does the minting, so a cross-site
navigation cannot produce a code or a paired CLI token.

That trust is conditional on the deployment declaring a proxy, not on the header
being present. `ARTI_AUTH_MODE` decides: `proxy` (and the legacy `""` default)
say an authenticating proxy terminates every request and overwrites the
`X-Auth-Request-*` headers, so `/oauth/authorize` may read identity from them.
Under `oidc` and `disabled` there is no such proxy, arti faces the network
itself, and the header is a string the caller chose. `/oauth/authorize` then
ignores it and identifies the user from the signed `arti_session` cookie,
redirecting to `/auth/login` when there is no session. In `oidc` mode that
holds for every route, because the login switch mounts `OIDCLogin` and the
header-reading `IngressLoginHandler` is never mounted. In `disabled` mode
`/auth/login` is still the ingress handler and still reads the header, which
that mode's own guarantee already covers: it attributes every request to a
fixed email regardless, so it must never be network-reachable.
Both the consent GET and the approving POST resolve identity through one shared
path, so they cannot drift apart into a flow that renders a page it will not
honour.

The cookie path applies no separate required-groups check, and that is
deliberate: both interactive login handlers apply `AUTH_REQUIRED_GROUPS` before
they mint `arti_session`, so a cookie that verifies has already passed the gate,
and an `oidc`-mode request carries no groups header to re-read. The proxy path
keeps checking `X-Auth-Request-Groups` on every request, where the proxy is the
live authority on group membership.

There is also a **local dev bypass** (`ARTI_AUTH_DISABLED`) that attributes every
request to a fixed email and logs a loud warning. Never enable it in prod — and with
auth enabled the server refuses to boot on an unsafe `JWT_SIGNING_KEY` (empty, the
in-repo dev default, or under 32 bytes).

## Browser login and the `arti_session` cookie

Humans never see an arti login form. By the time a browser reaches the pod,
oauth2-proxy → Dex has already authenticated them, so the request carries the
`X-Auth-Request-*` headers.

- The Next.js middleware checks for the `arti_session` cookie. If it's absent it
  bounces to the login route — it does *not* re-run OAuth (oauth2-proxy already
  did).
- The login handler runs the two gates against the forwarded headers, then mints a
  short HS256 JWT (email, name, picture, a `user` scope, 7-day TTL) and sets it as
  the `arti_session` cookie (HttpOnly, SameSite=Lax, Secure in prod).
- Logging out clears the cookie and redirects to oauth2-proxy's sign-out so the
  upstream Dex session is revoked too.

The cookie is the credential for all in-app API calls. It is *also* what
identifies the user at the OBO callback, where the public-ingress
`X-Auth-Request-Email` header would be forgeable — only the signed cookie is
trusted there.

## CLI authentication

The `arti` CLI caches a token in the user's config dir. Two ways in:

- **Pair flow** (`arti login`) — the CLI opens an SSO login URL carrying a pairing
  code; after SSO the login handler pairs the code with the verified email, and the
  CLI exchanges the code for a signed bearer (7-day access, 90-day refresh).
- **`ARTI_TOKEN`** — an injected bearer (e.g. a sandbox's device-flow token) takes
  precedence over the on-disk login, so headless callers skip `arti login` entirely.

## The device flow (RFC 8628) for headless agents

A headless agent (a couch sandbox) can't open a browser, so it uses the Device
Authorization Grant. It is Postgres-backed because each step can land on a
different replica.

1. **Code request** — the agent requests a grant, optionally a long-lived one. It
   gets back a `device_code` (its polling secret), a short human `user_code`
   (ambiguous letters excluded), a verification URL, and a 10-minute window.
2. **Human approval** — the human opens the verification URL, lands on an
   anti-phishing confirm page that shows the code and the requested lifetime, then
   clicks through SSO. The SSO-verified email is recorded on the grant.
3. **Token poll** — the agent polls; while pending it gets `428
   authorization_pending`. On approval the grant is atomically consumed and an
   **upload-scoped** access token is minted.

Lifetimes: a short grant yields a 24h access token with no refresh and no
persisted family row. A long grant yields a 24h access token plus a rotating
30-day refresh token tracked as a *family*. Re-approval revokes the user's prior
families (one active family per user, enforced by a partial unique index). Refresh
rotates the family with a compare-and-swap on the current token id to defeat
replay; an authed revoke endpoint kills the caller's families. Refresh tokens are
**bound to their issuing flow** — a device refresh token can't be redeemed on the CLI
or MCP refresh endpoints, and vice versa, so a token from one flow can't be escalated
through another.

## Scoped credentials and why session routes reject them {#scoped-credentials}

Three token *scopes* exist beyond a normal user session, all signed by the same
HS256 signer:

| Scope | Minted for | Authorizes |
| --- | --- | --- |
| App | An APP artifact's in-page bridge | The apps proxy, for that one app + its declared tool allowlist |
| Embed (comment) | The comments overlay injected into sandboxed served HTML | The comments embed API, for that one artifact |
| Upload | The device flow and self-serve API keys | Artifact create / append / read only |

App and embed tokens are **exposed in served-page source**, so accepting one as a
session credential would let a leaked page token drive the whole API as that user.
The auth middleware therefore admits an HS256 token only when it is *not*
app-scoped and *not* embed-scoped; each scoped token works only through its own
middleware, which pins it to a single artifact/app.

The **upload** scope is enforced separately, by a guard that sits after the main
auth middleware in the authed group. For an upload credential it (a) applies a
default-deny allowlist — downgrading *even an admin* to create/append/read only —
and (b) caps the body size (25 MiB). So a leaked upload bearer can never delete or
reach admin. Device tokens additionally re-check that the long-lived family isn't
revoked or expired on every call.

**API keys** are a *non-JWT* upload credential: an opaque `arti_upload_…` string,
stored only as a SHA-256 hash, minted self-serve by any authenticated user and tied
to that user's email. They carry the `upload` scope, so they hit the exact same guard
(and 25 MiB cap) as a device token — never delete, edit, admin, or MCP. Revocation and
expiry are enforced at the API-key auth rung (a revoked or expired key simply fails to
authenticate). Minting a key requires a full-access credential; an upload credential
can't mint more keys.

## Access control on reads

Authentication says *who you are*; the `allowed_access` column says *what you can
read*. The in-memory check and the SQL access filter share one rule: a caller may
read a row if they're the creator, an `allowed_access` pattern matches their email
(glob-aware, case-insensitive), the pattern is `*` (everyone authenticated), or a
`group:<name>` token overlaps their group membership. Admins (gated by RBAC /
config) bypass the filter for management actions. Restricted artifacts return
`404`, not `403`, so they can't be probed. See [Data model](data-model.md) for the
column itself.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
