#!/usr/bin/env bash
# Final end-to-end smoke: exercises REST, MCP, CLI, and FE against a
# running local stack. Prerequisites: `make dev-up`, `make migrate`,
# `make build`, `./bin/arti-server serve`, and `cd web && PORT=3031 npm run dev`.
#
# Check style: every assertion ends in `&& ok … || fail …` — a bare
# `check && ok` silently skips under set -e (a failing left side of an
# AND-list does not trigger errexit). Piped greps use `grep … >/dev/null`
# instead of `grep -q`: -q exits at the first match, which can SIGPIPE
# curl mid-stream and fail the pipeline under pipefail even though the
# content matched.
set -euo pipefail

API=${ARTI_API_URL:-http://localhost:8095}
WEB=${ARTI_WEB_URL:-http://localhost:3031}
EMAIL=${ARTI_EMAIL:-e2e-$(date +%s)@example.com}

bold() { printf "\n\033[1m== %s ==\033[0m\n" "$1"; }
ok()   { printf "  \033[32m✓\033[0m %s\n" "$1"; }
fail() { printf "  \033[31m✗\033[0m %s\n" "$1"; exit 1; }
ARTI=./bin/arti

bold "boot checks"
# Identify the server, don't just ping it. $API defaults to a well-known
# port that another local service may already hold, and any healthy HTTP
# server answers /healthz — a foreign 200 used to satisfy this check and the
# run would go on to test a stranger. Two topology-independent tells: arti's
# /healthz is an empty 204, and its AS metadata advertises arti's own scope
# vocabulary. Deliberately NOT compared against $API: `issuer` is
# ARTI_BASE_URL, the public origin (the FE, e.g. :3031), which differs from
# the API address by design — so an equality check here fails on a correctly
# configured server.
HEALTH=$(curl -s -o /dev/null -w "%{http_code}" $API/healthz || echo "000")
[ "$HEALTH" = "204" ] && ok "/healthz (204)" \
  || fail "/healthz: expected 204 from arti at $API, got $HEALTH — is another service on that port? (override with ARTI_API_URL)"
AS_META=$(curl -sf $API/.well-known/oauth-authorization-server || true)
echo "$AS_META" | jq -e '.scopes_supported | index("artifacts:read")' > /dev/null 2>&1 \
  && ok "/.well-known/oauth-authorization-server (issuer $(echo "$AS_META" | jq -r '.issuer // "?"'))" \
  || fail "/.well-known/oauth-authorization-server: no arti scopes in AS metadata from $API — not the arti server we think it is"
curl -sf $WEB/login                                           > /dev/null && ok "FE /login" || fail "FE /login"

bold "auth"
TOK=$(curl -sf -XPOST $API/auth/test -d "{\"email\":\"$EMAIL\"}" | jq -r .access_token)
[ -n "$TOK" ] && ok "issued test token" || fail "issued test token"
curl -sf -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $TOK" $API/api/artifacts | grep 200 > /dev/null && ok "REST authed" || fail "REST authed"
curl -s  -o /dev/null -w "%{http_code}" $API/api/artifacts | grep 401 > /dev/null && ok "REST rejects unauthed" || fail "REST rejects unauthed"

bold "CLI"
ARTI_BASE_URL=$API $ARTI login --email $EMAIL > /dev/null && ok "arti login" || fail "arti login"
ARTI_BASE_URL=$API $ARTI whoami | grep "$EMAIL" > /dev/null && ok "arti whoami" || fail "arti whoami"

SLUG="e2e-$(date +%s%N)"
ARTI_BASE_URL=$API $ARTI add - --title "$SLUG" --slug "$SLUG" --type plain --label e2e <<< "hello e2e" > /tmp/.url 2> /tmp/.meta
URL=$(cat /tmp/.url)
[ -n "$URL" ] && ok "arti add → $URL" || fail "arti add"

ARTI_BASE_URL=$API $ARTI get "$SLUG" -q | grep "hello e2e" > /dev/null && ok "arti get -q" || fail "arti get -q"
ARTI_BASE_URL=$API $ARTI ls --label e2e 2>&1 | grep "$SLUG" > /dev/null && ok "arti ls --label" || fail "arti ls --label"
ARTI_BASE_URL=$API $ARTI search "$SLUG" 2>&1 | grep "$SLUG" > /dev/null && ok "arti search" || fail "arti search"

# PACKAGE flow
PKG_DIR=/tmp/arti-e2e-pkg
rm -rf $PKG_DIR && mkdir -p $PKG_DIR
echo "# E2E skill" > $PKG_DIR/skill.md
mkdir -p $PKG_DIR/ref && echo "ref body" > $PKG_DIR/ref/notes.md
PKG_SLUG="e2e-pkg-$(date +%s%N)"
ARTI_BASE_URL=$API $ARTI add $PKG_DIR --slug "$PKG_SLUG" --title "$PKG_SLUG" > /tmp/.url2 2> /tmp/.meta2
PKG_URL=$(cat /tmp/.url2)
[ -n "$PKG_URL" ] && ok "arti add (dir → PACKAGE) → $PKG_URL" || fail "arti add (dir → PACKAGE)"

ARTI_BASE_URL=$API $ARTI get "$PKG_SLUG/skill.md" | grep "E2E skill" > /dev/null && ok "arti get slug/path (entry)" || fail "arti get slug/path (entry)"
ARTI_BASE_URL=$API $ARTI get "$PKG_SLUG/ref/notes.md" | grep "ref body" > /dev/null && ok "arti get slug/path (nested)" || fail "arti get slug/path (nested)"
rm -rf /tmp/arti-e2e-out
ARTI_BASE_URL=$API $ARTI get "$PKG_SLUG" --extract /tmp/arti-e2e-out -q > /dev/null
[ -f /tmp/arti-e2e-out/ref/notes.md ] && ok "arti get --extract" || fail "arti get --extract"

bold "REST"
H="Authorization: Bearer $TOK"
ID=$(curl -sf -H "$H" $API/api/artifacts/by-slug/$SLUG | jq -r .artifact_id)
curl -sf -H "$H" $API/api/artifacts/$ID/meta | jq -e ".artifact_id==\"$ID\"" > /dev/null && ok "GET /meta" || fail "GET /meta"
curl -sf -H "$H" $API/api/artifacts/$ID | grep "hello e2e" > /dev/null && ok "GET /content" || fail "GET /content"
curl -sf -H "$H" "$API/api/artifacts/search?q=$SLUG" | jq -e ".total > 0" > /dev/null && ok "search" || fail "search"

PKG_ID=$(curl -sf -H "$H" $API/api/artifacts/by-slug/$PKG_SLUG | jq -r .artifact_id)
curl -sf -H "$H" $API/api/artifacts/$PKG_ID/files | jq -e ".entries|length > 0" > /dev/null && ok "PACKAGE /files" || fail "PACKAGE /files"
curl -sf -H "$H" $API/api/artifacts/$PKG_ID/files/skill.md | grep "E2E skill" > /dev/null && ok "PACKAGE /files/{path}" || fail "PACKAGE /files/{path}"

bold "MCP"
T_LIST=$(curl -sf -H "$H" -H "Content-Type: application/json" $API/mcp \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}')
echo "$T_LIST" | jq -e '.result.tools | length >= 9' > /dev/null && ok "MCP tools/list (≥9)" || fail "MCP tools/list (≥9)"

MCP_CALL='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_artifact","arguments":{"ident":"'$SLUG'"}}}'
curl -sf -H "$H" -H "Content-Type: application/json" $API/mcp -d "$MCP_CALL" | jq -e ".result.content[0].text | fromjson | .title == \"$SLUG\"" > /dev/null && ok "MCP get_artifact" || fail "MCP get_artifact"

MCP_READ='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read_package_file","arguments":{"ident":"'$PKG_SLUG'","path":"skill.md"}}}'
curl -sf -H "$H" -H "Content-Type: application/json" $API/mcp -d "$MCP_READ" | jq -e '.result.content[0].text | contains("E2E skill")' > /dev/null && ok "MCP read_package_file" || fail "MCP read_package_file"

bold "FE"
# Catalog with session cookie
curl -sf -b "arti_session=$TOK" $WEB/ 2>&1 | grep "$SLUG" > /dev/null && ok "FE catalog shows artifact" || fail "FE catalog shows artifact"
# Viewer by id, by slug, by version
curl -sf -b "arti_session=$TOK" $WEB/a/$ID 2>&1 | grep "$SLUG" > /dev/null && ok "FE /a/<uuid>" || fail "FE /a/<uuid>"
curl -sf -b "arti_session=$TOK" $WEB/s/$SLUG 2>&1 | grep "$SLUG" > /dev/null && ok "FE /s/<slug>" || fail "FE /s/<slug>"
curl -sf -b "arti_session=$TOK" $WEB/s/$SLUG/v_1 2>&1 | grep "$SLUG" > /dev/null && ok "FE /s/<slug>/v_1" || fail "FE /s/<slug>/v_1"
# PACKAGE viewer with file tree
curl -sf -b "arti_session=$TOK" $WEB/s/$PKG_SLUG 2>&1 | grep "skill.md" > /dev/null && ok "FE PACKAGE viewer (file tree)" || fail "FE PACKAGE viewer (file tree)"
# Login redirect
curl -s  -o /dev/null -w "%{http_code}" $WEB/ | grep 307 > /dev/null && ok "FE redirects to /login when unauth" || fail "FE redirects to /login when unauth"

bold "cleanup"
ARTI_BASE_URL=$API $ARTI rm "$SLUG" -y > /dev/null 2>&1 && ok "arti rm slug" || fail "arti rm slug"
ARTI_BASE_URL=$API $ARTI rm "$PKG_SLUG" -y > /dev/null 2>&1 && ok "arti rm package slug" || fail "arti rm package slug"

printf "\n\033[1;32m===== E2E SUCCESS =====\033[0m\n"
