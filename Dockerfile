# syntax=docker/dockerfile:1.7-labs

# ==============================================================================
# BASE STAGE
# ==============================================================================
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS base

ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

RUN apk add --no-cache git ca-certificates tzdata make

# All Go dependencies are public (the module path is the only
# github.com/angellist reference in go.mod), so modules come from the Go
# proxy over HTTPS — no SSH agent, GOPRIVATE, or git rewrite needed.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# ==============================================================================
# SERVER STAGE
# ==============================================================================
FROM base AS server

ARG VERSION=dev

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/arti-server ./cmd/arti-server

FROM alpine:3.23 AS api

RUN apk add --no-cache ca-certificates tzdata wget

RUN addgroup -g 1000 appgroup && \
    adduser -u 1000 -G appgroup -s /bin/sh -D appuser

WORKDIR /app

COPY --from=server /out/arti-server /app/arti-server
COPY db/migrations /app/db/migrations

# Numeric UID (not the username "appuser") so k8s Pod Security Standards
# can statically verify runAsNonRoot at admission time.
USER 1000

EXPOSE 8090

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8090/healthz || exit 1

ENTRYPOINT ["/app/arti-server"]
CMD ["serve"]

# ==============================================================================
# TEST IMAGE
# Pre-built image for CI lint/test jobs. Tests are NOT run inside this
# image — they run as jobs with a Postgres sidecar; this image just
# bundles the Go toolchain, source, and goose so `make lint`,
# `make test-unit`, `make migrate-test`, and `make test-integration`
# work without further installs.
# ==============================================================================
FROM base AS test

# gcc + musl-dev: required for `go test -race` (cgo). bash: Makefile recipes.
RUN apk add --no-cache gcc musl-dev bash

# goose binary for `make migrate-test`. Pinned to the version in go.mod.
RUN go install github.com/pressly/goose/v3/cmd/goose@v3.27.1

# ==============================================================================
# WEB
# Frontend is built and served by the arti-web sidecar; the build is
# defined in web/Dockerfile. The CI pipeline targets that file directly
# (build_step_v3 with context: 'web'), so this stage exists only as a
# placeholder for symmetry with the api stage.
# ==============================================================================
FROM alpine:3.23 AS web

COPY web/Dockerfile /opt/web-dockerfile-reference
CMD ["/bin/sh", "-c", "echo 'see web/Dockerfile for the arti-web image'"]
