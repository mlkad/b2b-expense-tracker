# syntax=docker/dockerfile:1.7

# Two binaries, one Dockerfile. Which one an image runs is chosen by build
# stage, not by an environment variable - scratch has no shell, so there is
# nothing at runtime that could read one.
#
#   docker build --target api    -t expense-api .
#   docker build --target worker -t expense-worker .
#
# Both binaries are compiled either way. The build cache is shared between
# them, so the second costs almost nothing once the first has warmed it, and
# building both means a single `docker build` proves they both still compile.

# ---------------------------------------------------------------------------
# Build
# ---------------------------------------------------------------------------

# Pinned to a patch release rather than a minor line. The standard library is a
# dependency like any other, and "1.26" floats onto whatever the builder has
# cached - which is how an image silently picks up a toolchain with known CVEs.
# govulncheck in CI is what catches that; this is what prevents it.
FROM golang:1.26.6-alpine AS build

# git is needed for the version stamp. ca-certificates and tzdata are installed
# purely so their data files can be copied into the final image - alpine ships
# neither the CA bundle nor /usr/share/zoneinfo by default, and scratch has no
# package manager to add them later.
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Dependencies first, in their own layer. Application code changes on every
# commit and the module graph changes rarely, so this layer survives almost
# every rebuild.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# CGO_ENABLED=0 produces a static binary, which is what lets the final stage be
# a scratch-like image with no libc at all.
#
# -trimpath keeps the build machine's directory layout out of the binary: paths
# in a panic trace otherwise name the builder's filesystem, and the binary is
# not reproducible between machines.
#
# -w -s drop DWARF and the symbol table. That costs readable stack traces in a
# debugger and saves roughly a third of the image; the panic traces the service
# actually logs still carry function names.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-w -s -X main.version=${VERSION}" -o /out/api ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-w -s -X main.version=${VERSION}" -o /out/worker ./cmd/worker

# ---------------------------------------------------------------------------
# Runtime
# ---------------------------------------------------------------------------

# scratch, not alpine. There is no shell, no package manager and no libc, so
# there is nothing for an attacker who reaches code execution to pivot with -
# and nothing that needs patching between releases of this service.
#
# The cost is that `docker exec` into a running container is impossible. That
# is a real operational loss and it is the intended trade: debugging happens
# through logs, metrics and the readiness endpoint rather than by shelling into
# production.
FROM scratch AS base

# Certificate roots, so the gateway client can verify TLS.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Everything this service stores and renders is UTC, and Go resolves "UTC"
# without consulting the database at all - so this is not load-bearing today.
# It is here because the first time somebody calls time.LoadLocation for a
# tenant-local report, its absence would be a runtime error in production
# rather than a compile error in review, and the layer costs 3 MB.
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
ENV TZ=UTC

COPY --from=build /out/api /out/worker /

# Unprivileged, and by numeric id rather than by name: scratch has no
# /etc/passwd, so a named user cannot be resolved and the container would run
# as root. 65532 is the conventional "nonroot" id.
USER 65532:65532

# ---------------------------------------------------------------------------
# Migrations
#
# cmd/migrate drives the goose library directly and embeds the migrations, so
# the binary and the schema it expects are one artefact - there is no
# arrangement in which a container runs new code against a directory of old
# SQL. The goose CLI is not used because it imports a driver for every database
# goose supports, which would pull mssql, mysql, vertica, ydb and turso into
# this module's dependency graph for no benefit.
#
# alpine rather than scratch, and this is the one place that trade goes the
# other way: a migration that failed half way through is exactly when somebody
# needs a psql prompt inside the same network namespace.
# ---------------------------------------------------------------------------

FROM build AS migrate-build
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-w -s" -o /out/migrate ./cmd/migrate

FROM alpine:3.20 AS migrate
RUN apk add --no-cache postgresql16-client ca-certificates
COPY --from=migrate-build /out/migrate /usr/local/bin/migrate
USER 65532:65532
ENTRYPOINT ["migrate"]
CMD ["up"]

# ---------------------------------------------------------------------------
# The two shippable images
#
# Separate stages rather than one image with a switchable command, so neither
# carries the other's default and `docker inspect` says plainly what will run.
# ---------------------------------------------------------------------------

FROM base AS api
EXPOSE 8080
ENTRYPOINT ["/api"]

FROM base AS worker
ENTRYPOINT ["/worker"]
