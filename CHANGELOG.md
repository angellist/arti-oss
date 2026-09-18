# Changelog

Each release is one snapshot of the arti source tree, cut every two to four
weeks. Versions are `0.MINOR.PATCH` while the project is pre-1.0, so the MINOR
position carries breaking changes: a release moves MINOR when it contains one
and PATCH otherwise.

Sections are generated from the commit subjects of the changes in each snapshot;
entries describe only what ships in this repository.

## 0.1.4 — 2026-09-18

### Features

- apps: ask the viewer before an APP version runs as them
- apps: serve the map tools in-process and turn MAP on in production
- arti: add an admin block list that takes a document away from everyone (DB migrate 0034)
- web: add the Blocked Documents settings page and an admin review route
- web: widen the MAP table, clamp values to three lines, and add a snapshot control

### Fixes

- arti: keep the write access check out of the connection pool
- web: keep a collapsed comment bubble out of the text
- web: shrink the full-page exit to a corner square

### Documentation and performance

- describe the MAP artifact type
- document the run-as consent gate, the block list, and connecting your own MCP servers
- record the blocked-document review surface

## 0.1.3 — 2026-09-15

### Features

- access: store artifact ownership explicitly and make it transferable (DB migrate 0029)
- apps: per-call tool timeout, structured proxy errors, in-flight bound
- apps: raise the upstream response cap to 16 MiB, halve the default in-flight bound
- apps: run every arti read and write in-process for APP artifacts
- auth: record which credential wrote each document, and where each is used (DB migrate 0026)
- comments: add comment write API (DB migrate 0031)
- embed: allow lp-thread-panel on couch-embed, and make refusals frameable
- limits: use one 200 MiB upload body cap on every write path
- notifications: let each person choose their own Slack notifications (DB migrate 0028)
- views: page-view tracking for artifacts, slug-scoped counts in the catalog
- web: apps portal, and move Browse All into the search bar
- web: consolidate the bottom-left user menu into a single top-left Settings entry
- web: dark mode, and Settings as a top-level rail entry
- web: make medium reading width 10% narrower (944px -> 850px)
- web: mobile catalog cards, tighter viewer chrome, and a way out of full page
- web: tell a denied reader what the document is and who owns it
- web: transfer a document's owner from the access modal

### Fixes

- apps: answer upstream failures with 503, which Cloudflare passes through
- apps: name proxy timeouts and oversized upstream responses
- apps: start the per-call budget at the tool call, not at the lookups
- artifacts: pass keepAccess to TransferOwner in the share-mint test
- credusage: notify only for keys on a new network, and harden the usage pipeline (DB migrate 0027)
- dev: pull the MinIO images from quay.io, which still serves them
- list: clamp an oversized limit to the page maximum instead of the default
- opensearch: run the artifacts index with zero replicas
- search: stop the slug-ACL fan-out resurrecting is_latest on every version
- server: redirect trailing-slash paths to their canonical form
- share: decide a disputed lineage from creators, not from the caller
- web: align comment cards with their collapsed bubbles
- web: keep the full-page exit bubble off the content frame's scrollbar
- web: loosen comment body line-height for readability
- web: open the apps portal on recently updated
- web: simplify the artifact viewer header for mobile
- web: size the full-page exit bubble like the comments rail
- web: theme the catalog header rule, and move Upload into the NEW menu

### Documentation and performance

- skills: correct loadArtifact's return shape and the llm.complete token limits
- skills: document callTool timeouts, error codes, and isError results
- warn that a directory upload publishes every file, including .git

## 0.1.2 — 2026-09-01

### Features

- access: make ACLs slug-level — every ACL write applies to all versions (DD-0055)
- arti/comments: edge-mounted comment rail + one icon language
- cli: add `arti edit` for in-place metadata edits
- cli: arti access command, full ACL flags on append, and CLI-doc completeness
- comments: @-mention people in comments and notify them on Slack
- comments: per-doc allow/disallow switch, owner-only, in the viewer ⋮ menu
- embed: gate an embedded document on the viewer's own ACL
- search: index:skip-fulltext label to keep a body out of the index
- settings: Users roster page, and a record for principals arti cannot derive
- share: external timed share links for documents
- upload: accept gzip request bodies so HTML/JS uploads clear the WAF
- upload: drop a file on a document to publish it as the next version
- viewer: scale full-page HTML with the text-size control, add an embed-only xs step
- web: always show the comment count in the single-slug view
- web: collapse artifact frontmatter behind a metadata disclosure
- web: comment overlay — click-to-cycle cards, chip placement, draggable rail
- web: explicit Confirm/Cancel on Edit access, sans-serif emails, API Keys tab first
- web: rework the comment card's three states and composer chrome
- web: suggest known people in the access and group editors

### Fixes

- auth: require explicit consent before /oauth/authorize issues a code
- auth: trust the identity header only where a proxy is declared, and identify oidc-mode users from their session
- embed: keep polling when window.open returns null, for Electron hosts
- logging: stop reporting client disconnects as server errors, and record why real failures happened
- markdown: add optical gap where emphasis abuts a word
- test: keep the skip-fulltext fixtures tenant-clean
- viewer: let the full-page HTML content frame be embedded (frame-ancestors, not XFO)
- viewer: open off-document links from served HTML in a new tab instead of blanking the frame
- web: align ⋮ menu rows on a shared icon slot, drop the divider
- web: clearer env-var instructions in the new-API-key modal
- web: give the comments overlay a dark palette on dark pages
- web: keep page key bindings out of the comment composer
- web: keep the comment composer inside the card border on served pages
- web: make / search and title-click header collapse instant
- web: never offer one query's people as the answer to another
- web: redirect /a/<slug> to /s/<slug> and 404 instead of 500 on the id route
- web: render the version diff one text step smaller
- web: resolve arti-server proxy target at runtime, not at build time

## 0.1.1 — 2026-08-11

### Breaking changes

- auth: rename AUTH_ALLOWED_DOMAINS to AUTH_ALLOWED_EMAILS

### Features

- artifacts: make description editable in place, like labels
- auth: restyle the browser auth pages in arti's brand
- web: align comment card controls into one icon strip
- web: render Mermaid markdown diagrams

### Fixes

- app: show the stale-version strip in the full-screen APP view
- auth: allow full addresses in the email allowlist; resolve `aud` from the auth mode
- auth: let CLI pairing and device approval complete in oidc mode
- observability: make arti's logs tell the truth (healthz noise, 401 attribution, one event per log call)
- opensearch: stop version conflicts leaving stale is_latest flags
- pkgzip: type extensionless package entries from their bytes
- web: make the stale-version notice a thin top strip


## 0.1.0 — 2026-08-04

Initial public release.
