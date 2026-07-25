---
title: As an agent (headless)
order: 1
summary: Get a long-lived ARTI_TOKEN over the RFC 8628 device flow, then upload and fetch artifacts from a browserless sandbox with the CLI or raw curl.
---

# As an agent (headless)

You are an autonomous agent, a CI job, a Runlayer task, or a sandbox with no
browser and no interactive login. You need to publish what you produce — a
report, a screenshot, a build artifact — back to arti so a human (or another
agent) can read it at a stable URL.

The path: a human approves you **once** through the device flow, you store the
resulting `ARTI_TOKEN`, and every subsequent upload is a bearer-authenticated
`POST`. The token is deliberately weak — it can create, append, and read
artifacts and nothing else (see [Scope: what an upload token can do](#scope-what-an-upload-token-can-do)).

Production base URL: `https://arti.example.com`. All paths below are
relative to it.

> **Device flow or API key?** If you can pre-provision the agent yourself, a self-serve
> [API key](../guides/api.md#api-keys-self-serve) is simpler — mint one, drop it into
> `ARTI_TOKEN`, and skip the human-approval round trip entirely. It carries the same
> `upload` scope. Use the device flow instead when a human must approve at run time, or
> when the agent should act as *that approving human's* identity rather than yours.

## Get a token via the device flow

arti implements [RFC 8628](https://www.rfc-editor.org/rfc/rfc8628) (OAuth 2.0
Device Authorization Grant). The agent never sees a browser; it prints a short
code, a human approves it from their own machine, and the agent polls for the
token. Handlers live in `internal/auth/device.go`, wired in
`cmd/arti-server/cmd_serve.go`.

### 1. Request a device code

```sh
curl -sX POST https://arti.example.com/auth/device/code \
  -H 'Content-Type: application/json' \
  -d '{"duration":"long"}'
```

`duration` is `"short"` (default) or `"long"`. **short** mints a single 24h
access token with no refresh; **long** mints a 24h access token plus a rotating
30-day refresh token. Use `long` for a standing agent, `short` for a one-shot
job.

Response (`200`):

```json
{
  "device_code": "kQ8…32-byte-base64url-secret…",
  "user_code": "BK4P-7QXM",
  "verification_uri": "https://arti.example.com/auth/device",
  "verification_uri_complete": "https://arti.example.com/auth/device?user_code=BK4P-7QXM",
  "expires_in": 600,
  "interval": 5
}
```

- `device_code` — your polling secret. Never print it; it's not for the human.
- `user_code` — show this to the human (the `XXXX-XXXX` form omits I/L/O/U/0/1).
- `verification_uri_complete` — the link to hand the human.
- `expires_in` — the grant window is **10 minutes**. Approve within it or start over.
- `interval` — poll no faster than every **5 seconds**.

### 2. Human approves

Print the line and wait:

```
Approve this agent at https://arti.example.com/auth/device?user_code=BK4P-7QXM
```

The human opens it, signs in through SSO, sees the code and the requested
duration, and clicks **Sign in & Approve**. The approval is bound to their
SSO-verified email — the agent inherits *that human's* identity and access.

### 3. Poll for the token

```sh
curl -sX POST https://arti.example.com/auth/device/token \
  -H 'Content-Type: application/json' \
  -d '{"device_code":"kQ8…"}'
```

Until approved you get `428` with `{"error":"authorization_pending"}` — sleep
`interval` and retry. Terminal errors: `400 expired_token` (grant lapsed or
already picked up), `403 access_denied` (the approver's email isn't in an
allowed domain).

On success (`200`), for `duration:"long"`:

```json
{
  "access_token": "eyJ…",
  "refresh_token": "eyJ…",
  "token_type": "Bearer",
  "expires_in": 86400,
  "refresh_expires_in": 2592000,
  "scope": "upload",
  "email": "agent-owner@example.com"
}
```

For `duration:"short"` the response omits `refresh_token` and
`refresh_expires_in`. The `access_token` is your `ARTI_TOKEN`.

A minimal poll loop:

```sh
DC=$(curl -sX POST $BASE/auth/device/code -d '{"duration":"long"}' \
  -H 'Content-Type: application/json')
DEVICE_CODE=$(jq -r .device_code <<<"$DC")
echo "Approve: $(jq -r .verification_uri_complete <<<"$DC")"

while :; do
  R=$(curl -s -o body.json -w '%{http_code}' -X POST $BASE/auth/device/token \
    -H 'Content-Type: application/json' -d "{\"device_code\":\"$DEVICE_CODE\"}")
  [ "$R" = "200" ] && break
  [ "$R" = "428" ] && { sleep 5; continue; }
  echo "device flow failed ($R): $(cat body.json)"; exit 1
done
export ARTI_TOKEN=$(jq -r .access_token body.json)
```

### Refresh (long-lived agents)

Before the 24h access token expires, rotate it with the refresh token. Refresh
tokens are single-use; each call returns a new pair (rotation defends against a
leaked token being replayed).

```sh
curl -sX POST https://arti.example.com/auth/device/refresh \
  -H 'Content-Type: application/json' \
  -d '{"refresh_token":"eyJ…"}'
```

Returns a fresh `access_token` + `refresh_token`. The refresh chain is capped at
**30 days** from first issue; after that the human re-approves. Re-approving a
user revokes their prior token family — there is one active family per user.

## Use the token

Two ways to authenticate — both send `Authorization: Bearer <token>`.

### Option A — the CLI

The CLI honors `ARTI_TOKEN` and skips login entirely when it's set
(`cmd/arti/token.go`). This takes precedence over any cached on-disk login.

```sh
export ARTI_TOKEN=eyJ…
arti add report.md --slug nightly-eval --title "Nightly eval $(date +%F)" --label run-record
```

See the [CLI reference](../reference/cli.md) for the full command surface. Set
`ARTI_BASE_URL` if you're pointing at a non-default server.

### Option B — raw curl

No CLI install in the sandbox? Hit the REST API directly. A headless upload of a
TEXT artifact:

```sh
curl -sX POST https://arti.example.com/api/artifacts \
  -H "Authorization: Bearer $ARTI_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "artifact_type": "TEXT",
    "named_slug": "nightly-eval",
    "title": "Nightly eval 2026-06-19",
    "content_type": "text/markdown",
    "content": "# Results\n\nPass rate: 94%\n",
    "labels": ["run-record", "eval"]
  }'
```

Response is the created artifact's metadata (`201`), including its `url`. Posting
to the same `named_slug` again creates the next version automatically.

For a **binary** payload (screenshot, PDF, zip) use multipart — no base64
round-trip:

```sh
curl -sX POST https://arti.example.com/api/artifacts \
  -H "Authorization: Bearer $ARTI_TOKEN" \
  -F 'file=@screenshot.png' \
  -F 'title=Run screenshot' \
  -F 'content_type=image/png' \
  -F 'artifact_type=ATTACHMENT' \
  -F 'labels=run-record'
```

To **append** to a running log (idempotent, retry-safe) use the by-slug append
endpoint — see [api-consumer](api-consumer.md#append-to-a-running-log) for the
idempotency-key pattern, which matters for agents that retry after a crash.

Fetch is just a `GET`:

```sh
# metadata
curl -s -H "Authorization: Bearer $ARTI_TOKEN" \
  https://arti.example.com/api/artifacts/by-slug/nightly-eval
# raw content
curl -s -H "Authorization: Bearer $ARTI_TOKEN" \
  https://arti.example.com/api/artifacts/by-slug/nightly-eval/raw
```

## Scope: what an upload token can do

A device-flow token carries the `upload` scope (`internal/auth/jwt.go`), and the
server enforces a **default-deny allowlist** for it in `EnforceUploadScope`
(`internal/auth/scope_guard.go`). Even an admin's upload token is downgraded to
exactly these operations:

| Allowed | Method + path |
|---|---|
| Identity check | `GET /api/me` |
| Read any artifact | `GET /api/artifacts*` |
| Create an artifact | `POST /api/artifacts` |
| Append a version | `POST /api/artifacts/by-slug/{slug}/append` |

Everything else — `DELETE`, `PATCH`, `suggest-metadata`, MCP, admin, comments —
is `403`. Two more guardrails apply on every upload-scoped request:

- **Body cap.** Upload requests are limited to 25 MiB by default
  (`ARTI_DEVICE_MAX_UPLOAD_BYTES`); overflow is `413`. This is tighter than the
  200 MiB global create limit because upload tokens are meant for modest sandbox
  output.
- **Live revocation.** A long-lived token's family is re-checked on every call.
  Revoke it (or let it expire) and the token stops working immediately, before
  its 24h access JWT would otherwise lapse.

Keep `ARTI_TOKEN` out of logs, commits, and screenshots. It is a personal,
per-owner capability — treat it like a password.

## Related

- [REST API reference](../reference/rest-api.md) — the full endpoint surface.
- [CLI reference](../reference/cli.md) — every command and flag.
- [Auth architecture](../architecture/auth.md) — scopes, the device flow, and how `RequireAuth` gates routes.
- [As an API consumer](api-consumer.md) — pagination, filtering, idempotent append.
