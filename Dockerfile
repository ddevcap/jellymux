# ── Stage 1: Fetch pre-built jellymux-web ─────────────────────────────────────
# The web UI is built and published as a tarball by the jellymux-web repo's
# release workflow. Override JELLYFIN_WEB_VERSION at build time to pin a version.
# Source: https://github.com/ddevcap/jellymux-web (GPL-2.0)
FROM alpine:3.21 AS web-stage

ARG JELLYFIN_WEB_VERSION=10.11.6-jellymux.3

ADD https://github.com/ddevcap/jellymux-web/releases/download/v${JELLYFIN_WEB_VERSION}/dist.tar.gz /tmp/dist.tar.gz
RUN mkdir -p /srv/jellymux-web \
    && tar -xzf /tmp/dist.tar.gz -C /srv/jellymux-web \
    && rm /tmp/dist.tar.gz

# ── Stage 2: Build Go proxy ───────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o jellymux .

# ── Stage 3: Runtime ──────────────────────────────────────────────────────────
# Use Caddy as the base — it gives us a production-grade reverse proxy and
# static file server. We add the proxy binary and supervisord to run both.
FROM caddy:2-alpine

RUN apk add --no-cache supervisor ca-certificates

# Create a non-root user for running the services.
RUN addgroup -S jfmux && adduser -S jfmux -G jfmux

WORKDIR /app

# Go proxy binary
COPY --from=builder /app/jellymux ./jellymux

# Jellyfin web dist
COPY --from=web-stage /srv/jellymux-web /srv/jellymux-web

# Caddyfile — Caddy listens on :8096 (public port):
#   /web/*  →  static files from /srv/jellymux-web
#   /*      →  reverse proxy to Go proxy on 127.0.0.1:8097
COPY Caddyfile /etc/caddy/Caddyfile

# Supervisord config to run Caddy + Go proxy together
COPY supervisord.conf /etc/supervisord.conf

# Ensure the non-root user can write Caddy state and supervisor pid.
RUN mkdir -p /data /config /tmp && chown -R jfmux:jfmux /data /config /tmp /app /srv/jellymux-web

EXPOSE 8096

USER jfmux

CMD ["/usr/bin/supervisord", "-c", "/etc/supervisord.conf"]
