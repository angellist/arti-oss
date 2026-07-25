---
title: Build & test
order: 3
summary: The make targets that build the binaries and run the Go test tiers, the lint gate (gofmt + go vet + docs-check), the pre-push hook that protects main, and how to run the web checks.
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
| `make test-e2e` | `-tags 'integration arti_test'` over `tests/e2e/…` — full-stack assertions. |
| `make test` | `test-unit` + `test-integration`. |

Integration and e2e tests connect to `arti_test` (a separate DB on the same
dev Postgres). Migrate it once:

```sh
make dev-up
make migrate-test
make test            # unit + integration
```

`scripts/e2e-smoke.sh` is a separate, shell-based 27-check smoke that boots a
fresh artifact across REST, CLI, MCP, and the FE — useful for a quick manual
end-to-end sanity check (see [Local setup](local-setup.md#verify-end-to-end)).

If you can't run a DB, run at minimum `make lint` plus
`go test ./… -count=1` for the packages you touched — and say so explicitly
rather than claiming the full suite passed.

## Lint

```sh
make lint
```

`make lint` is three checks, in order:

1. **`gofmt -l .`** must print nothing. Any unformatted file fails the build.
2. **`go vet ./...`**.
3. **`make docs-check`** — verifies every help diagram has a committed render
   (below).

`make lint` does **not** run golangci-lint — it's gofmt + vet + docs-check
only. Don't chase pre-existing errcheck findings that a local golangci-lint
reports; they are not the gate.

### The gofmt struct-tag gotcha

gofmt aligns struct-tag columns across a contiguous field block. Editing one
`env:"…" default:"…"` tag (e.g. in the config struct in `cmd_serve.go`) can shift
the trailing `//` comment column for the *whole* block, so a one-line change can
leave several lines unformatted. After editing struct tags, `gofmt -w` the file
and re-run `make lint` before pushing.

## The pre-push hook

`make hooks` (run by `make setup`) points `core.hooksPath` at `.githooks/`. Its
`pre-push` hook gates pushes whose remote ref is `refs/heads/main`: it runs
`make lint` and `make test-unit`, and blocks the push if either fails. Pushes to
any other branch are not gated.

```sh
make hooks                 # one-time on a fresh clone, if make setup wasn't run
git push --no-verify       # emergency override — you own the consequences
```

If a change is risky or touches auth/config/deploy and you can't fully verify it
locally, open a PR instead of pushing to `main`.

## Web checks

The web app has its own toolchain (run from `web/`):

```sh
cd web
npm run lint     # eslint
npm run test     # vitest run
npm run build    # npm run build:embed && next build
```

`npm run build` first bundles the comments embed (`embed/comments-embed.ts` →
`public/comments-embed.js` via esbuild), then runs `next build`.

CI builds the web Docker image, which runs `next build` inside it — so a broken
`next build` fails the build step. eslint and vitest are **not** necessarily
wired into every CI setup; run them locally.

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
  its rendered SVG fails lint and can't reach `main`. Add a diagram, run
  `make docs-diagrams`, and commit both files. See
  [Extending arti](extending.md#help-docs) for the authoring flow.

## What CI runs

CI mirrors the local gates:

| Group | Steps |
| --- | --- |
| Build | builds the `api` and `web` Docker images. |
| Quality | `make lint`; `make test-unit`; `make migrate-test && make test-integration` (with a Postgres sidecar). |

There is no codegen-diff check in CI — committed generated code is trusted to be
in sync. Keep it that way ([Code generation](codegen.md)).
