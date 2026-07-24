---
title: Embed surfaces
order: 6
summary: Serves any artifact full-page into a cross-site iframe via per-surface shared-secret routes on the public ingress, bypassing the SSO cookie — as a fixed service identity, or (user mode) as the real signed-in viewer via a consent popup.
---

# Embed surfaces

A **surface** is a named external place allowed to frame arti content. Surfaces
are config-driven (`ARTI_EMBED_SURFACES`); the serving routes are generic. Front
is one configured surface — its side panel ("Deployment Context") was the first
consumer — not a special-cased feature. The embed routes are mounted on the
**public** ingress alongside the comments embed and apps proxy.

A surface serves in one of two **identity modes**:

- **`service`** (default) — every request resolves as a fixed, configured
  `email`. Simple; the surface's read scope is that one identity's. Good for
  read-only context panels.
- **`user`** — the embedded page is served with **no identity**; the viewer
  authorizes themselves through a one-time consent popup and every tool call
  then runs **as that real viewer** (full per-user OBO). This is what lets an
  embedded APP call tools the surface identity isn't provisioned for, with
  correct attribution — see [Per-user mode](#per-user-mode-identity-user).

## Why the cookie path fails

The default serving route (`/s/<slug>`) is behind oauth2-proxy and authenticates
with the `SameSite` session cookie. In a third-party iframe (`app.frontapp.com`)
that cookie is never sent, so arti redirects to SSO, and the provider's sign-in
returns `X-Frame-Options: DENY` — the panel goes blank. The embed routes must not
depend on the cookie or the SSO gateway, so they live on the public group and
authenticate with a per-surface shared secret instead.

Separately, arti's default paths only let **APP** artifacts be framed
(everything else stays `X-Frame-Options: SAMEORIGIN`). The embed route owns its
own headers, so it can serve any type framable.

## How it works

A surface is a JSON object in the `ARTI_EMBED_SURFACES` map, validated at startup
— a half-formed surface fails fast rather than serving silently. Its fields:

| field | required | role |
|---|---|---|
| `secret` | yes | shared secret, compared constant-time against `?auth_secret=` |
| `origin` | yes | host(s) allowed to frame this surface (`frame-ancestors`). A single string or a list — a **nested** topology (e.g. Front ▸ couch ▸ arti) must enumerate *every* ancestor origin |
| `identity` | no | `"service"` (default) or `"user"` — see [modes](#embed-surfaces) |
| `email` | service only | identity artifacts resolve as — the read-scope gate. **Required** in service mode; **rejected** in user mode (tool calls run as the real viewer, so a fixed identity would be a footgun) |
| `slug_allow` | yes | patterns (exact or `*` glob) the requested slug must match; `["*"]` = any |
| `shell` | no | client adapter: `""` or `"front"` |
| `shell_slug` | with shell | how the shell builds a slug from the host id, e.g. `deployment-ctx-{id}` (must contain `{id}`) |

Routes registered when at least one surface is configured:

- `GET /embed/{surface}` — the doc. Validates secret → matches `slug_allow` →
  serves full-page (service mode: as the surface `email`; user mode: with no
  baked identity + the connect handshake). A miss (no artifact / not allowed /
  not readable) renders a friendly placeholder, never a JSON 404. Bad secret →
  403; missing `slug` → 400. Response is `Cache-Control: no-store`.
- `GET /embed/{surface}/_files/{token}/*` — sibling assets for PACKAGE/APP
  artifacts. The scoped token in the path is the credential, so assets load
  without the cookie-authed files route.
- `GET /embed/{surface}/shell` — explicit shell path (back-compat). The shell is
  also served by the bare `/embed/{surface}` path when it carries no `?slug=`, so
  the configured Front URL stays clean.
- `POST /embed/{surface}/token` · `GET /embed/{surface}/token` — user-mode
  handshake only (CORS-enabled): the consent page POSTs here to mint, the
  embedded app GETs here to poll for its token. No-ops for service surfaces. See
  [Per-user mode](#per-user-mode-identity-user).

**One gate, two layers.** `slug_allow` is the only server-side gate; the URL
always names the artifact via `?slug=`. `shell_slug` is *not* a gate — it only
tells the shell how to build the URL client-side. For Front: the shell builds
`deployment-ctx-cnv_123` from the conversation id, then the server matches it
against `slug_allow: ["deployment-ctx-*"]`.

**Rendering — full-page, every type.** The response *is* the artifact as the
iframe's top-level document (no `srcdoc`, no re-nesting — both kill in-frame
navigation), with the **app-grade sandbox** applied uniformly so links and
`target=_blank` work. The default non-APP sandbox omits popups, so links silently
die there; applying the app sandbox to everything makes a single doc as clickable
as an APP. By type:

- **APP** — app entry + bridge (MCP / `llm.complete` work).
- **PACKAGE** — entry HTML with `<base href>` pointing at the token-scoped
  `_files` path so sibling links/assets resolve in-panel.
- **single HTML** — served as-is.
- **markdown** — rendered server-side (GFM, no raw-HTML passthrough) into a styled
  HTML doc with `<base target="_blank">` so links open a real tab.
- **PDF / image / other** — streamed inline.

**Front shell.** Front's side-panel URL is static and never injects the
conversation id, so the shell runs Front's Plugin SDK, reads the open
conversation, builds the slug, and frames the doc route in a nested same-origin
iframe — swapping the `src` as the user changes threads (keeps the SDK
subscription alive). The auth secret is read from the shell's own URL (Front
appends it) and forwarded to the doc. Topology: Front frames the shell
(`frame-ancestors {origin}`), the shell frames the doc (`frame-ancestors 'self'`).

![Embed request and trust flow](./front-embed.svg)

## Per-user mode (`identity: "user"`)

Service mode caps a surface to one identity's tools and gives no attribution. A
`user`-mode surface instead runs every tool call **as the real signed-in
viewer**, so an embedded APP reaches exactly what that person can (including
first-use OBO consent for connectors), attributed to them.

The design constraint is that the embedded page is opaque-origin (the app
sandbox omits `allow-same-origin`) **and**, inside a host like Front, its popup
is further sandboxed by the host's plugin iframe. So the handshake must work
from a **fully sandboxed popup** — it cannot submit a form (no `allow-forms`),
cannot rely on `window.opener`, and must not mint on a prefetchable GET. It is
built accordingly and is host-agnostic (nothing Front-specific):

1. **Serve.** `GET /embed/{surface}` returns the app with no token — just a
   `connect` config. The injected app bridge shows a "Connect as you"
   affordance (never an unprompted popup — browsers block those).
2. **Consent.** The bridge opens `GET /auth/embed/app-token` (a top-level popup
   — first-party on arti's origin, so the session cookie flows). This route is
   behind the auth proxy; if there's no session yet it redirects through
   `/auth/login?return_to=…` and back. It **renders a consent page and issues a
   signed one-time `consent_challenge`** (HMAC over `email|app|surface|state`,
   bound to the authenticated session that rendered it) — **it never mints.**
3. **Complete.** The consent page's "Continue" is a **`fetch` POST** to the
   public `POST /embed/{surface}/token` (needs only `allow-scripts`, which the
   host grants; a POST can't be prefetched). That endpoint verifies the
   challenge (identity comes from inside it — no cookie needed), mints a
   short-TTL per-user app token, and stashes it in a small cross-replica store
   keyed by the app's `(surface, state)` nonce.
4. **Deliver.** The bridge **polls** `GET /embed/{surface}/token?state=…` until
   the token appears (single-use), then enables `window.arti.callTool`. No
   `postMessage`, no opener.

App authors write none of this — it's all in the arti-injected bridge. The same
APP artifact works unchanged whether opened standalone at `/app` (token
injected) or embedded per-user (token via poll); it just calls
`window.arti.callTool(...)`. See the [App SDK](../reference/app-sdk.md).

**Why the consent click is the security control.** A drive-by page can
`window.open` the mint URL, but the consent page renders in the *victim's*
browser: the attacker can't read the challenge (cross-origin popup) or click
Continue for them, and completion is an unprefetchable POST. So a token is
minted only when the human sees who they're authorizing into what and clicks —
the same trade OAuth makes. Defense-in-depth: the surface secret is required at
every step, the completion re-checks read access + `slug_allow`, the token is
marked (`embed-user` scope) and short-lived, and every mint is audit-logged.

## Per-user mode — self-hosting / ingress requirement

The handshake needs two routing facts, which a self-hoster must ensure (arti's
own k8s charts are not part of the public tree):

- **`/auth/embed/*` must sit behind your auth front door** (the same
  oauth2-proxy / built-in `oidc` login that protects `/auth/login` and `/app`),
  so the consent step has a session and can issue a challenge for the real user.
- **`/embed/*` (including `/embed/{surface}/token`) must stay public** (no auth
  proxy), so the sandboxed, opaque-origin app can fetch the completion + poll
  endpoints. They authenticate themselves (surface secret + signed challenge +
  the app's state nonce), never a cookie.

Both auth modes work: the handshake only needs *a protected route that yields
the signed-in user*, which `/auth/login` provides in `proxy` mode (behind
oauth2-proxy) and in built-in `oidc` mode alike.

## Invariants / security

- **Request-level gates are `secret` + `slug_allow`** (plus `email` in service
  mode). `origin` / `frame-ancestors` only governs framing in a browser — it
  does *not* gate a `curl`. The shared secret is a real bearer credential; in
  service mode its blast radius is "artifacts matching `slug_allow` that `email`
  can read." In user mode nothing is served *as* anyone until a viewer completes
  the consent handshake, and every tool call is then bounded by that viewer's
  own access.
- **Service mode: `email` is the authority bound.** Use a dedicated
  least-privilege identity whose read scope equals what you're willing to expose
  to that origin. For `slug_allow: ["*"]` surfaces it is the *only* thing
  bounding reachable artifacts — opt in deliberately. **User mode: the consent
  click is the authority bound** — see [Per-user mode](#per-user-mode-identity-user).
- **Static secret** is what Front's plugin model provides. Stored per-surface in
  a k8s Secret, so a leak is isolated and rotation is an env change.
- **Secrets stay out of logs.** The request logger records the path only (not the
  query), so `auth_secret` isn't logged; the `_files` token is redacted; the
  shell's nested iframe is `referrerpolicy="no-referrer"`.
- **Markdown is rendered without raw-HTML passthrough** — raw HTML is escaped; the
  sandbox is defense in depth.
- No global change to cookie `SameSite`, oauth2-proxy, or any other route's
  framing. Only the embed routes relax framing, and only to the per-surface
  `origin`.

For where this lives in the code, see the [Codebase map](../development/codebase-map.md).
