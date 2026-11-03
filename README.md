# cisid

**English** · [Türkçe](README.tr.md)

A self-hosted, Docker-based CI/CD system written in Go.

> Status: Phases 1–6 are done. Moving the agent protocol to gRPC is postponed ([ADR 001](docs/adr/001-agent-protocol-websocket.md)). For details, see [ROADMAP.md](ROADMAP.md).

## Documentation

- [ROADMAP.md](ROADMAP.md): phases and progress
- [CLAUDE.md](CLAUDE.md): code and commit rules
- [docs/pipeline.md](docs/pipeline.md): The `.cisid.yml` format and how steps run
- [docs/ui.md](docs/ui.md): Web UI
- [docs/api.md](docs/api.md): Job API and live log stream (SSE)
- [docs/secrets.md](docs/secrets.md): Encrypted repository secrets and masking in logs
- [docs/metrics.md](docs/metrics.md): Prometheus metrics
- [docs/tracing.md](docs/tracing.md): OpenTelemetry traces: one trace from the webhook to the steps
- [docs/accounts.md](docs/accounts.md): User accounts, roles, sign-in and personal tokens
- [docs/polling.md](docs/polling.md): Push builds without webhooks: polling branches
- [docs/forges.md](docs/forges.md): Setting up GitHub, Gitea, Forgejo, GitLab and Bitbucket, repository names and private repositories
- [docs/commit-status.md](docs/commit-status.md): Showing job results on the forges as commit statuses
- [docs/webhook.md](docs/webhook.md): Webhook setup and behavior
- [docs/runner.md](docs/runner.md): Docker runner usage and lifecycle
- [docs/server.md](docs/server.md): Server configuration, endpoints and migrations
- [docs/agent.md](docs/agent.md): Agent configuration, WebSocket protocol and reconnection
- [docs/hub.md](docs/hub.md): Job dispatch on the server side, ownership rules and graceful shutdown
- [docs/deployment.md](docs/deployment.md): Installing with Docker images, docker compose and Kubernetes
- [docs/release.md](docs/release.md): Publishing a release: images and the chart go to ghcr.io through GitHub Actions
- [deployments/helm/cisid](deployments/helm/cisid/README.md): Helm chart
- [docs/security.md](docs/security.md): Trust boundaries, security measures and a production checklist
- [docs/adr/](docs/adr): Architecture decision records (e.g. why the agent protocol is not gRPC)

## Architecture

```
   ┌──────────────┐        ┌──────────────┐
   │  Git Webhook │        │  CLI / Web   │
   └──────┬───────┘        └──────┬───────┘
          │        HTTP           │
          └───────────┬───────────┘
                      ▼
        ┌───────────────────────────┐
        │     cmd/server (API)      │
        │                           │
        │  internal/api   handlers  │
        │  internal/hub   dispatch  │
        │  internal/db    storage   │
        └──────┬─────────────▲──────┘
               │             │
       SQL     │             │  job_assigned ▼ / ▲ job_log, job_result
               ▼             │  (WebSocket, see docs/adr/001)
      ┌────────────────┐     │
      │   PostgreSQL   │     │
      │ pipelines,     │     │
      │ jobs, logs     │     │
      └────────────────┘     │
                             │
        ┌────────────────────┴──────┐
        │     cmd/agent (worker)    │   ×N
        │                           │
        │  internal/agent   ws, job │
        │  internal/runner  docker  │
        └─────────────┬─────────────┘
                      │ Docker API
                      ▼
        ┌───────────────────────────┐
        │     Docker Engine         │
        │  ┌───────┐ ┌───────┐      │
        │  │ step1 │ │ step2 │ ...  │
        │  └───────┘ └───────┘      │
        └───────────────────────────┘

        pkg/models: types shared by the server and the agent
```

**Flow**

