# Agent

**English** · [Türkçe](tr/agent.md)

`cmd/agent` starts a worker process. The agent connects to the server over WebSocket, runs assigned jobs with the [Docker runner](runner.md), streams logs to the server live and reports the result. The core logic lives in the `internal/agent` package.

## Running

```bash
CISID_SERVER_URL=ws://localhost:8080/agent/ws CISID_AGENT_NAME=worker-1 go run ./cmd/agent
```

| Variable | Default | Description |
|---|---|---|
| `CISID_SERVER_URL` | `ws://localhost:8080/agent/ws` | The server's agent endpoint (`ws://` or `wss://`) |
| `CISID_AGENT_NAME` | hostname | Name reported to the server (`X-Cisid-Agent` header and `agent_hello`) |
| `CISID_AGENT_TOKEN` | — | Agent token the server requires. Sent in the `Authorization: Bearer` header. If you give a comma-separated list, the first one is sent. |
| `CISID_SERVER_CA_FILE` | — | PEM CA file to trust for `wss://` (e.g. a self-signed or internal company CA) |
| `CISID_STEP_MEMORY` | — | Memory limit for each step and service container (`512m`, `2g`). Swap is not available, and a container that exceeds the limit is killed. |
| `CISID_STEP_CPUS` | — | CPU limit per container (`2`, `0.5`) |
| `CISID_STEP_PIDS` | `4096` | Process limit per container (fork bomb protection) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` etc. | — | OTLP/HTTP collector to send traces to. If not set, tracing is off ([tracing.md](tracing.md)). |
| `DOCKER_HOST` etc. | Docker default | Docker daemon connection (`client.FromEnv`) |

`SIGINT` / `SIGTERM` starts a graceful shutdown (see below).

To run the agent as a container, see [deployment.md](deployment.md).

## Protocol

Each message is a JSON envelope. The types are in `pkg/models/protocol.go`.

```json
{"id": 17, "type": "<message type>", "payload": { ... }}
```

`id` is present only in the `job_log`, `job_step`, `job_result` and `job_rejected` messages the agent sends (see [Acknowledged delivery](#acknowledged-delivery)).

| Type | Direction | Payload |
|---|---|---|
| `agent_hello` | agent → server | `{"name":"worker-1","capacity":1,"running_job_id":42}`, the first message on every connection. `running_job_id`: the job the agent holds or whose result it has not sent yet (the field is omitted if there is none) |
| `job_assigned` | server → agent | `{"job_id":42,"steps":[{"name":"test","image":"golang:1.25","commands":["go test ./..."],"env":{},"secrets":{},"timeout_seconds":600,"depends_on":[0]}],"services":[{"name":"postgres","image":"postgres:18-alpine","env":{}}],"trace_parent":"00-..."}`. `trace_parent` is present only if the server produces traces ([tracing.md](tracing.md)) |
| `job_rejected` | agent → server | `{"job_id":43,"reason":"agent busy"}` |
| `job_log` | agent → server | `{"job_id":42,"step":0,"stream":"stdout","seq":17,"data":"ok ...\n"}` |
| `job_step` | agent → server | `{"job_id":42,"step":1,"state":"running","started_at":"..."}` when a step starts, and with `success`, `failed` or `canceled`, `exit_code` and `finished_at` when it ends. Servers that do not know the type acknowledge and ignore it. |
| `job_result` | agent → server | `{"job_id":42,"status":"failed","exit_code":3,"failed_step":0,"error":"...","steps":[{"state":"success",...},{"state":"failed",...}]}`. `steps` repeats how every step went (`skipped` for steps that never started) and replaces what `job_step` reported |
| `job_cancel` | server → agent | `{"job_id":42}`. Stop the job and report the result as `canceled` with `canceled by request` |
| `ack` | server → agent | `{"id":17}`. All messages up to and including 17 are processed |
| `server_closing` | server → agent | `{}`. The server is shutting down: the agent stops sending and closes the connection itself (see [hub.md](hub.md#graceful-shutdown)) |

`seq` starts at 1 in each job and goes up by one with each log chunk. The server uses it to detect ordering and missing chunks (gaps).

## Job execution

- The agent runs **one** job at a time. It answers an assignment that arrives while it is busy with `job_rejected`. It also answers an assignment that arrives during shutdown with `job_rejected`, with the reason `"agent shutting down"`.
- If the assignment has services, the agent first creates a Docker network for the job and starts the services on that network, with their names as host names. Steps run on the same network. When the job ends, the services and the network are removed ([pipeline.md](pipeline.md#services)).
- Steps run in order, and the job stops at the first failing step.
- Each job gets its own Docker volume (`cisid-job-<id>-<random>`). The volume is mounted at `/workspace` in all steps and serves as the working directory. When the job ends, the volume is removed.
- Commands run as a single shell script: `/bin/sh -c "set -ex\n<commands>"`. So the script stops at the first failing command, and each command is written to stderr before it runs.
- The secrets a step asks for are added as environment variables and masked as `***` in the step's output ([secrets.md](secrets.md#masking-in-logs)).
- Every step gets these environment variables: `CI=true`, `CISID=true`, `CISID_JOB_ID`, `CISID_STEP`. The step's own `env` field cannot override them.
- `timeout_seconds` is passed to the runner's `Step.Timeout` field.

| Outcome | `status` | `exit_code` | `error` |
|---|---|---|---|
| All steps exited with 0 | `success` | 0 | - |
| A step exited with a non-zero code | `failed` | the step's code | - |
| Runner error (pull, timeout, ...) | `failed` | -1 | error message |
| Invalid assignment (no step, image or command) | `failed` | -1 | `invalid job ...` |
| Agent shut down | `canceled` | -1 | `context canceled` |

## Connection and reconnection

- If the connection drops or cannot be established, the agent retries with **exponential backoff + jitter**. The wait starts at 1 s, doubles on each attempt and is capped at 1 min. The actual wait is picked at random in the range `[d/2, d]`.
- The backoff resets after a successful connection.
- Jobs are tied to the agent's lifetime, not to the connection. If the connection drops, the running job continues.
- Outgoing messages wait in an **outbox** that lives across connections. The outbox holds at most 4096 pending or unacknowledged messages. Logs and results produced while disconnected are sent after the agent reconnects.
- If the outbox is full, new **log** chunks are dropped. The job does not slow down, and the dropped chunks show up as `seq` gaps. **Result** messages are never dropped: they wait for space to free up.

## Acknowledged delivery

- The agent gives each message an `id` that increases at the moment the message is sent. So the `id` order matches the send order.
- A message stays in the outbox until the server acknowledges it with `ack`. `ack` is cumulative: `{"id":17}` acknowledges all messages up to and including 17.
- However the connection drops (network error, server crash), unacknowledged messages are resent first on the new connection, with the same `id`.
- The server is safe against duplicates:
  - Log chunks are written once, keyed by `(job_id, seq)`.
  - A result or rejection that arrives a second time is ignored and still acknowledged.
- A job whose result is not yet acknowledged is still reported as `running_job_id` in the hello.
- On shutdown, the agent waits up to `FlushTimeout` for all messages to be **acknowledged**.

`TestIntegrationLogsSurviveNetworkFailures` cuts the TCP connection three times during a 300-line log stream and verifies that each chunk is written exactly once, in order.

## Concurrency

| Goroutine | Task | When it ends |
|---|---|---|
| executor | Runs jobs one after another | When the agent context is canceled |
| writer | The **only** goroutine that writes to the connection | When the connection closes |
| reader | Reads messages and hands jobs to the executor | When the connection closes |
| flush | Drains the outbox on shutdown, then closes the connection | When the executor finishes |

Writer and reader run in an `errgroup`. If one fails, the other stops too.

## Graceful shutdown

1. The agent context is canceled (`SIGTERM`).
2. The runner stops the running step's container: first `SIGTERM`, then `SIGKILL` at the end of the grace period.
3. The `canceled` result is written to the outbox, and the workspace volume is removed.
4. The outbox is drained for at most `FlushTimeout` (10 s).
5. The connection is closed with `1000 Normal Closure`.

## Container isolation

- All step and service containers run with `no-new-privileges`: setuid binaries cannot gain privileges beyond those the container started with. Containers never start as `--privileged`.
- The limits set with `CISID_STEP_*` apply to each container separately. The process limit defaults to 4096.
- The kernel kills a step that exceeds the memory limit. The job becomes `failed`, with `exit_code` 137 and `error` `out of memory: …`.
- Jobs with services run in their own Docker network and cannot reach the services of other jobs.
- The agent runs with full access to the Docker daemon, so treat the agent machine as the place where untrusted code runs. Run each agent on a separate, isolated machine or VM.

## Authentication and TLS

- If `CISID_AGENT_TOKEN` is set on the server, agents cannot connect without presenting the same token (`401`). If the agent's token is rejected, the agent logs it and keeps retrying with backoff. So when you change the token on the server, restarting the agent is enough.
- Tokens and secrets travel over the connection, so connect to remote servers with `wss://`. The agent warns if it sends the token to a non-local address over unencrypted `ws://`.
- All agents share one token. To rotate the token, give the server `new,old`: it accepts both. After the agents switch to `new`, remove `old` from the list. The old token is then rejected. Agents also accept the list and send the first token, so the server and the agents can share the same secret.

