# jellymux

A lightweight reverse proxy that sits in front of one or more [Jellyfin](https://jellyfin.org) servers and presents them to clients as a single unified server.

- **Single endpoint** — any standard Jellyfin client connects without modification.
- **Own user accounts** — clients authenticate against the proxy, not the backends.
- **Merged libraries** — if two backends both expose a Movies library, clients see one combined Movies library with deterministic UUID v5 IDs to prevent collisions.
- **Direct streaming** — optional per-user 302 redirects so clients stream directly from backends over the local network (e.g. Tailscale), saving proxy bandwidth.
- **Health checks & circuit breaker** — unhealthy backends are automatically skipped until they recover.

## Quick start

```yaml
# docker-compose.yml
services:
  jellymux:
    image: ddevcap/jellymux:latest
    restart: unless-stopped
    ports:
      - "8096:8096"
    environment:
      DATABASE_URL: postgres://jellymux:jellymux@postgres:5432/jellymux?sslmode=disable
      EXTERNAL_URL: https://jellyfin.example.com
      SERVER_NAME: "My Jellymux"
      INITIAL_ADMIN_PASSWORD: changeme
    depends_on:
      postgres:
        condition: service_healthy

  postgres:
    image: postgres:16-alpine
    restart: unless-stopped
    environment:
      POSTGRES_DB: jellymux
      POSTGRES_USER: jellymux
      POSTGRES_PASSWORD: jellymux
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U jellymux -d jellymux"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  postgres_data:
```

```bash
docker compose up -d
```

Point your Jellyfin client at `http://<host>:8096` and log in with `admin` / `changeme`.

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://jellymux:jellymux@localhost:5432/jellymux?sslmode=disable` | PostgreSQL connection string |
| `LISTEN_ADDR` | `:8096` | Address the proxy binds to |
| `EXTERNAL_URL` | `http://localhost:8096` | Publicly reachable URL reported to clients |
| `SERVER_ID` | `jellymux-default-id` | Server UUID presented to clients |
| `SERVER_NAME` | `Jellymux` | Server name presented to clients |
| `SESSION_TTL` | `720h` (30 days) | Session idle timeout |
| `LOGIN_MAX_ATTEMPTS` | `10` | Failed logins per IP before temporary ban |
| `LOGIN_WINDOW` | `15m` | Sliding window for counting failed logins |
| `LOGIN_BAN_DURATION` | `15m` | How long an IP is banned |
| `INITIAL_ADMIN_USER` | `admin` | Username for the auto-seeded admin account |
| `INITIAL_ADMIN_PASSWORD` | *(empty — seeding skipped)* | Password for the auto-seeded admin account |
| `CORS_ORIGINS` | *(empty)* | Comma-separated additional CORS origins |
| `BITRATE_LIMIT` | `0` (unlimited) | Max remote client bitrate in bits/s |
| `HEALTH_CHECK_INTERVAL` | `30s` | Backend health check interval |
| `LOG_LEVEL` | `info` | Minimum log level: `debug`, `info`, `warn`, `error` |
| `DIRECT_STREAM_NETWORKS` | *(empty = RFC 1918)* | CIDRs where direct stream redirects are allowed |

## Setup flow

1. Start the stack with `INITIAL_ADMIN_PASSWORD` set.
2. Log in as `admin` and save the session token.
3. Register each Jellyfin backend via `POST /proxy/backends`.
4. Create proxy users via `POST /proxy/users`.
5. For each user + backend, call `POST /proxy/backends/:id/login` to create the mapping.
6. Point Jellyfin clients at the proxy URL and log in with proxy credentials.

## Health endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness probe — always returns `200` |
| `GET` | `/ready` | Readiness probe — `200` if DB is reachable, `503` otherwise |

## Links

- **Source**: [github.com/ddevcap/jellymux](https://github.com/ddevcap/jellymux)
- **Issues**: [github.com/ddevcap/jellymux/issues](https://github.com/ddevcap/jellymux/issues)
- **Full API docs**: see the [README](https://github.com/ddevcap/jellymux#admin-api) on GitHub