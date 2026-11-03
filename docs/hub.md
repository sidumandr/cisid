# Agent Hub

**English** · [Türkçe](tr/hub.md)

`internal/hub` is the server side of the agent protocol. It holds the agents' WebSocket connections, hands pending jobs to idle agents and writes the logs and results that agents send to the database. It runs on the server at `GET /agent/ws`.

## Flow

```
GitHub ──webhook──► jobs (pending) ──Wake──► dispatcher
                                                 │ ClaimJob (FOR UPDATE SKIP LOCKED)
                                                 │ Planner.Plan → steps
                                                 ▼
                                     job_assigned ──► agent
                                                       │
            job_logs ◄── AppendJobLog ◄── job_log ◄────┤
            jobs     ◄── FinishJob    ◄── job_result ◄─┘
```

1. A webhook creates a job (`pending`) and wakes the hub (`Wake`). The hub also checks on its own every 5 seconds, so it does not miss jobs that another server instance created.
2. The dispatcher calls `ClaimJob` for each idle agent. This query sets the oldest `pending` job to `running` and assigns it to the agent. Thanks to `FOR UPDATE SKIP LOCKED`, the same job never goes to two agents, even when several servers run at the same time.
3. The `Planner` produces the job's steps, and the hub sends `job_assigned` to the agent. If `.cisid.yml` is missing or invalid, the job becomes `failed` and the hub moves on to the next job. If GitHub is temporarily unreachable, the job goes back to the queue.
4. `job_log` messages from the agent are written to the `job_logs` table, and the `job_result` message to the `jobs` table. When the result arrives, the agent is idle again and the hub looks for the next job.

## Planner

`internal/pipeline` produces the job's steps: first **clone**, then the steps from `.cisid.yml` in the pushed commit ([pipeline.md](pipeline.md)). If the plan cannot be produced for a temporary reason, the job does not count as failed. The job goes back to the queue and that dispatch round stops, so the same job is not picked up again right away.

Values from the webhook (clone URL, branch, SHA) are not written into the script. They are passed as environment variables and used only inside quotes in the script (`"$CISID_CLONE_URL"`). So the script is not open to shell injection.

## Ownership rules

- An agent can send logs and results only for the job it **holds**. Messages for any other job are dropped, and a warning is logged.
- `FinishJob` succeeds only if the job is `running` on that agent. A result cannot be written to a job that was requeued or has finished.
- If a second connection arrives with the same name, the old one is closed. This cleans up the half-open old connection of an agent that dropped and reconnected.

## Reconnection and lost jobs

On every connection, the agent reports the job it holds in `agent_hello` (`running_job_id`). It also reports a job whose result it has not sent yet. The hub uses this to do two things:

- It requeues the **other** `running` jobs that the database shows as assigned to this agent (`RequeueAgentJobs`). This happens in two ways: the assignment message never reached the agent, or the agent restarted.
- It gives the agent no new work until the reported job finishes.

If the server restarts, the agent reconnects, the running job continues and its result is delivered to the new server (`TestIntegrationJobSurvivesServerRestart`).

If an agent rejects a job (`job_rejected`), the job goes back to the queue. The agent gets no new work until it reports a result or reconnects. This prevents a loop of "keep sending work to a busy agent and getting rejected".

## Acknowledged delivery

The hub acknowledges each numbered message from the agent after it processes it (after writing it or deliberately dropping it). Under load, the writer goroutine merges acknowledgements and sends a single `ack` for the last processed `id`. The agent resends unacknowledged messages on the new connection. See [agent.md](agent.md#acknowledged-delivery) for details.

## Graceful shutdown

The WebSocket library's `Close` method **reads and discards** incoming data messages while it waits for the closing handshake. If the hub closed the connection itself, the logs and results the agent sent at that moment would be lost. So shutdown works like this:

1. The hub sends `server_closing` to each agent.
2. The agent finishes the message it is writing, stops taking new messages from the outbox and closes the connection **itself**.
3. The hub processes and acknowledges every message that arrives before the agent's close frame. Unsent or unacknowledged messages stay in the agent's outbox and are delivered to the new server.
4. An (old) agent that does not do this has its connection force-closed after 5 s (`WithDrainTimeout`).

## Liveness and leases

The hub sends a WebSocket ping to each connection every 30 s. If the pong does not arrive within 10 s, the hub closes the connection, so dead connections are detected.

Each `running` job has a **lease** (`jobs.lease_expires_at`, 5 min by default, `WithLease`):

- `ClaimJob` hands out the job with a lease.
- Every lease/3, the hub renews the leases of the jobs held by agents connected to it (`RenewLeases`).
- In the same round, it requeues jobs whose lease has expired (`RequeueExpired`) and dispatches them right away. When a job's lease expires, its agent has not been connected to any server for that whole time.
- When an agent's connection drops, its job waits until the lease expires. So short disconnects and server restarts do not rerun the job.
- If a returning agent reports its job in the hello, the job is reattached to it (`AdoptJob`), even if the lease has expired. The only condition is that the job has not gone to another agent in the meantime. If it has, the old agent's result is discarded.

All times come from the database clock (`now()`). Clock differences between servers do not affect the result.

## Known limitations

- Each log chunk is written with a separate `INSERT`. Jobs that produce a lot of logs may need batched writes.
- With several server replicas, agents connected to another replica pick up a job that arrived at one replica only on their own replica's next poll (at most 5 seconds). The wake-up is immediate only on the replica that received the job.

## Tests

| Test | Coverage |
|---|---|
| `hub_test.go` | Mock store and planner, raw WebSocket client: job lifecycle, required hello, reconnection, rejection, ownership, plan error, multiple agents, connection replacement, shutdown with and without drain |
| `hub_integration_test.go` | Real PostgreSQL and a real agent: successful and failed jobs, a server restart while a job runs, a lost agent's job moving to another agent when the lease expires |
| `cmd/server` `TestRunEndToEnd` | Real server, real agent and a signed webhook: result and logs in the database |

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/hub/ ./cmd/server/
```
