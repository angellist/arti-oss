---
title: Build & test
order: 3
summary: The make targets that build the binaries and run the Go test tiers, the lint gate (gofmt + go vet + docs-check), the web gates (lint-web, test-web, web-check), the pre-push hook that protects main, and the Node version to use.
---

# Build & test

The gates CI runs are the gates you run locally *before* pushing — a push to
`main` may deploy with no further safety net between you and production. The
pre-push hook enforces the most important ones.

## Build

```sh
make build           # both binaries → bin/arti-server, bin/arti
make build-server    # just the server
make build-cli       # just the CLI (stamped with version + commit via ldflags)
```

`build-cli` injects `main.version` and `main.commit` so `arti version` /
`arti update` can compare the running binary against `origin/main`.

## Test tiers

Go tests are split by build tag, and the web app is excluded from all of them
(`go list ./... | grep -v /web/`).

| Target | What it runs |
| --- | --- |
| `make test-unit` | `go test … -race -count=1` — unit tests, no DB. |
| `make test-integration` | Same, with `-tags integration` — needs Postgres up (`make dev-up`) and the test DB migrated (`make migrate-test`). |
| `make test-e2e` | `-tags 'integration arti_test'` over `tests/e2e/…`. The tree has no `tests/` directory today, so this target finds no packages; use `scripts/e2e-smoke.sh` for end-to-end checks. |
| `make test` | `test-unit` + `test-integration`. |

Integration and e2e tests connect to `arti_test` (a separate DB on the same
dev Postgres). Migrate it once:

```sh
make dev-up
make migrate-test
make test            # unit + integration
```

`scripts/e2e-smoke.sh` is a separate, shell-based smoke that creates a
fresh artifact and checks it across REST, CLI, MCP, and the FE — useful for a quick manual
end-to-end sanity check (see [Local setup](local-setup.md#verify-end-to-end)).

If you can't run a DB, run at minimum `make lint` plus
`go test ./… -count=1` for the packages you touched — and say so explicitly
rather than claiming the full suite passed.

## Lint

```sh
make lint
```

`make lint` is five checks, in order:

1. **`gofmt -l .`** must print nothing. Any unformatted file fails the build.
2. **`go vet ./...`**.
3. **`make docs-check`** — verifies every help diagram has a current committed
   render (below).
4. **`make tenant-scan`** and 5. **`make export-manifest-check`** — repository
   hygiene checks that run only when their scripts are present, and print
   `skipped` otherwise.

`make lint` is Go-only: it needs no `node_modules`, and it does **not** run
golangci-lint — it's gofmt + vet + docs-check plus the two hygiene checks. Don't chase pre-existing errcheck findings that a local golangci-lint
reports; they are not the gate.

### The gofmt struct-tag gotcha

gofmt aligns struct-tag columns across a contiguous field block. Editing one
tag (e.g. a `yaml:"…"` tag in the config structs in `internal/config/config.go`)
can shift the trailing `//` comment column for the *whole* block, so a one-line change can
leave several lines unformatted. After editing struct tags, `gofmt -w` the file
and re-run `make lint` before pushing.

## The pre-push hook

`make hooks` (run by `make setup`) points `core.hooksPath` at `.githooks/`. Its
`pre-push` hook gates pushes whose remote ref is `refs/heads/main`: it runs
`make lint` and `make test-unit`, and blocks the push if either fails. When
`web/node_modules` exists it also runs `make lint-web` and `make test-web`;
when it doesn't, it prints a loud `SKIPPED` note instead of passing silently.
Pushes to any other branch are not gated.

```sh
make hooks                 # one-time on a fresh clone, if make setup wasn't run
git push --no-verify       # emergency override — you own the consequences
```

If a change is risky or touches auth/config/deploy and you can't fully verify it
locally, open a PR instead of pushing to `main`.

## Web checks

The web gates are separate make targets, kept out of `make lint` /
`make test-unit` so a Go-only change never needs `node_modules`:

```sh
make web-install   # cd web && npm ci (once)
make lint-web      # npm run lint (eslint --max-warnings N) + npm run typecheck (tsc --noEmit)
make test-web      # npm test (vitest run)
make web-check     # lint-web + test-web
```

`npm run lint` pins `--max-warnings` to the number of pre-existing findings,
which `web/eslint.config.mjs` records as warnings with the reason for each. Any
new warning fails the build. Fix a warning, then lower the pin; never raise it.

`npm run build` (`npm run build:embed && next build`) first bundles the comments
embed (`embed/comments-embed.ts` → `public/comments-embed.js` via esbuild), then
runs `next build`. CI builds the web Docker image, which runs `next build`
inside it — so a broken `next build` fails the build step.

### Node version

CI runs the web checks in the `node:20-alpine` image (`web/Dockerfile`). The repo
declares no Node version anywhere else (no `engines`, no `.nvmrc`), so use Node
20 locally to match CI.

## Help diagrams (`docs-diagrams` / `docs-check`)

Help pages can embed diagrams authored as Mermaid `.mmd` files next to the
markdown under `web/docs/`. The render step turns them into committed SVGs:

```sh
make docs-diagrams   # renders every web/docs/**/*.mmd → web/public/help-diagrams/**.svg
make docs-check      # verifies every .mmd has a committed .svg (no mmdc needed)
```

- `docs-diagrams` needs `mmdc` (mermaid-cli + headless Chromium) and is an
  author-machine step — it is **not** run in CI.
- `docs-check` is a pure file check folded into `make lint`, so a `.mmd` without
  its rendered SVG, or with an SVG rendered from an older version of the source
  (content hash in `web/public/help-diagrams/.sources`), fails lint and can't
  reach `main`. Add a diagram, run
  `make docs-diagrams`, and commit both files. See
  [Extending arti](extending.md#help-docs) for the authoring flow.
- Fenced `mermaid` blocks in markdown are rendered in the browser at hydration
  time; they do not replace the committed-SVG pipeline for help diagrams.

## What CI runs

CI mirrors the local gates:

| Group | Steps |
| --- | --- |
| Build | builds the `api` and `web` Docker images. |
| Quality | `make lint && make strip-check`; `make test-unit`; `make migrate-test && make test-integration` (with a Postgres sidecar); `npm run lint && npm run typecheck && npm test` in the web test image. |

There is no codegen-diff check in CI — committed generated code is trusted to be
in sync. Keep it that way ([Code generation](codegen.md)).
