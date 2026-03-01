# jellyfin-proxy

[![CI](https://github.com/ddevcap/jellyfin-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/ddevcap/jellyfin-proxy/actions/workflows/ci.yml)
[![E2E](https://github.com/ddevcap/jellyfin-proxy/actions/workflows/e2e.yml/badge.svg)](https://github.com/ddevcap/jellyfin-proxy/actions/workflows/e2e.yml)
[![codecov](https://codecov.io/gh/ddevcap/jellyfin-proxy/branch/main/graph/badge.svg?token=WJ76T7CQHV)](https://codecov.io/gh/ddevcap/jellyfin-proxy)

A lightweight reverse proxy that sits in front of one or more Jellyfin servers
and presents them to clients as a single unified server.

> **🤖 AI-assisted** — this project was built with the help of AI tooling. All
> code is tested, linted, and human-reviewed. Contributions and bug reports are
> welcome.

## What it solves

Jellyfin has no built-in multi-server support. If you run several Jellyfin
instances (e.g. one per location, one per media type), every client must be
configured separately for each server and users must maintain separate accounts
on each.

`jellyfin-proxy` solves this by:

- Exposing a **single Jellyfin-compatible endpoint** that any standard Jellyfin
  client connects to without modification.
- Maintaining its **own user accounts** with hashed passwords — clients
  authenticate against the proxy, not against any backend directly.
- **Routing requests** transparently to the correct backend based on the item
  being requested, using deterministic UUID v5 IDs to disambiguate items across
  servers.
- Letting you map each proxy user to accounts on one or more backends, with
  optional per-user tokens for fine-grained access control.
- **Merging libraries** across backends transparently — if two backends both
  expose a Movies library, clients see a single unified Movies library. Items
  are fetched from all contributing backends and concatenated. Deterministic
  UUID v5 IDs ensure items from different servers never collide.
- **Direct streaming** (optional) — when a user's `direct_stream` flag is
  enabled, the proxy issues a `302` redirect for all streaming requests
  (video, audio, images, HLS) instead of piping bytes through itself. Clients
  connect directly to the backend over the local network (e.g. Tailscale),
  saving proxy bandwidth entirely. API calls still go through the proxy for
  ID rewriting. Controlled per-user via the admin API.

---

## Architecture

```
Jellyfin client
      │
      ▼
 jellyfin-proxy  (Go + Gin, :8097 internally)
      │  served via Caddy (:8096 externally)
      │  ┌──────────────────────────────────┐
      ├─▶│  Backend A  (Movies)             │──┐
      ├─▶│  Backend B  (TV Shows)           │──┤
      └─▶│  Backend C  (Movies)             │──┘
         └──────────────────────────────────┘
                       ▼
            Merged into unified libraries:
            • Movies  (A + C combined)
            • TV Shows (B)
```

The proxy fans out requests to all backends in parallel, merges the responses,
and rewrites item IDs so every item is globally unique. Libraries with the same
type (e.g. Movies on Backend A + Movies on Backend B) are collapsed into a
single virtual library — clients see one "Movies" folder instead of two.

The Dockerfile bundles the Go binary, a [custom fork of the Jellyfin Web UI](https://github.com/ddevcap/jellyfin-proxy-web)
with proxy-specific patches, and Caddy into a single container managed by supervisord. Caddy serves
the web UI as static files and reverse-proxies all API traffic to the Go proxy.
The container runs as a non-root user (`jfproxy`).

**Background services:**

- **Health checker** — periodically pings every backend's
  `/System/Info/Public` endpoint. Backends that fail 2 consecutive checks are
  marked unavailable and skipped in fan-out requests until they recover.
- **Circuit breaker** — if a backend fails 5 consecutive live requests (e.g.
  connection refused), it is tripped offline immediately without waiting for
  the next health check cycle.
- **Session cleaner** — runs hourly to delete sessions that have been idle
  longer than `SESSION_TTL`.
- **Request ID** — every request gets a unique `X-Request-Id` header
  (generated or forwarded from upstream) for log correlation.

---

## Quick start

### Docker Compose (recommended)

1. Copy and edit the compose file:

```yaml
environment:
  DATABASE_URL: postgres://jellyfin:jellyfin@postgres:5432/jellyfin_proxy?sslmode=disable
  EXTERNAL_URL: https://jellyfin.example.com
  SERVER_ID: my-unique-server-id        # any stable string
  SERVER_NAME: "My Jellyfin Proxy"
  INITIAL_ADMIN_PASSWORD: changeme      # only used when the DB is empty
```

2. Start the stack:

```bash
docker compose up -d
```

On first startup the proxy automatically creates an admin user
(`admin` / the value of `INITIAL_ADMIN_PASSWORD`). Once any user exists in the
database this seeding step is skipped on subsequent restarts.

Point your Jellyfin client at `http://<host>:8096` and log in with those
credentials.

---

## Configuration

All configuration is via environment variables.

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | `postgres://jellyfin:jellyfin@localhost:5432/jellyfin_proxy?sslmode=disable` | PostgreSQL connection string |
| `LISTEN_ADDR` | `:8096` | Address the Go proxy binds to |
| `EXTERNAL_URL` | `http://localhost:8096` | Publicly reachable URL reported to clients |
| `SERVER_ID` | `jellyfin-proxy-default-id` | Server UUID presented to clients |
| `SERVER_NAME` | `Jellyfin Proxy` | Server name presented to clients |
| `SESSION_TTL` | `720h` (30 days) | Session idle timeout (`0` = never expire) |
| `LOGIN_MAX_ATTEMPTS` | `10` | Failed logins per IP before temporary ban |
| `LOGIN_WINDOW` | `15m` | Sliding window for counting failed logins |
| `LOGIN_BAN_DURATION` | `15m` | How long an IP is banned after too many failures |
| `INITIAL_ADMIN_USER` | `admin` | Username for the auto-seeded admin account |
| `INITIAL_ADMIN_PASSWORD` | *(empty — seeding skipped)* | Password for the auto-seeded admin account |
| `SHUTDOWN_TIMEOUT` | `15s` | Max time to wait for in-flight requests during graceful shutdown |
| `CORS_ORIGINS` | *(empty)* | Comma-separated additional origins allowed for credentialed CORS requests |
| `BITRATE_LIMIT` | `0` (unlimited) | Max remote client bitrate in bits/s, applied via Jellyfin user policy |
| `HEALTH_CHECK_INTERVAL` | `30s` | How often the proxy pings backends to check availability. Backends that fail 2 consecutive checks are skipped in fan-out requests until they recover |
| `LOG_LEVEL` | `info` | Minimum log level: `debug`, `info`, `warn`, `error` |
| `DIRECT_STREAM_NETWORKS` | *(empty = RFC 1918 private ranges)* | Comma-separated CIDRs where direct stream redirects are allowed (e.g. `100.64.0.0/10` for Tailscale) |

---

## Operational endpoints

These endpoints are **unauthenticated** and intended for container
orchestrators (Kubernetes, Docker health checks, load balancers).

| Method | Path | Description |
|---|---|---|
| `GET` | `/health` | Liveness probe — always returns `200 {"status":"ok"}` |
| `GET` | `/ready` | Readiness probe — returns `200` if the database is reachable, `503` otherwise |

---

## Admin API

All admin endpoints are under `/proxy` and require a valid session token from
an **admin** user. Obtain a token by logging in:

```bash
curl -s -X POST http://localhost:8096/Users/AuthenticateByName \
  -H 'Content-Type: application/json' \
  -H 'X-Emby-Authorization: MediaBrowser Client="curl", Device="dev", DeviceId="dev", Version="1.0"' \
  -d '{"Username":"admin","Pw":"changeme"}' | jq .AccessToken
```

Pass the token as a header on every admin request:

```
X-Emby-Token: <token>
```

---

### Users

Proxy users are the accounts clients log in with. They are entirely managed by
the proxy and are independent of any backend Jellyfin accounts.

| Method | Path | Description |
|---|---|---|
| `POST` | `/proxy/users` | Create a user |
| `GET` | `/proxy/users` | List all users |
| `GET` | `/proxy/users/:id` | Get a user |
| `GET` | `/proxy/users/:id/backends` | List all backend mappings for a user |
| `PATCH` | `/proxy/users/:id` | Update display name, password, admin flag, or direct stream |
| `DELETE` | `/proxy/users/:id` | Delete a user |

**Create user** — `POST /proxy/users`

```json
{
  "username": "alice",
  "display_name": "Alice",
  "password": "supersecret",
  "is_admin": false,
  "direct_stream": false
}
```

`direct_stream` — when `true`, streaming requests for this user are redirected
directly to the backend (302) instead of being piped through the proxy. Requires
clients to have direct network access to all backends (e.g. Tailscale).
Defaults to `false`.

---

### Backends

A backend is a real Jellyfin server that the proxy routes requests to. When a
backend is registered, the proxy fetches its server ID from `/System/Info/Public`
and stores it as the `external_id`. This ID is used as the namespace for
deterministic UUID v5 proxy IDs, so every item is globally unique across
backends without any manual configuration.

| Method | Path | Description |
|---|---|---|
| `POST` | `/proxy/backends` | Register a backend |
| `GET` | `/proxy/backends` | List all backends |
| `GET` | `/proxy/backends/:id` | Get a backend |
| `PATCH` | `/proxy/backends/:id` | Update name, URL, or enabled state |
| `DELETE` | `/proxy/backends/:id` | Remove a backend |
| `GET` | `/proxy/backends/health` | Health status of all backends (available, last error, failure count) |

**Register a backend** — `POST /proxy/backends`

```json
{
  "name": "Movies",
  "url": "http://jellyfin-movies:8096"
}
```

The proxy fetches the server ID from the backend's public `/System/Info`
endpoint (no credentials required) and persists the backend record. Per-user
tokens are created separately via `POST /proxy/backends/:id/login`.


---

### Backend user mappings

Each proxy user that should have access to a backend must be mapped to an
account on that backend. There are two ways to create a mapping:

#### Option A — Login (recommended)

The proxy authenticates against the backend on the user's behalf and stores the
resulting backend user ID and token automatically.

```
POST /proxy/backends/:id/login
```

```json
{
  "proxy_user_id": "<proxy-user-uuid>",
  "username": "alice-on-backend",
  "password": "backendpassword"
}
```

#### Option B — Manual mapping

If you already know the backend user ID and token:

```
POST /proxy/backends/:id/users
```

```json
{
  "user_id": "<proxy-user-uuid>",
  "backend_user_id": "<jellyfin-user-uuid-on-backend>",
  "backend_token": "<optional-per-user-token>"
}
```

When `backend_token` is omitted, authenticated requests to the backend will be
sent without credentials. Use `POST /proxy/backends/:id/login` (Option A) to
automatically obtain and store a token.

**Other mapping operations:**

| Method | Path | Description |
|---|---|---|
| `GET` | `/proxy/backends/:id/users` | List all user mappings for a backend |
| `PATCH` | `/proxy/backends/:id/users/:mappingId` | Update mapping (token, enabled) |
| `DELETE` | `/proxy/backends/:id/users/:mappingId` | Remove a mapping |

Set `"enabled": false` on a mapping to block a specific user from a specific
backend without deleting the mapping.

---

## Known limitations / Roadmap

The proxy is functional for day-to-day media playback but some areas are still
rough or not yet implemented:

| Area | Status | Notes |
|---|---|---|
| **User images / avatars** | ✅ Implemented | Profile pictures are stored in the proxy database. Upload, fetch, and delete are supported via the standard Jellyfin image endpoints |
| **Live TV** | ⚠️ Partial | Channels and Programs are merged across backends; Info is proxied from the first backend. Covered by tests — recording management and guide data are not proxied |
| **SyncPlay** | ⚠️ Not supported | Returns an empty list — cross-backend sync is not possible |
| **Search** | ✅ Implemented | Results from all backends are merged into a single response |
| **Download / file export** | ✅ Implemented | `GET /Items/:itemId/Download` — streams or redirects (DirectStream) the file from the correct backend |
| **Lyrics** | ✅ Implemented | `GET /Audio/:itemId/Lyrics` — proxied JSON response from the correct backend |
| **Collections** | ✅ Implemented | `GET /Collections/:itemId/Items` — paged item list proxied from the correct backend |
| **Admin write operations** | ❌ Not implemented | Jellyfin admin actions (library scans, user management on the backend, etc.) must be performed directly on each backend |
| **Subtitle upload** | ❌ Not implemented | Writing subtitles back to a backend is not proxied |
| **Transcoding sessions** | ⚠️ Partial | Progress reporting is forwarded but session lists are not aggregated across backends |
| **Notifications / webhooks** | ❌ Not implemented | Backend-originated push events are not forwarded to clients |
| **Multi-backend watch state sync** | ⚠️ Partial | Played / favorite actions are propagated to matching items on other backends via TMDB/IMDB provider ID matching. Items without provider IDs are not synced |

---

## Typical setup flow

1. Start the stack with `INITIAL_ADMIN_PASSWORD` set.
2. Log in as `admin` and save the session token.
3. Register each Jellyfin backend with `POST /proxy/backends`.
4. Create proxy users with `POST /proxy/users`.
5. For each user + backend combination call `POST /proxy/backends/:id/login` to
   create the mapping.
6. Point Jellyfin clients at the proxy URL and log in with proxy credentials.

---

## Contributing

Contributions are welcome. Please open an issue before starting significant
work so we can discuss the approach first.

### Prerequisites

| Tool | Version | Install |
|---|---|---|
| Go | ≥ 1.24 | [go.dev/dl](https://go.dev/dl/) |
| golangci-lint | ≥ 2.0 | `brew install golangci-lint` or [golangci-lint.run](https://golangci-lint.run/welcome/install/) |
| lefthook | ≥ 2.0 | `brew install lefthook` or [github.com/evilmartians/lefthook](https://github.com/evilmartians/lefthook) |
| Docker + Compose | any recent | [docs.docker.com](https://docs.docker.com/get-docker/) |

### Local setup

```bash
git clone https://github.com/ddevcap/jellyfin-proxy.git
cd jellyfin-proxy

# Install the pre-commit hook (lint + test runs automatically on every commit).
lefthook install

# Run the unit / integration test suite.
make test        # or: go test -race ./...

# Run the linter.
golangci-lint run ./...
```

### Jellyfin test servers

The repo includes a `docker-compose.jellyfin.yml` that starts two standalone
Jellyfin instances for local development. Sample media files are included, but
the Jellyfin config/database is **not** committed — they are created
automatically on first launch.

```bash
make jellyfin-up      # start servers + auto-configure (root / password)
make jellyfin-down    # stop servers
```

| Server | URL | Credentials |
|---|---|---|
| Server 1 | http://localhost:8196 | `root` / `password` |
| Server 2 | http://localhost:8296 | `root` / `password` |

`make jellyfin-up` runs the Jellyfin startup wizard automatically via
`scripts/setup-jellyfin.sh`, creating an admin account, adding a Movies
library, and waiting for the scan to finish. Re-running is safe — setup is
skipped if already completed. Once both servers are running, register them as
backends in the proxy and map your proxy users (see the comments in
`docker-compose.jellyfin.yml` for example API calls).

### Testing

The project has two levels of tests:

| Command | What it runs | Docker needed? |
|---|---|---|
| `make test` | Unit & integration tests (294 specs) with race detection | No |
| `make e2e` | End-to-end tests (54 specs) against a live Docker stack | Yes |

**Unit tests** use an in-memory SQLite database and `httptest.Server` mocks.
Every route registered in the router has at least one test.

**E2E tests** spin up a full Docker Compose stack (proxy + Postgres + 2 real
Jellyfin backends with test media) and exercise the complete flow: login →
browse merged libraries → play media → mark favorites → error handling.
The test suite automatically runs the Jellyfin startup wizard, creates an
admin account, adds a Movies library, and waits for the scan to complete
before running any tests — no manual setup needed.

```bash
# Run e2e tests (starts stack, runs tests, tears down).
make e2e

# Or manage the stack manually:
make e2e-up                          # start the stack
go test -tags e2e -v ./e2e/...       # run tests
make e2e-down                        # tear down
```

The e2e tests use the `//go:build e2e` build tag, so `go test ./...` skips
them automatically. The e2e stack runs on port **18096** to avoid conflicting
with a local dev stack on 8096.

### Pre-commit hook

`lefthook.yml` registers a `pre-commit` hook that runs automatically whenever
Go files are staged:

1. **`golangci-lint run --fix`** — lints the whole module and auto-stages any
   files it fixes.
2. **`go test ./...`** — runs the full test suite.

The hook is skipped entirely when no `.go` files are staged (e.g. docs-only
commits), keeping it fast. To bypass it in an emergency:

```bash
git commit --no-verify
```

### Code style

- All code is formatted with `gofmt` / `goimports` — the linter enforces this.
- New endpoints should follow the patterns in `api/handler/media.go` and have
  corresponding tests in the same package.
- Keep handler functions thin: routing, ID translation, and delegating to
  `sc.ProxyJSON` / `sc.ProxyStream`. Business logic belongs in the `backend`
  or `idtrans` packages.
- The linter configuration lives in `.golangci.yml` — update it if you enable
  or disable linters as part of your change.
