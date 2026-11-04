# Roadmap

**English** · [Türkçe](ROADMAP.tr.md)

Each phase builds on the previous one. An item counts as done together with its own tests (TDD, see [CLAUDE.md](CLAUDE.md)).

References: Woodpecker CI (pipeline/runner), Drone CI (agent ↔ server), Tekton (step/workspace model).

## Phase 1 — MVP

Goal: push to GitHub → a command runs in a container → the result shows up in the database.

- [x] **Webhook handler.** `internal/api/webhook.go`: HMAC-SHA256 verification, repository/branch/SHA from the push payload, a `pending` row in the `jobs` table, `202 Accepted`.
- [x] **Server skeleton.** `cmd/server`: config (env), pgx pool, migrations, HTTP router, graceful shutdown.
- [x] **Agent hub + job dispatch.** The `/agent/ws` endpoint on the server, assigning pending jobs to agents (`FOR UPDATE SKIP LOCKED`), storing `job_log` and `job_result` messages.
- [x] **Docker runner.** `internal/runner/docker.go` (`github.com/moby/moby/client`): image pull (progress callback), container create (env, workdir, mount), start, stdout/stderr `io.Reader`, exit code, timeout, context cancellation. Mock Docker client tests with `testify/mock`.
- [x] **Pipeline definition.** Read the `.cisid.yml` file in the repository from GitHub and parse it (image + command list), run it after the clone step.
- [x] **Agent (basic).** `cmd/agent`: connect to the server over WebSocket, listen for `job_assigned`, call the runner, report the result.

## Phase 2 — Streaming

Goal: logs stream live, and dropped connections lose no work.

- [x] Live log streaming from agent to server (WebSocket, a single writer goroutine), and writing the logs to the database.
- [x] Live log broadcast from server to clients and a log read API. SSE is used instead of WebSocket, because the stream is one-way and SSE resumes where it left off with `Last-Event-ID`. It works across servers through PostgreSQL `LISTEN/NOTIFY`.
- [x] Agent reconnect: exponential backoff + jitter.
- [x] Job lease + heartbeat: the job of an agent that never comes back returns to the queue when the lease expires, and an agent that comes back takes its job back.
- [x] Acknowledged (ack) delivery of messages: on a dropped connection, unacknowledged messages are sent again, and the server is safe against duplicates.
- [x] Message protocol types live under `pkg/models`.

## Phase 3 — Features