1. The forge's (GitHub, Gitea, Forgejo, GitLab, Bitbucket) push webhook reaches the `server`, and the job is written to PostgreSQL as `pending`.
2. `agent` processes take pending jobs from the `server`.
3. The `runner` runs each step in its own Docker container.
4. Logs and results are written back to the database through the `server`.

## Directory layout

```
.
├── cmd/
│   ├── server/      # API server (main)
│   └── agent/       # Worker binary (main)
├── internal/
│   ├── agent/       # Agent: WebSocket connection, job execution
│   ├── api/         # HTTP router, handlers, middleware, access
│   ├── auth/        # Roles, passwords, session and token secrets
│   │   └── testdata/    # Sample GitHub webhook payloads
│   ├── config/      # Server configuration from environment variables
│   ├── events/      # Job change notifications over PostgreSQL LISTEN/NOTIFY
│   ├── hub/         # Agent connections and job dispatch (server side)
│   ├── metrics/     # Prometheus metrics
│   ├── forge/       # Forge-independent interface, repository keys
│   ├── github/      # GitHub API client
│   ├── gitea/       # Gitea and Forgejo API client
│   ├── gitlab/      # GitLab API client
│   ├── bitbucket/   # Bitbucket Cloud API client
│   ├── pipeline/    # .cisid.yml parsing, job → steps
│   ├── secrets/     # Secrets encrypted with AES-256-GCM
│   ├── status/      # Commit status reporting
│   ├── telemetry/   # OpenTelemetry trace setup and trace context propagation
│   ├── trigger/     # Manual, scheduled and polled builds
│   ├── web/         # Embedded web UI
│   ├── runner/      # Docker integration
│   └── db/          # PostgreSQL access (pgx)
│       ├── dbtest/      # Temporary databases for tests
│       └── migrations/  # SQL schema files
├── pkg/
│   └── models/      # Shared types
├── api/
│   └── proto/       # gRPC definitions (future)
├── deployments/
│   └── helm/
│       ├── cisid/       # Helm chart
│       └── charttest/   # Chart tests (separate Go module)
└── docs/            # Module documentation
```

## Requirements

- Go 1.25+
- PostgreSQL 14+
- Docker (for the runner and the agent)

## Running locally

```bash
docker compose up -d --wait postgres
```

```bash
CISID_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' CISID_GITHUB_WEBHOOK_SECRET=dev-secret go run ./cmd/server
```

```bash
go run ./cmd/agent
```

The agent connects to `ws://localhost:8080/agent/ws`. When a push arrives, it takes the job, clones the repository and runs the [`.cisid.yml`](docs/pipeline.md) steps in Docker.

Details: [docs/server.md](docs/server.md), [docs/agent.md](docs/agent.md).

## Running with Docker

This builds the stack of PostgreSQL, the server and one agent from source and starts it (`.env` needs at least `CISID_GITHUB_WEBHOOK_SECRET` and `CISID_AGENT_TOKEN`):

```bash
docker compose -f compose.stack.yaml up -d --build --wait
```

Details: [docs/deployment.md](docs/deployment.md).

## Build and test

```bash
go build ./...
```

```bash
go vet ./...
```

```bash
go test ./...
```

The race detector needs cgo. On Linux/macOS it runs directly:

```bash
go test -race ./...
```

On Windows without a C compiler, you can run it with Docker (PowerShell, from the project root):

```bash
docker run --rm -v "${PWD}:/src" -v cisid-gomod:/go/pkg/mod -w /src golang:1.27 go test -race ./...
```

Tests that need PostgreSQL (after `docker compose up -d postgres`):

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/db/ ./internal/hub/ ./cmd/server/
```

Integration tests against real Docker (they pull the alpine image):

```bash
CISID_DOCKER_TESTS=1 go test ./internal/runner/ ./internal/agent/ ./internal/pipeline/ -run Integration -v
```

The Helm chart tests live in a separate Go module (so the Helm SDK adds no dependencies to the main module):

```bash
cd deployments/helm/charttest && go test ./...
```

## License

[MIT](LICENSE)
