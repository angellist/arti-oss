---
title: Using the API
order: 3
summary: Call arti's HTTP API from a service, script, or agent — the credential menu (session, CLI login, device flow, API key, service secret), how to mint a self-serve upload API key, and a first request.
---

# Using the API

Everything arti does is an HTTP call. The web UI, the CLI, and the MCP server all
sit on top of the same REST surface at `https://arti.example.com`, and
you can drive it directly from any language that can make an HTTPS request.

This guide is about **getting access** — which credential to use and how to get
one. For the endpoint patterns (create, append, fetch, list, errors) see
[As an API consumer](../recipes/api-consumer.md); for the exhaustive route list
see the [REST API reference](../reference/rest-api.md).

Base URL in production: `https://arti.example.com`. Every `/api/*`
route is authenticated — you send `Authorization: Bearer <token>` (or, in a
browser, the `arti_session` cookie).

## Getting a token — the credential menu

There are five ways to authenticate, in two families. **Full-access** credentials
act as you across the whole API (subject to each artifact's read rules).
**Upload-scoped** credentials are deliberately weak: create, append, and read
artifacts, nothing else.

| Credential | For | How you get it | Lifetime | Scope |
|---|---|---|---|---|
| `arti_session` cookie | Humans in the web app | Automatic after SSO login | 7 days | Full |
| CLI login | A person at a terminal | `arti login` (browser PKCE) | 7-day access, 90-day refresh | Full |
| **API key** | A service/script you own | Self-serve — [Settings → API Keys](#api-keys-self-serve) or `POST /api/keys` | Up to 365 days (default 90); no auto-refresh | **Upload only** |
| **Device flow** | A headless agent needing a human to vouch | [RFC 8628 device flow](../recipes/agent.md) — human approves once | 24-hour access (+30-day refresh for a standing agent) | **Upload only** |
| Service secret | Service-to-service backends | Operator sets `ARTI_SERVICE_SECRET`; send `X-Arti-Service-Secret` | Static | Service principal |

Both upload-scoped credentials run through the **same** default-deny guard
(`EnforceUploadScope`): they may only `GET /api/me`, read artifacts
(`GET /api/artifacts…`), create (`POST /api/artifacts`), and append
(`POST /api/artifacts/by-slug/{slug}/append`) — everything else is `403`, even for
an admin. Both are also capped at **25 MiB per request** body
(`ARTI_DEVICE_MAX_UPLOAD_BYTES`). See [Auth architecture](../architecture/auth.md).

### Which one when

- **You're a person at a terminal** → `arti login` (the [CLI](command-line.md)), or
  just use the [web UI](web-ui.md).
- **You run and own a long-lived service or script that uploads** (a nightly job,
  a dashboard publisher) → mint an **API key**. It's the least ceremony: no
  browser, no refresh dance, tied to you, revocable.
- **You're a headless agent in a platform you don't fully control** (Runlayer,
  Devin, a CI sandbox) → the **device flow** leases a temporary token after a
  human approves once; or drop your own **API key** into `ARTI_TOKEN`. Either way
  you upload large payloads over plain HTTP, which keeps the bytes **out of the
  model's context window** (unlike the MCP `add_artifact` tool, which routes
  content through the model) — cheaper and more reliable for anything big.
- **You're an internal backend calling arti machine-to-machine** → the service
  secret.

## API keys (self-serve) {#api-keys-self-serve}

An API key is a long-lived, personal, **upload-only** bearer — the low-ceremony
way to let a service you own publish to arti. Every key is **tied to you**: all
uploads are attributed to your email, exactly as if you'd run them yourself. In v1
the only scope is `upload`, so a key can create, append, and read — never delete,
edit metadata, manage access, or reach admin/MCP.

Keys look like `arti_upload_XXXXX…` and are shown **once**, at creation — arti
stores only a SHA-256 hash, so it can never show you the secret again. Lost it?
Revoke and mint a new one.

### Create one in the UI

Go to **Settings → API Keys** (`/settings/keys` — the tab is available to
everyone). Give the key a **name** (e.g. `ci-upload`), leave the scope on
**Upload**, pick an expiry (30 / 90 / 180 / 365 days; default 90), and create. The
full key is revealed once in a copy-me dialog — copy it immediately. The list
below shows each key's prefix, owner, created/expiry dates, last-used time, and
status (Active / Expired / Revoked), with a Revoke button.

A yellow badge appears on your user menu when any active key is within **7 days**
of expiring — keys do **not** auto-refresh, so rotate before then (mint a new key,
swap it into `ARTI_TOKEN`, revoke the old one).

### Create one over REST

```sh
curl -sX POST https://arti.example.com/api/keys \
  -H "Authorization: Bearer $SESSION_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{ "name": "ci-upload", "scopes": ["upload"], "ttl_days": 90 }'
```

The `201` response carries the plaintext key **once**, as `key`:

```json
{
  "id": "…uuid…",
  "name": "ci-upload",
  "key_prefix": "arti_upload_AbCdE",
  "owner_email": "you@example.com",
  "scopes": ["upload"],
  "created_at": "2026-07-02T…Z",
  "expires_at": "2026-09-30T…Z",
  "last_used_at": null,
  "revoked_at": null,
  "key": "arti_upload_…full-secret-shown-once…"
}
```

Minting a key requires an existing full-access credential (a session cookie or a
CLI token) — an upload-scoped credential can't mint more keys. List your keys with
`GET /api/keys` and revoke one with `DELETE /api/keys/{id}`. Full shapes:
[REST API → API keys](../reference/rest-api.md#api-keys).

### Use it

The key is a plain bearer. Anywhere you'd use a token, use the key:

```sh
export ARTI_TOKEN=arti_upload_…
# the CLI picks up ARTI_TOKEN automatically:
arti add report.md --slug nightly-eval --title "Nightly eval $(date +%F)" --label run-record
# or raw curl:
curl -sX POST https://arti.example.com/api/artifacts \
  -H "Authorization: Bearer $ARTI_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{ "artifact_type":"TEXT","named_slug":"nightly-eval","title":"Nightly eval",
        "content_type":"text/markdown","content":"# Results\n" }'
```

**Uploading HTML or JavaScript?** Gzip the request body and send
`Content-Encoding: gzip`. Cloudflare's WAF inspects request bodies and
false-matches its `<script>` rule on inline HTML/JS, returning `403` before the
request reaches arti — a gzipped body carries no matchable markup, and the server
decompresses it transparently. Do this for **any** HTML/JS upload, even a single
file; PACKAGE/APP zips are already compressed and need nothing. The `arti` CLI and
the web upload gzip textual uploads automatically; over raw `curl`:

```sh
jq -n --arg c "$(base64 < report.html)" \
  '{artifact_type:"TEXT",content_type:"text/html",named_slug:"q3-report",title:"Q3 Report",content_base64:$c}' \
  | gzip | curl -sX POST https://arti.example.com/api/artifacts \
      -H "Authorization: Bearer $ARTI_TOKEN" \
      -H 'Content-Type: application/json' -H 'Content-Encoding: gzip' \
      --data-binary @-
```

## Device flow (headless agents)

When a human needs to vouch for a browserless agent — a couch sandbox, a Runlayer
task — arti implements the [RFC 8628 device authorization grant](../recipes/agent.md):
the agent prints a short code, a human approves it once through SSO, and the agent
polls for an upload-scoped `ARTI_TOKEN`. It's the right choice when the identity
should be a *specific approving human* and the access should be *temporary*. The
full walkthrough (poll loop, refresh, revocation) is in
[As an agent (headless)](../recipes/agent.md).

## A first request

With any token in `$TOKEN`, confirm who you are, then read the catalog:

```sh
# identity + effective permissions
curl -s -H "Authorization: Bearer $TOKEN" \
  https://arti.example.com/api/me

# newest 10 artifacts you can read
curl -s -H "Authorization: Bearer $TOKEN" \
  'https://arti.example.com/api/artifacts?order_by=created&order_dir=desc&limit=10'
```

From here, the [API consumer recipe](../recipes/api-consumer.md) walks through
creating, appending, fetching, listing, and error handling with real `curl`.

## Related

- [As an API consumer](../recipes/api-consumer.md) — the endpoint tour.
- [As an agent (headless)](../recipes/agent.md) — the full device-flow walkthrough.
- [REST API reference](../reference/rest-api.md) — every route, param, and field.
- [CLI](../reference/cli.md) — the `arti` client and `ARTI_TOKEN`.
- [Auth architecture](../architecture/auth.md) — how each credential is verified and scoped.
- [Configuration](../reference/configuration.md#auth) — the env vars that gate auth and limits.
