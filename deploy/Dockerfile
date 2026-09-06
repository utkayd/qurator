# syntax=docker/dockerfile:1
#
# Multi-stage, reproducible build for the single qurator binary (Constitution Principle
# I). See specs/001-qr-service-baseline/research.md §6 for the reasoning behind every
# decision below; nothing here should be "obviously" changed without reading it first.

# --- builder --------------------------------------------------------------------------

# Verified to exist and resolve to a multi-arch index (linux/amd64, linux/arm64, and
# others) as of this writing:
#   gcr.io/distroless/static-debian13:nonroot
#   @sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
# research.md called for debian13 (not debian12 — that tag line does not exist for
# static-debian13's nonroot variant at all; debian12 is a separate, still-supported
# repository, but debian13 is what was verified reachable here). Pinned by digest, not
# just tag, so a base-image update never lands silently; bump the digest deliberately
# and note it in the commit that does so.
ARG BASE_DIGEST=sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7

FROM golang:1.26-alpine AS builder

WORKDIR /src

# Cache module downloads across builds independent of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out/data

ARG VERSION=dev
ARG SOURCE_DATE_EPOCH=0

# CGO_ENABLED=0 is not an optimisation: it is what makes the single static binary
# promise true (Constitution Principle I), and it is why the SQLite driver had to be
# modernc.org/sqlite (pure Go) rather than mattn/go-sqlite3 (cgo).
#
# -trimpath removes local filesystem paths from the binary. -buildid= (empty) and
# -ldflags "-s -w" strip the build ID and debug/symbol info, both of which otherwise
# vary run to run for byte-identical source. Combined with SOURCE_DATE_EPOCH-driven
# timestamp normalisation at the image layer (buildx --rewrite-timestamp in
# release.yml), this gets the image close to reproducible without claiming full
# bit-for-bit determinism, which Go's build ID and map iteration order do not
# guarantee outright.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -buildid= -X main.version=${VERSION}" \
      -o /out/qurator ./cmd/qurator

# --- final ------------------------------------------------------------------------------
# distroless "static" over "scratch": static already bundles CA certificates and tzdata.
# scratch has neither — every outbound TLS call (an S3-compatible upgrade path, in
# particular) would fail with an unverifiable certificate chain until both were added
# back by hand, and scratch has no nonroot user mechanism at all. "static" over a
# language-specific distroless base (e.g. distroless/base) because qurator is a fully
# static binary with zero libc dependency; static is the smallest base that still isn't
# scratch.
FROM gcr.io/distroless/static-debian13@${BASE_DIGEST} AS final

# Re-declare after FROM: ARGs do not cross build stages implicitly.
ARG BASE_DIGEST=sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7
LABEL org.opencontainers.image.source="https://github.com/utkayd/qurator" \
      org.opencontainers.image.base.digest="${BASE_DIGEST}" \
      org.opencontainers.image.description="Self-hosted, single-binary dynamic QR code service"

# The distroless nonroot variant already ships a "nonroot" user (UID 65532); USER here
# is what actually applies it to the running container rather than only being present.
USER 65532:65532

COPY --from=builder --chown=nonroot:nonroot /out/qurator /qurator
COPY --from=builder --chown=nonroot:nonroot /out/data /data

# Zero-configuration defaults point at a volume so a plain `docker run -v` (or the
# compose file alongside this Dockerfile) is enough to persist data across restarts.
ENV QURATOR_SERVER_DATA_DIR=/data \
    QURATOR_DB_DSN=/data/qurator.db \
    QURATOR_BLOB_PATH=/data/blobs
VOLUME ["/data"]
EXPOSE 8080

# distroless has no shell or curl, so the HEALTHCHECK uses the exec form and asks the
# binary itself to probe its own /readyz (the `healthcheck` subcommand). Orchestrators
# that prefer HTTP probes can hit GET /healthz and /readyz directly.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/qurator", "healthcheck"]

ENTRYPOINT ["/qurator"]