- [x] Multi-step pipelines, a workspace volume shared between steps.
- [x] Service containers (e.g. PostgreSQL for tests), on a separate Docker network per job.
- [x] Secret management: encrypted storage, injection as env, masking in logs ([docs/secrets.md](docs/secrets.md)).
- [x] Branch filters (`when.branch`).
- [x] Matrix build: each combination is a separate job that can run in parallel ([docs/pipeline.md](docs/pipeline.md#matrix)).
- [x] GitHub commit status reporting ([docs/commit-status.md](docs/commit-status.md)).
- [x] Job cancel / rerun API (token protected).
- [x] Simple web UI: job list and a job page with live logs ([docs/ui.md](docs/ui.md)).

## Phase 4 — Production

- [ ] ~~Move agent ↔ server communication to gRPC (`api/proto`).~~ Postponed, the protocol stays on WebSocket: [ADR 001](docs/adr/001-agent-protocol-websocket.md).
- [x] Agent authentication (shared token) and TLS (`CISID_TLS_*`, `wss://` and a custom CA on the agent).
- [x] Observability: `slog` structured logs and Prometheus metrics ([docs/metrics.md](docs/metrics.md)).
- [x] OpenTelemetry traces: webhook, dispatch, GitHub requests, services and steps on the agent, and commit status in a single trace; export over OTLP/HTTP ([docs/tracing.md](docs/tracing.md)).
- [x] Container isolation and resource limits: memory, CPU, process count, `no-new-privileges`, a network per job, OOM detection.
- [x] Rate limiting (token bucket per IP) and webhook replay protection (deduplication by signed body, and rejecting payloads older than 7 days).
- [x] Deployment: Docker images and a docker compose stack ([docs/deployment.md](docs/deployment.md)).
- [x] Helm chart: server, agents with a Docker-in-Docker sidecar, optional PostgreSQL, TLS, ingress; tests with the Helm SDK ([deployments/helm/cisid](deployments/helm/cisid/README.md)).
- [x] Running cisid's own CI on cisid (`.cisid.yml`).

## Phase 5 — Ready for use

- [x] MIT license.
- [x] Log retention: logs of finished jobs are deleted after `CISID_LOG_RETENTION` ([docs/server.md](docs/server.md)).
- [x] Pull request builds: a new commit on a pull request cancels its older builds; forks are optional and get no secrets ([docs/webhook.md](docs/webhook.md#pull-requests)).
- [x] Limiting secrets to branches on the server side; limited secrets are not given to pull requests ([docs/secrets.md](docs/secrets.md)).
- [x] A token for read access (`CISID_READ_TOKEN`, a session cookie for the UI) and agent token rotation ([docs/api.md](docs/api.md#read-access)).
- [x] Cache between jobs: `cache` in `.cisid.yml`, Docker volumes per repository; read-only for fork pull requests ([docs/pipeline.md](docs/pipeline.md#cache)).
- [x] Manual and scheduled builds: branch builds from the API and the UI, cron schedules in the database; multiple server replicas do not start the same run twice ([docs/api.md](docs/api.md#manual-and-scheduled-builds)).
- [x] End-to-end test of multiple server replicas: two servers on the same database, dispatch across replicas, live logs and cancel (`TestRunTwoServers`).
- [x] Publishing releases: on a `v*` tag, GitHub Actions runs the tests, pushes the images (amd64, arm64) and the chart to ghcr.io, and creates a release ([docs/release.md](docs/release.md)).

## Phase 6 — For everyone

- [x] First release: v0.1.0, images and the Helm chart on ghcr.io ([docs/release.md](docs/release.md)).
- [x] Multiple git forges: GitHub, Gitea, Forgejo, GitLab and Bitbucket Cloud; webhooks, commit status, cloning private repositories ([docs/forges.md](docs/forges.md)).
- [x] Push builds without webhooks: polling branches; multiple server replicas do not build the same commit twice ([docs/polling.md](docs/polling.md)).
- [x] User accounts and roles (viewer, maintainer, admin), password sign-in, personal API tokens, CSRF protection ([docs/accounts.md](docs/accounts.md)).
- [x] Sign-in through forges (OAuth): GitHub, Gitea, Forgejo, GitLab, Bitbucket; accounts match on the forge's immutable user ID, with optional automatic account creation ([docs/accounts.md](docs/accounts.md#signing-in-through-a-forge)).
- [x] Documentation in English and Turkish: English at the usual paths, Turkish in `docs/tr/` and the `.tr.md` files.

## Phase 7: Hearing about builds

- [x] Notifications: Slack-compatible chats, signed webhooks and email, for every failure, breaks, fixes or every result, per branch and optionally for pull requests; each result is sent once even with several server replicas ([docs/notifications.md](docs/notifications.md)).
- [x] Build badges for READMEs, with `CISID_PUBLIC_BADGES` for servers where reading needs a sign-in ([docs/notifications.md](docs/notifications.md#badges)).

## Phase 8: Faster and fuller pipelines

- [x] Parallel steps: `depends_on` in `.cisid.yml`, the first failure stops the other steps; agents report each step's state and duration, shown live in the UI ([docs/pipeline.md](docs/pipeline.md#parallel-steps)).
- [x] Artifacts: `artifacts` in `.cisid.yml`, collected once the steps pass, uploaded by the agent, kept in PostgreSQL with the logs and downloaded from the job page ([docs/pipeline.md](docs/pipeline.md#artifacts)).
- [ ] Agent labels: send jobs only to agents that have what they need (e.g. `arm64`, `gpu`).
- [ ] Approval for pull requests from forks: a maintainer starts their builds.
