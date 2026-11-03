# Server

**English** · [Türkçe](tr/server.md)

`cmd/server` starts the API server. The server reads its configuration from environment variables, connects to PostgreSQL, applies migrations and serves HTTP requests. On `SIGINT` / `SIGTERM` it shuts down gracefully.

## Quick start

```bash
docker compose up -d --wait postgres
```

```bash
CISID_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' CISID_GITHUB_WEBHOOK_SECRET=dev-secret go run ./cmd/server
```

```bash
curl http://localhost:8080/readyz
```

To run PostgreSQL, the server and the agent together as containers, see [deployment.md](deployment.md).

## Configuration (`internal/config`)

| Variable | Default | Description |
|---|---|---|
| `CISID_DATABASE_URL` | **required** | `postgres://` connection URL |
| `CISID_GITHUB_WEBHOOK_SECRET` | — | GitHub webhook secret; enables `/webhooks/github` ([webhook.md](webhook.md)). A server without a webhook runs manual and scheduled builds. |
| `CISID_HTTP_ADDR` | `:8080` | Address to listen on |
| `CISID_SHUTDOWN_TIMEOUT` | `15s` | How long to wait for in-flight requests on shutdown |
| `CISID_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CISID_GITHUB_TOKEN` | — | GitHub token: for private repositories, the rate limit and commit statuses ([forges.md](forges.md#github)) |
| `CISID_GITHUB_API_URL` | `https://api.github.com` | API URL for GitHub Enterprise Server |
| `CISID_GITEA_URL`, `CISID_GITEA_TOKEN`, `CISID_GITEA_WEBHOOK_SECRET` | — | Gitea server; turns on when the URL is set ([forges.md](forges.md#gitea-and-forgejo)) |
| `CISID_FORGEJO_URL`, `CISID_FORGEJO_TOKEN`, `CISID_FORGEJO_WEBHOOK_SECRET` | — | Forgejo server; turns on when the URL is set |
| `CISID_GITLAB_URL`, `CISID_GITLAB_TOKEN`, `CISID_GITLAB_WEBHOOK_SECRET` | `https://gitlab.com` | GitLab ([forges.md](forges.md#gitlab)) |
| `CISID_BITBUCKET_TOKEN`, `CISID_BITBUCKET_WEBHOOK_SECRET` | — | Bitbucket Cloud ([forges.md](forges.md#bitbucket-cloud)) |
| `CISID_AGENT_TOKEN` | — | The token agents must present when they connect, at least 16 characters. You can give several tokens separated by commas; all of them are accepted, so you can rotate tokens without cutting off agents ([agent.md](agent.md#authentication-and-tls)). If unset, any client can connect as an agent, and the server logs a warning about this at startup. |
| `CISID_READ_TOKEN` | — | Read (viewer) token, at least 16 characters. If set, reading requires sign-in by default ([api.md](api.md#read-access)). |
| `CISID_TLS_CERT_FILE`, `CISID_TLS_KEY_FILE` | — | If both are set, the server serves over HTTPS (TLS 1.2 or later). Agents connect with `wss://`. |
| `CISID_RATE_LIMIT` | `10` | Requests per second per client IP for the webhook and `/api` (bursts of up to twice that are allowed). `0` turns the limit off. Exceeding it returns `429`. |
| `CISID_TRUST_PROXY` | `false` | If `true`, the client IP is taken from the first address in `X-Forwarded-For`. Turn this on only behind a reverse proxy that writes this header itself; otherwise the header can be forged. |
| `CISID_API_TOKEN` | — | The server's admin token: it can do everything, including creating the first users; at least 16 characters ([accounts.md](accounts.md)) |
| `CISID_READ_ACCESS` | `signed-in` if a read token is set, otherwise `public` | `public`: anyone can read; `signed-in`: reading requires sign-in or a token ([accounts.md](accounts.md#read-access)) |
| `CISID_ADMIN_USER`, `CISID_ADMIN_PASSWORD` | `admin`, — | If a password is set, the server creates this admin at startup (and leaves it alone if it exists); the password must be at least 10 characters |
| `CISID_SESSION_TTL` | `7d` | How long a sign-in lasts (such as `12h`, `30d`) |
| `CISID_<FORGE>_OAUTH_CLIENT_ID`, `..._SECRET` | — | Sign-in through a forge: `GITHUB`, `GITEA`, `FORGEJO`, `GITLAB`, `BITBUCKET`; requires `CISID_PUBLIC_URL` ([accounts.md](accounts.md#signing-in-through-a-forge)) |
| `CISID_OAUTH_SIGNUP_ROLE` | — | `viewer` or `maintainer`: forge users without an account get one |
| `CISID_SECRET_KEY` | — | 32-byte key that encrypts secrets, base64 ([secrets.md](secrets.md)) |
| `CISID_PUBLIC_URL` | — | The URL users reach the server at. Used in commit status links ([commit-status.md](commit-status.md)). |
| `CISID_BUILD_FORKS` | `false` | If `true`, pull requests from forks are built too; these jobs get no secrets ([webhook.md](webhook.md#pull-requests)) |
| `CISID_POLL_INTERVAL` | `1m` | How often watched branches are polled, at least `10s`; `0` turns polling off ([polling.md](polling.md)) |
| `CISID_LOG_RETENTION` | `90d` | How long the logs of finished jobs are kept (such as `30d`, `72h`). `0` keeps logs forever. Expired logs are deleted in the hourly cleanup; the job record stays. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` etc. | — | The OTLP/HTTP collector to send traces to. If unset, tracing is off ([tracing.md](tracing.md)). |

All missing or invalid values are reported in a single error message. When the configuration is logged, secrets and the database password are masked (`slog.LogValuer`).

## Endpoints (`internal/api/router.go`)

| Path | Description |
|---|---|
| `POST /webhooks/github` | GitHub webhooks ([webhook.md](webhook.md)) |
| `POST /webhooks/{gitea,forgejo,gitlab,bitbucket}` | Webhooks from the other forges ([forges.md](forges.md)); a forge whose secret is not set returns `404` |
| `GET /agent/ws` | Agent WebSocket connections ([hub.md](hub.md)) |
| `GET /api/jobs/...` | Jobs, logs and the live log stream ([api.md](api.md)) |
| `/api/repos/{owner}/{repo}/secrets` | Repository secrets ([secrets.md](secrets.md)) |
| `/api/repos/{owner}/{repo}/builds`, `…/schedules` | Manual and scheduled builds ([api.md](api.md#manual-and-scheduled-builds)) |
| `GET /`, `GET /jobs/{id}` | Web UI ([ui.md](ui.md)) |
| `GET /metrics` | Prometheus metrics ([metrics.md](metrics.md)) |
| `GET /auth/{forge}/login`, `/callback` | Sign-in through a forge ([accounts.md](accounts.md#signing-in-through-a-forge)) |
| `GET /healthz` | Liveness: `200 {"status":"ok"}` if the process is up |
| `GET /readyz` | Readiness: `200` if the database answers within 2 s, otherwise `503 {"status":"unavailable","reason":"database unreachable"}` |

Every request goes through two middlewares:

- **Logging:** One log line is written for each request: method, path, status, duration and remote address. The wrapper passes `Hijack` and `Flush` calls through to the underlying writer, so WebSocket upgrades keep working.
- **Panic recovery:** If a handler panics, the stack trace is logged and the client gets `500`. The server keeps running.

## Migrations (`internal/db/migrate.go`)

- The SQL files live in `internal/db/migrations/` and are embedded in the binary (`embed`). Name format: `<version>_<name>.sql`, for example `002_job_execution.sql`.
- On every startup the server applies the migrations that have not been applied yet, in version order. Applied migrations are recorded in the `schema_migrations` table.
- Each migration runs in its own transaction. If it fails, that migration is rolled back completely and the server does not start.
- If several servers that share the same database start at the same time, they queue up on a PostgreSQL advisory lock. Each migration is applied exactly once.

## Startup and shutdown

```
config.Load ─► pgxpool + Ping (10 s) ─► Migrate ─► net.Listen ─► Serve
                                                                  │
                                     SIGTERM ─────────────────────┘
                                        │
                     Shutdown: new connections are refused,
                     in-flight requests get up to CISID_SHUTDOWN_TIMEOUT
```

If the time runs out, the remaining connections are closed forcibly and the process exits with an error code.

## Tests

```bash
go test ./cmd/server/ ./internal/api/ ./internal/config/
```

Tests that need a database (migrations, store, end-to-end server test):

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/db/ ./cmd/server/
```

Each `internal/db` test creates a temporary database (`cisid_test_*`) and drops it when the test ends. `TestRunEndToEnd` starts the real server, waits for readiness, sends a signed webhook, checks that the job was written to the database, and shuts the server down.
