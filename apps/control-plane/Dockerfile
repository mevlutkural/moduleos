# Production image for the ModuleOS control-plane daemon.
#
# Build from the repository root so the root Go module is in the context:
#
#   docker buildx build -f apps/control-plane/Dockerfile .
#
# The context is restricted by apps/control-plane/Dockerfile.dockerignore.

# golang:1.26.10-trixie (multi-architecture index)
FROM --platform=$BUILDPLATFORM golang:1.26.10-trixie@sha256:a050dcceee6eb9bf110151662dfac7662f3bec40927c7dee48731e3d92083d42 AS builder

ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
# BUILD_DATE is required: it becomes the OCI creation time and /version.
ARG BUILD_DATE

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY LICENSE ./
COPY apps/control-plane/cmd ./apps/control-plane/cmd
COPY apps/control-plane/internal ./apps/control-plane/internal

RUN set -eu; \
    case "$BUILD_DATE" in \
        [0-9][0-9][0-9][0-9]-[01][0-9]-[0-3][0-9]T[0-2][0-9]:[0-5][0-9]:[0-6][0-9]Z) ;; \
        *) echo "BUILD_DATE must be an RFC 3339 UTC timestamp such as 2026-01-01T00:00:00Z" >&2; exit 1 ;; \
    esac; \
    buildinfo=github.com/mevlutkural/moduleos/apps/control-plane/internal/buildinfo; \
    for command in moduleosd moduleosd-container; do \
        CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build \
            -trimpath \
            -buildvcs=false \
            -ldflags="-s -w -X ${buildinfo}.Version=${VERSION} -X ${buildinfo}.Commit=${REVISION} -X ${buildinfo}.BuildDate=${BUILD_DATE}" \
            -o "/out/usr/local/bin/${command}" \
            "./apps/control-plane/cmd/${command}"; \
    done; \
    chmod 0555 /out/usr/local/bin/moduleosd /out/usr/local/bin/moduleosd-container; \
    install -D -m 0444 LICENSE /out/usr/share/licenses/moduleos/LICENSE; \
    install -d -m 0700 -o 65532 -g 65532 /out/data/moduleos

# gcr.io/distroless/static-debian13 (multi-architecture index); no shell or
# package manager.
FROM gcr.io/distroless/static-debian13@sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78

ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE

LABEL org.opencontainers.image.title="ModuleOS control plane" \
      org.opencontainers.image.description="moduleosd, the ModuleOS single-node Docker Swarm control-plane daemon" \
      org.opencontainers.image.url="https://github.com/mevlutkural/moduleos" \
      org.opencontainers.image.source="https://github.com/mevlutkural/moduleos" \
      org.opencontainers.image.documentation="https://github.com/mevlutkural/moduleos/blob/main/apps/control-plane/README.md" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian13" \
      org.opencontainers.image.base.digest="sha256:15f8637509792e05b97d83451b0b6ccfa4abd548f997e406ac1e72c87fe1fa78"

COPY --from=builder /out/usr/local/bin/ /usr/local/bin/
COPY --from=builder /out/usr/share/licenses/moduleos/ /usr/share/licenses/moduleos/
# Copying the directory itself (not its contents) preserves its 0700 mode and
# 65532:65532 owner; a fresh named volume inherits both on first mount.
COPY --from=builder /out/data/ /var/lib/

ENV ENV=production \
    LISTEN_ADDRESS=0.0.0.0 \
    PORT=3000 \
    DATABASE_PATH=/var/lib/moduleos/moduleos.db

EXPOSE 3000

# The helper starts as root only to adjust ownership of the data directory and
# exact SQLite files and to add the Docker socket group. It then drops to
# 65532:65532 irreversibly and executes moduleosd, which runs as PID 1.
USER 0:0

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD ["/usr/local/bin/moduleosd-container", "healthcheck"]

STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/moduleosd-container", "start"]
