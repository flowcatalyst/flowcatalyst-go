# syntax=docker/dockerfile:1
#
# Production image for the unified fc-server binary. Multi-stage:
#   1. build the Vue SPA  (embedded into the binary via //go:embed all:dist)
#   2. build the static Go binary (also embeds the SQL migrations via
#      //go:embed all:sql, so the runtime image needs ONLY the binary)
#   3. minimal alpine runtime with a working HEALTHCHECK
#
# Build:  docker build -t flowcatalyst-go --build-arg VERSION=$(git rev-parse --short HEAD) .
# Run:    docker run -p 8080:8080 -e FC_DATABASE_URL=... flowcatalyst-go

# ── Stage 1 — Vue SPA ──────────────────────────────────────────────────────
FROM node:24-alpine AS frontend
WORKDIR /app/frontend
RUN corepack enable
# pnpm version is pinned via package.json "packageManager"; corepack honours it.
COPY frontend/ ./
RUN pnpm install --frozen-lockfile && pnpm build

# ── Stage 2 — Go binary ────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
# Module layer cached independently of source.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly-built SPA so frontend/embed.go embeds a real dashboard
# (the build-context copy of frontend/dist is excluded via .dockerignore).
COPY --from=frontend /app/frontend/dist ./frontend/dist
ARG VERSION=docker
RUN go build -trimpath \
      -ldflags="-s -w -X github.com/flowcatalyst/flowcatalyst-go/internal/server.Version=${VERSION}" \
      -o /out/fc-server ./cmd/fc-server

# ── Stage 3 — runtime ──────────────────────────────────────────────────────
# Alpine (not distroless) so the image carries wget for a self-contained
# HEALTHCHECK. ca-certificates for outbound TLS (SQS/Secrets Manager/webhooks).
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates wget \
 && adduser -D -u 10001 flowcatalyst
USER flowcatalyst
COPY --from=build /out/fc-server /usr/local/bin/fc-server
ENV FC_API_PORT=8080
# 8080 = API (+ embedded SPA), 9090 = Prometheus metrics.
EXPOSE 8080 9090
# GET (not --spider/HEAD): the /health route is GET-only, so a HEAD probe 405s.
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/usr/local/bin/fc-server"]
