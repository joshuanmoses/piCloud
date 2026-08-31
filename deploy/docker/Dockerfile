# syntax=docker/dockerfile:1.7
#
# PiFiles application image.
#
# Produces a single container that serves the REST API, the compiled web
# interface, and (from Phase 7) the background job runner. Caddy sits in front
# of it as a pure reverse proxy; PostgreSQL runs alongside.
#
# Build for a Raspberry Pi from a workstation:
#     docker buildx build --platform linux/arm64 -f deploy/docker/Dockerfile .
#
# The builder stages are pinned to $BUILDPLATFORM and cross-compile to
# $TARGETARCH. Emulating an ARM64 toolchain under QEMU works but is roughly an
# order of magnitude slower, and Go cross-compiles natively.

# ---------------------------------------------------------------------------
# Stage 1 — build the web interface
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:26-alpine AS frontend

WORKDIR /build

# Dependencies are installed from the lockfile in their own layer, so editing
# application source does not invalidate the (slow) npm install.
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci --no-audit --no-fund

COPY frontend/ ./
RUN npm run build

# ---------------------------------------------------------------------------
# Stage 2 — build the server
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend

# Declared without defaults on purpose: BuildKit populates these from the
# target platform. Giving them defaults would mask that detection and silently
# cross-compile for the wrong architecture on a plain `docker build`.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /build

COPY backend/go.mod backend/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY backend/ ./

# The compiled SPA is embedded in the binary, so a deployment can never run a
# frontend and a backend that disagree about the API contract.
COPY --from=frontend /build/dist ./internal/web/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w \
        -X github.com/joshuanmoses/piCloud/backend/internal/version.Version=${VERSION} \
        -X github.com/joshuanmoses/piCloud/backend/internal/version.Commit=${COMMIT} \
        -X github.com/joshuanmoses/piCloud/backend/internal/version.BuildDate=${BUILD_DATE}" \
      -o /out/pifiles \
      ./cmd/server

# ---------------------------------------------------------------------------
# Stage 3 — runtime
# ---------------------------------------------------------------------------
FROM alpine:3.22 AS runtime

# Alpine rather than scratch or distroless: on a headless Raspberry Pi, being
# able to `docker compose exec app sh` to diagnose a mount or permission problem
# is worth the few megabytes. wget backs the container health check.
RUN apk add --no-cache ca-certificates tzdata wget \
    && rm -rf /var/cache/apk/*

# A fixed high UID keeps the storage bind mount's ownership predictable across
# hosts. Documented in docs/storage.md so operators can chown correctly.
ARG PIFILES_UID=10001
ARG PIFILES_GID=10001
RUN addgroup -g ${PIFILES_GID} -S pifiles \
    && adduser -u ${PIFILES_UID} -G pifiles -S -H -s /sbin/nologin pifiles

COPY --from=backend /out/pifiles /usr/local/bin/pifiles

# /data is the container-side mount point for the storage root. The host path
# is configurable; see STORAGE_ROOT and docs/storage.md.
RUN mkdir -p /data && chown pifiles:pifiles /data
VOLUME ["/data"]

USER pifiles:pifiles
WORKDIR /data

ENV STORAGE_ROOT=/data \
    HTTP_HOST=0.0.0.0 \
    HTTP_PORT=8080 \
    APP_ENV=production

EXPOSE 8080

# Liveness only: this must not depend on PostgreSQL, because restarting the
# application container cannot fix a database outage and would produce a
# restart loop that makes diagnosis harder.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/health/live || exit 1

ENTRYPOINT ["/usr/local/bin/pifiles"]
