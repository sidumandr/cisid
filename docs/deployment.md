# Deployment

**English** · [Türkçe](tr/deployment.md)

cisid consists of two binaries: `cisid-server` (API, hub, web UI) and `cisid-agent` (the worker that runs jobs in Docker). Both are built from a single `Dockerfile`. On a single machine, use docker compose; on Kubernetes, use the Helm chart ([Kubernetes](#kubernetes)).

For a team on one server inside the company network, with HTTPS, sign-up and polling set up, follow [team-server.md](team-server.md).

## Images

```bash
docker build --target server -t cisid-server .
```

```bash
docker build --target agent -t cisid-agent .
```

| Image | Base | User | Note |
|---|---|---|---|
| `server` | `alpine:3.22` | uid 10001 | Needs no privileges, listens on port `8080` |
| `agent` | `alpine:3.22` | root | To access the host's Docker socket |

The binaries are built statically with `CGO_ENABLED=0`. The images contain no Go toolchain and no source code.

## The full stack with docker compose

`compose.stack.yaml` starts PostgreSQL, the server and one agent. It takes the PostgreSQL definition from `compose.yaml`. On its own, `compose.yaml` runs only PostgreSQL for development and needs no variables.

Create a `.env` file in the project root (do not add it to git):

```
CISID_GITHUB_WEBHOOK_SECRET=a-long-random-value
CISID_AGENT_TOKEN=random-token-of-at-least-16-chars
CISID_API_TOKEN=write-token-of-at-least-16-chars
CISID_SECRET_KEY=base64-32-byte-key
CISID_GITHUB_TOKEN=github-token-for-commit-status
CISID_PUBLIC_URL=https://ci.example.com
# Other forges, e.g. the office Gitea:
CISID_GITEA_URL=https://git.example.com
CISID_GITEA_TOKEN=gitea-access-token
CISID_GITEA_WEBHOOK_SECRET=a-long-random-value
# For forges whose webhooks cannot reach the server (see docs/polling.md):
CISID_POLL_INTERVAL=1m
```

`CISID_AGENT_TOKEN` is required; compose does not start without it. The forge settings ([forges.md](forges.md)) and the other variables are optional. For what the others mean, see [server.md](server.md) and [secrets.md](secrets.md). To avoid typing `-f` in every command, you can add `COMPOSE_FILE=compose.stack.yaml` to `.env`.

```bash
docker compose -f compose.stack.yaml up -d --build --wait
```

The server opens at `http://localhost:8080` (change it with `CISID_HTTP_PORT`). Webhook URLs are the `/webhooks/<forge>` paths, e.g. `/webhooks/github` or `/webhooks/gitea` ([forges.md](forges.md)). `OTEL_EXPORTER_OTLP_ENDPOINT` in `.env` is passed to the server and the agent and turns on traces ([tracing.md](tracing.md)).

More agents:

```bash
docker compose -f compose.stack.yaml up -d --scale agent=3
```

Each agent uses its own container hostname as its name. If you want a fixed name, set `CISID_AGENT_NAME`, but then do not use `--scale`: a second agent that connects with the same name takes over the first one's connection.

To stop (the database volume is kept):

```bash
docker compose -f compose.stack.yaml down
```

### Agents and the Docker socket

The agent starts step and service containers on the host's Docker daemon (`/var/run/docker.sock` is mounted). This gives the agent container root-equivalent access on the host. Run the agent only on a machine dedicated to CI, and do not run jobs from repositories you do not trust on that machine. For container limits, see [runner.md](runner.md).

Because steps run on the host daemon and not inside the agent container, the workspace is a Docker volume. The agent's file system is not used.

### TLS

In the compose stack, the agent connects to the server over compose's private network with `ws://server:8080`. Because it sends the token unencrypted, it logs a warning at startup. If agents run on other machines, run the server with TLS (`CISID_TLS_CERT_FILE`, `CISID_TLS_KEY_FILE`) or put it behind a reverse proxy, and connect the agents with `wss://` ([agent.md](agent.md)).

## Production notes

- The server is stateless: all state is in PostgreSQL. When the server restarts, agents reconnect and running jobs continue where they left off ([hub.md](hub.md)).
- Multiple server replicas can share the same database: jobs are dispatched with `FOR UPDATE SKIP LOCKED`, ownership is kept in the database with leases, the log stream reaches all replicas through PostgreSQL `LISTEN/NOTIFY`, and schedules run on a single replica. `TestRunTwoServers` tests this end to end: a push that arrives at one replica runs on an agent connected to the other, and the first replica streams the logs live and can cancel the job. If only agents connected to another replica can take a job that arrives at one replica, the job waits for up to one job polling interval, at most 5 seconds.
- Back up the database regularly. Secrets and users' forge tokens cannot be decrypted without `CISID_SECRET_KEY`, so store the key separately.
- If the server is exposed to the internet, set `CISID_READ_TOKEN`; jobs, logs and `/metrics` then require the token ([api.md](api.md#read-access)). `/healthz` and `/readyz` stay open.

## Kubernetes

The [deployments/helm/cisid](../deployments/helm/cisid/README.md) chart installs the server as a Deployment and the agents as a StatefulSet with a Docker-in-Docker sidecar. For trying it out, it can also install a single-replica PostgreSQL. Installation, TLS, an external database and security notes are in the chart's README.