## Known limitations

- If an agent cannot connect for longer than 5 minutes (the lease), its job goes to another agent. When it comes back, the result of its original job is discarded (see [hub.md](hub.md#liveness-and-leases)).

## Cache volumes

If the assignment has `cache`, the agent mounts a volume named `cisid-cache-<repository key>-<path digest>` for each path in all steps ([pipeline.md](pipeline.md#cache)). Docker creates the volume on first use, with the `cisid.cache` and `cisid.cache.path` labels. The agent does not remove these volumes: they are meant to persist between jobs. If the disk fills up, clean them up by label:

```bash
docker volume prune -a --filter label=cisid.cache
```

On Kubernetes, the Docker-in-Docker sidecar's data is deleted with the pod. To keep the cache across pod restarts, set `agent.docker.dind.persistence.enabled=true` in the chart.

## Tests

| Test | Scenario |
|---|---|
| `TestAgentRunsAssignedJob` | Two steps: the `Step` passed to the runner, log order and `seq`, result, volume cleanup |
| `TestAgentReportsFailedStep` | Non-zero exit code, the next step does not run |
| `TestAgentReportsRunnerError` | Image pull error |
| `TestAgentFailsInvalidAssignment` | Missing step, image or command |
| `TestAgentMountsCaches`, `TestAgentRejectsBadCaches` | Cache volume names, labels, read-only mounts; invalid keys and paths |
| `TestDockerIntegrationCache` | Real Docker: the next job finds the cache, a read-only job cannot write to it |
| `TestAgentRejectsJobWhileBusy` | The second assignment is rejected |
| `TestAgentReconnectsAfterConnectionLoss` | The server closes the connection, the agent reconnects |
| `TestAgentRetriesWhileServerUnavailable` | The handshake gets 503 three times and connects on the 4th attempt |
| `TestAgentDeliversResultAfterReconnect` | The result of a job that finishes while disconnected arrives over the new connection |
| `TestAgentShutdownCancelsRunningJob` | On shutdown a `canceled` result is sent and `Run` returns cleanly |
| `TestAgentIgnoresUnknownAndMalformedMessages` | Malformed or unknown messages do not bring the agent down |
| `TestAgentHelloReportsUndeliveredResult` | A job whose result could not be sent is reported in the hello after reconnecting |
| `TestAgentClosesConnectionWhenServerDrains` | On `server_closing` the agent closes cleanly and reconnects |
| `TestAgentResendsUnacknowledgedMessages` | Unacknowledged logs and results are resent on the new connection with the same `id` |
| `TestOutbox*` | Cumulative ack, outbox limit |
| `TestBackoff*` | Growth, upper bound, overflow, jitter range |
| `TestDockerIntegrationJob` | Real Docker: steps share the workspace, the exit code is reported |

The server side is a real WebSocket endpoint set up with `httptest`. The runner is mocked with `testify/mock`.

```bash
go test ./internal/agent/
```

```bash
CISID_DOCKER_TESTS=1 go test ./internal/agent/ -run Integration -v
```
