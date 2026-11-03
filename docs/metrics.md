# Metrics

**English** · [Türkçe](tr/metrics.md)

The server serves Prometheus metrics at `GET /metrics` (`internal/metrics`). Labels never contain unbounded values such as repository names or job numbers.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `cisid_http_requests_total` | counter | `route`, `method`, `code` | HTTP requests. `route` is the matched pattern (e.g. `GET /api/jobs/{id}`). |
| `cisid_http_request_duration_seconds` | histogram | `route` | Request duration. Long-lived agent and log stream connections are excluded. |
| `cisid_jobs` | gauge | `status` | Job counts in the database, read on every scrape |
| `cisid_jobs_finished_total` | counter | `status` | Jobs finished on this server |
| `cisid_agents_connected` | gauge | — | Agents connected to this server |
| `cisid_log_chunks_total` | counter | — | Log chunks received from agents |
| `cisid_webhook_deliveries_total` | counter | `result` | `queued`, `ignored`, `duplicate`, `canceled` (closed pull request), `invalid_signature`, `invalid_payload`, `error` |
| `cisid_commit_statuses_total` | counter | `result` | `sent`, `retry` (forge unreachable), `failed`, `skipped` (no token for the forge) |
| `go_*`, `process_*` | | | Go runtime and process metrics |

If the database is unreachable, `cisid_jobs` is skipped in that scrape. The other metrics are still served.

## Example alerts

```yaml
- alert: CisidQueueBacklog
  expr: cisid_jobs{status="pending"} > 20
  for: 10m
- alert: CisidNoAgents
  expr: sum(cisid_agents_connected) == 0
  for: 5m
- alert: CisidCommitStatusesFailing
  expr: rate(cisid_commit_statuses_total{result="failed"}[15m]) > 0
```

## Logs

The server and the agent write structured logs (`log/slog`, in `key=value` format). Set the level with `CISID_LOG_LEVEL`. Secret values, tokens and the database password are never written to the logs. When tracing is on, lines written inside a span carry `trace_id` and `span_id` ([tracing.md](tracing.md)).

> If `CISID_READ_TOKEN` is set, `/metrics` also requires this token ([api.md](api.md#read-access)). In Prometheus:
>
> ```yaml
> scrape_configs:
>   - job_name: cisid
>     authorization:
>       credentials_file: /etc/prometheus/cisid-read-token
>     static_configs:
>       - targets: ["cisid:8080"]
> ```
>
> If it is not set, `/metrics` is public. If you expose the server to an untrusted network, restrict this path in the reverse proxy.
