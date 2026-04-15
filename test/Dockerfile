# Linux smoke-test image for anvil. Builds a real Linux binary inside
# a golang:1.26 toolchain (CGo can't cross-compile sqlite3 + sqlite-vec
# reliably from macOS) and copies it onto a plain ubuntu:24.04 runtime
# that only carries ca-certificates + sqlite3 — the same baseline an
# end user would get from `apt-get install`.
#
# Build from the repo root:
#   docker build -f test/Dockerfile -t anvil-smoke:test .
#
# Then `docker run --rm anvil-smoke:test anvil version` smoke-tests
# the produced binary under glibc.
FROM golang:1.26 AS builder
# sqlite3.h comes from libsqlite3-dev — the builder image ships gcc
# but not the C headers sqlite-vec + mattn/go-sqlite3 #include.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libsqlite3-dev && \
    rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY . .
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -o /anvil ./cmd/anvil

# Minimal runtime image — mirrors what an end user on a plain Ubuntu
# host would have after `brew install` or `apt-get install`.
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates sqlite3 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /anvil /usr/local/bin/anvil
RUN chmod +x /usr/local/bin/anvil

WORKDIR /workspace
ENTRYPOINT []
