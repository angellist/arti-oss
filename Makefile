.PHONY: setup hooks dev-up dev-down dev-reset build build-cli build-server generate test test-unit test-integration test-e2e lint lint-web test-web web-check web-install migrate migrate-test psql server cli docs-diagrams docs-check

# Stamp the CLI build with its version + commit so `arti version` / the update
# check can compare against origin/main. git describe gives the short SHA when
# there are no tags; rev-parse gives the full commit used for the staleness check.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo "")
CLI_LDFLAGS = -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT)"

setup: hooks
	@command -v sqlc >/dev/null  || (echo "install sqlc"  && exit 1)
	@command -v goose >/dev/null || (echo "install goose" && exit 1)
	@command -v oapi-codegen >/dev/null || (echo "install oapi-codegen" && exit 1)
	go mod tidy

# Route git hooks to the tracked .githooks dir so the pre-push gate
# (lint + unit tests before a push to main) applies to everyone.
hooks:
	git config core.hooksPath .githooks
	@echo "git hooks installed (.githooks); pre-push gates pushes to main."

dev-up:
	docker compose -f deployments/docker-compose/docker-compose.yml up -d
	@echo "Postgres :5436, MinIO :9210 (console :9211)"

dev-down:
	docker compose -f deployments/docker-compose/docker-compose.yml down

dev-reset:
	docker compose -f deployments/docker-compose/docker-compose.yml down -v

migrate:
	$(MAKE) -C db migrate

migrate-test:
	$(MAKE) -C db migrate-test

psql:
	$(MAKE) -C db psql

generate:
	sqlc generate
	oapi-codegen --config=api/openapi/oapi-codegen.yaml api/openapi/openapi.yaml

build-server:
	mkdir -p bin
	go build -o bin/arti-server ./cmd/arti-server

build-cli:
	mkdir -p bin
	go build $(CLI_LDFLAGS) -o bin/arti ./cmd/arti

build: build-server build-cli

server: build-server
	./bin/arti-server serve

test-unit:
	go test $$(go list ./... | grep -v /web/) -race -count=1

test-integration:
	go test -tags integration $$(go list ./... | grep -v /web/) -race -count=1

test-e2e:
	go test -tags 'integration arti_test' ./tests/e2e/... -race -count=1

test: test-unit test-integration

# The web gates, matching the CI :react: Web Quality step exactly. Kept
# separate from `lint` / `test-unit` on purpose: those two are what the
# pre-push hook runs, and they must not start requiring node_modules for
# someone pushing a Go-only change. Run `make web-install` once first.
web-install:
	cd web && npm ci

lint-web:
	cd web && npm run lint && npm run typecheck

test-web:
	cd web && npm test

web-check: lint-web test-web

lint:
	gofmt -l . | (! grep .) || (echo "run gofmt" && exit 1)
	go vet ./...
	$(MAKE) docs-check
	$(MAKE) tenant-scan
	$(MAKE) export-manifest-check

# Render committed mermaid diagrams. Author-machine only (needs mmdc /
# headless Chromium); NOT run in CI. Outputs to web/public/help-diagrams/
# so Next serves them at /help-diagrams/<flattened>.svg. Source .mmd files
# live next to their docs under web/docs/. Also records each source's
# content hash in the manifest so docs-check can detect a stale .svg.
docs-diagrams:
	@command -v mmdc >/dev/null || (echo "install mermaid-cli (mmdc)" && exit 1)
	@mkdir -p web/public/help-diagrams
	@: > web/public/help-diagrams/.sources
	@find web/docs -name '*.mmd' | sort | while read -r f; do \
		rel="$${f#web/docs/}"; \
		out="web/public/help-diagrams/$${rel%.mmd}.svg"; \
		mkdir -p "$$(dirname "$$out")"; \
		echo "mmdc $$f -> $$out"; \
		mmdc -i "$$f" -o "$$out" -b transparent || exit 1; \
		echo "$$(git hash-object "$$f")  $$rel" >> web/public/help-diagrams/.sources; \
	done

# Verify every .mmd has a committed rendered .svg that is up to date with its
# source. Pure check (no mmdc) — safe for CI / fresh clones. The recorded
# content hash (git hash-object, deterministic) must match the current source,
# so an edited .mmd with a stale committed .svg fails the gate. Fails loud so
# diagrams can't silently rot.
docs-check:
	@manifest=web/public/help-diagrams/.sources; \
	bad=0; \
	for f in $$(find web/docs -name '*.mmd' 2>/dev/null | sort); do \
		rel="$${f#web/docs/}"; \
		out="web/public/help-diagrams/$${rel%.mmd}.svg"; \
		if [ ! -f "$$out" ]; then echo "MISSING rendered diagram: $$out (run 'make docs-diagrams')"; bad=1; continue; fi; \
		want="$$(git hash-object "$$f")"; \
		got="$$(awk -v r="$$rel" '$$2==r {print $$1}' "$$manifest" 2>/dev/null)"; \
		if [ "$$want" != "$$got" ]; then echo "STALE diagram for $$rel: source changed since render (run 'make docs-diagrams')"; bad=1; fi; \
	done; \
	[ $$bad -eq 0 ] || exit 1
	@echo "docs-check: all diagrams rendered and current"

# ---- OSS Workstream 2 gates -------------------------------------------
# Tenant identifiers may live only under angellist/ (see the scan script
# for the full allowlist). Runs as part of `make lint`. The script is not
# exported (its patterns are the tenant identifiers), so the target no-ops
# in the public tree.
tenant-scan:
	@if [ -x scripts/check-tenant-clean.sh ]; then \
		./scripts/check-tenant-clean.sh; \
	else \
		echo "tenant-scan: skipped (internal-only check)"; \
	fi

# Every git-tracked path must be classified copy/drop/template/add in the
# export manifest, so a new path forces an explicit export decision. The
# manifest and checker live under oss/ (not exported), so the target no-ops
# in the public tree.
export-manifest-check:
	@if [ -x oss/check-manifest.sh ]; then \
		./oss/check-manifest.sh; \
	else \
		echo "export-manifest-check: skipped (internal-only check)"; \
	fi

# The public tree must build without the tenant directory: export the
# committed tree, delete angellist/, and build+vet what remains.
strip-check:
	rm -rf /tmp/arti-strip-check && mkdir -p /tmp/arti-strip-check
	git archive HEAD | tar -x -C /tmp/arti-strip-check
	rm -rf /tmp/arti-strip-check/angellist
	cd /tmp/arti-strip-check && go build ./... && go vet ./...
	@echo "strip-check: tree builds without angellist/"
