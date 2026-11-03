# Traces (OpenTelemetry)

**English** · [Türkçe](tr/tracing.md)

The server and the agent can export a job's journey as an OpenTelemetry trace: the webhook arriving, the job being dispatched, `.cisid.yml` being read from GitHub, the services and steps on the agent, and the commit status being reported. All of it shows up in a single trace, linked together even though it comes from two separate processes. The code is in the `internal/telemetry` package.

Tracing is off by default. While it is off, no spans are created and no wrapper is added to the HTTP layer.

## Turning it on

Give the server and the agents the address of an OTLP/HTTP collector:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/server
```

| Variable | Description |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base address of the collector. Traces are sent to `<address>/v1/traces`. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Full address to send traces to. If set, it replaces the one above. |
| `OTEL_SDK_DISABLED` | If `true`, tracing is off even when an address is set. |
| `OTEL_SERVICE_NAME` | Service name. Defaults to `cisid-server` and `cisid-agent`. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Headers to send to the collector, e.g. `authorization=Bearer ...`. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | Sampling. Defaults to `parentbased_always_on`. |
| `OTEL_RESOURCE_ATTRIBUTES` | Extra resource attributes, e.g. `deployment.environment=prod`. |

These are the standard OpenTelemetry variables. Other `OTEL_EXPORTER_OTLP_*` settings, such as certificates and timeouts, work too. Only OTLP/HTTP (`http/protobuf`) is supported. If you set `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`, the process does not start and prints the reason ([ADR 001](adr/001-agent-protocol-websocket.md)).

If the collector is unreachable, jobs are not affected: only the spans are lost. On shutdown, pending spans are sent within at most 5 seconds.

The official OTLP exporter adds the gRPC packages to the binary even when only HTTP is used. As a result, the server and agent binaries grew by about 7 MB (stripped: server 14.8 → 22.2 MB, agent 8.4 → 15.5 MB). We accepted this cost instead of writing our own exporter.

### Trying it locally with Jaeger

Jaeger accepts OTLP/HTTP on port `4318` and serves its UI on port `16686`:

```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2.11.0
```

Start the server and the agent with `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318`, send a push and open the `cisid-server` service at `http://localhost:16686`. To find a specific job, search for the tag `cisid.job.id=42`.

The Compose stack passes the variable to the server and the agent. If the collector runs on the host, the containers reach it at `http://host.docker.internal:4318` ([deployment.md](deployment.md)).

## A job's trace

```
POST /webhooks/github                         cisid-server
├── dispatch job                              cisid-server
│   ├── HTTP GET   (GitHub: .cisid.yml)       cisid-server
│   └── run job                               cisid-agent
│       ├── service postgres
│       ├── step clone
│       └── step test
└── report commit status   (on every status change)     cisid-server
    └── HTTP POST  (GitHub: statuses)
```

The webhook span ends as soon as the job enters the queue. The spans under it can arrive minutes later, and collector UIs still show them in the same tree.

| Span | Process | Attributes |
|---|---|---|
| `POST /webhooks/github` | server | `cisid.repo`, `cisid.branch`, `cisid.commit`, `cisid.job.id`, `github.delivery_id` |
| `GET /api/jobs/{id}` etc. | server | `http.route` and the standard HTTP attributes |
| `dispatch job` | server | `cisid.job.id`, `cisid.repo`, `cisid.commit`, `cisid.agent`, `cisid.matrix` |
| `HTTP GET`, `HTTP POST` (GitHub) | server | Standard HTTP client attributes |
| `run job` | agent | `cisid.job.id`, `cisid.agent`, `cisid.job.status` |
| `service <name>` | agent | `cisid.service.name`, `cisid.step.image` |
| `step <name>` | agent | `cisid.step.index`, `cisid.step.name`, `cisid.step.image`, `cisid.step.exit_code` |
| `report commit status` | server | `cisid.job.id`, `cisid.job.status` |

Failed steps, jobs that cannot be planned and GitHub errors set the span status to `Error`. The `run job` span of a failed job is `Error` too. Canceled jobs do not count as errors: their status shows in the `cisid.job.status` attribute.

## How the trace context is carried

A job stores the span of the request that queued it, in W3C `traceparent` format, in the `jobs.trace_parent` column (migration `010_job_trace.sql`). Even if the job is dispatched minutes later, maybe on another server replica, the `dispatch job` span uses this value as its parent. The server adds a `trace_parent` field to the `job_assigned` message it sends to the agent ([agent.md](agent.md#protocol)). This way the agent's spans join the same trace too.

- All jobs of a matrix share the first job's trace.
- A rerun job is linked to the trace of the `rerun` request.
- Webhook and API requests continue an incoming W3C `traceparent` header. Requests to GitHub carry no trace header.
- An agent that gets a job from a server with tracing off starts a new trace for each job.

## Correlating with logs

Log lines written inside a span carry the `trace_id` and `span_id` fields:

```
level=INFO msg="job queued" delivery=... job_id=42 ... trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7
```

## What is not traced

- Database queries and Docker API calls.
- Agent WebSocket connections, live log streams (SSE), the web UI, `/metrics` and health checks. These connections stay open for minutes or are called very often, so they add nothing to a trace.
- Step output and environment variables. Spans never contain secret values.

## Tests

| Test | What it verifies |
|---|---|
| `internal/telemetry` | Off when there is no address, bad configuration is rejected, export to a real OTLP/HTTP collector, `traceparent` propagation, trace IDs added to logs |
| `TestRouterTraces` | Which requests get a span, naming by route, an incoming `traceparent` is continued |
| `TestWebhookStoresRequestTrace` | The webhook writes the trace context to the job |
| `TestStoreKeepsTraceParent` | `trace_parent` in the queue, matrix and rerun |
| `TestHubTracesDispatch` | The dispatch span continues the job's trace, the planner sees the span, the `trace_parent` sent to the agent |
| `TestAgentTracesJob` | `run job`, service and step spans, the failed step is marked |
| `TestReporterTracesReports` | Commit status span |
| `TestRunEndToEnd` | Real server and agent: all spans reach the collector and form a single trace |

The `internal/telemetry/telemetrytest` package provides an in-process OTLP/HTTP collector for tests.
