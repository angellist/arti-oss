---
title: App serving
order: 4
summary: arti serves APP artifacts as sandboxed opaque-origin pages whose JS reaches MCP tools only through a scoped-token proxy that re-checks access and an arti-app.json allowlist on every call.
---

# App serving

An **APP** is a package-shaped artifact served as a single-page web app. Its
JavaScript calls `window.arti.callTool(server, tool, args)`, which POSTs to a
same-origin proxy that brokers MCP tool calls. The page has no backend, no API
keys, and no ambient session — every dynamic operation is a tool call arti
mediates and governs.

## The sandbox guarantee

arti serves every HTML artifact in a sandboxed iframe with **no
`allow-same-origin`**, so the page runs in a unique opaque origin: it cannot
read the session cookie, touch the catalog DOM, or call arti's APIs with the
user's credentials. APP serving keeps that. It grants only the popup and
top-navigation permissions the OAuth consent flow needs, plus a `frame-ancestors`
allowlist for embedding. It never adds `allow-same-origin`.

Because the page can't use the ambient session, it gets a scoped credential
instead: a JWT scoped to one app (`app:<artifactID>`), minted per viewer and
injected into the page. This token authorizes **only** the apps proxy, **only**
for its one app. The main auth middleware rejects an app-scoped token on every
regular API/MCP route, so a leaked app token can't drive the rest of arti.

## Request flow

arti injects the bridge server-side and serves the entry page full-screen at
`/app/{ident}[/{version}]`.

![App-serving request flow](./app-serving.svg)

Each call to the apps proxy runs through these checks:

1. Verify the Bearer app token; extract `{email, app_id}` from its scope.
2. Re-check the domain allowlist so a user whose access was pulled stops working
   before the token expires.
3. Confirm the request's `app_id` equals the token's scoped app.
4. Load the APP and re-run the artifact read rule (defense in depth).
5. Read `arti-app.json` fresh from the package; the `(server, tool)` pair must be
   in its `tools` allowlist or the call is rejected.
6. Resolve the named server to a server config (URL + auth policy live
   server-side — the app names a server, it can't supply a URL), then dispatch by
   that server's auth mode.

## Server auth modes

Each configured server declares how arti authenticates the upstream call:

- **OBO (on-behalf-of)** — the tool runs as the viewer, with their upstream
  permissions and audit trail. The viewer's per-user upstream token comes from
  the OBO broker. No token yet → the proxy returns an `authorize_url`; the page
  opens the consent popup and retries. Most built-in servers (notion, flowdash,
  slack, linear, the arti self-MCP, …) route here.
- **Service** — the built-in `llm` server. Dispatched in-process to the Anthropic
  Messages API with arti's service key; no OBO. The viewer email is used only for
  budgets and usage attribution. Serves exactly one tool, `complete`.
- **None** — forwards no bearer. Resolves only under `ARTI_AUTH_DISABLED` (local
  dev); not a real-deployment path.

For OBO and none, a minimal outbound MCP client runs one `initialize` +
`tools/call` upstream. A 401 maps to a re-consent only for OBO servers; a none
401 is a genuine upstream rejection.

## OBO broker

arti is an OAuth *client* of the upstream while also being the OAuth provider
users log into. The broker discovers the resource's authorization server,
dynamic-client-registers, runs authorization-code + PKCE, and brokers a per-user
access token (plus refresh). It is multi-pod correct: PKCE state is a
self-contained encrypted token (no server-side state map), and the registered
client plus per-user tokens live in Postgres with envelope-encrypted secret
columns.

The OBO callback binds the consent to the arti user who started it: it trusts
**only** the signed session cookie sent on the top-level callback navigation (the
`X-Auth-Request-Email` header is forgeable on the public ingress), and refuses if
that identity doesn't match the flow's email. A shared `authorize_url` completed
by a different user is rejected.

## Invariants / security

- **Never add `allow-same-origin` to an APP's CSP.** The whole model rests on the
  page being a credential-less opaque origin. Same-origin would hand it the user's
  cookie.
- **App tokens stay off session routes.** Anything that mints or accepts a normal
  session JWT must keep rejecting app-scoped (and embed-scoped) tokens.
- **Upstream URLs are server-side only.** Apps name a configured server; they
  never supply a URL. Resolving by name is the SSRF defense — don't add a path
  that takes a URL from the app or the user.
- **Re-check access on every proxy call**, not just at page load: domain
  allowlist, artifact read rule, and the `arti-app.json` allowlist. A valid token
  must not outlive revoked access.
- **OBO tool calls run as the viewer.** The callback binds consent to the signed
  session identity; don't loosen that to a forgeable header.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
