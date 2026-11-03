# Metrikler

[English](../metrics.md) · **Türkçe**

Server, Prometheus metriklerini `GET /metrics` adresinde sunar (`internal/metrics`). Etiketlerde repo adı ya da job numarası gibi sınırsız değerler yer almaz.

| Metrik | Tür | Etiketler | Açıklama |
|---|---|---|---|
| `cisid_http_requests_total` | counter | `route`, `method`, `code` | HTTP istekleri. `route` eşleşen desendir (ör. `GET /api/jobs/{id}`). |
| `cisid_http_request_duration_seconds` | histogram | `route` | İstek süresi. Uzun süre açık kalan agent ve log akışı bağlantıları hariçtir. |
| `cisid_jobs` | gauge | `status` | Veritabanındaki job sayıları, her scrape'te okunur |
| `cisid_jobs_finished_total` | counter | `status` | Bu server'da biten job'lar |
| `cisid_agents_connected` | gauge | — | Bu server'a bağlı agent'lar |
| `cisid_log_chunks_total` | counter | — | Agent'lardan gelen log parçaları |
| `cisid_webhook_deliveries_total` | counter | `result` | `queued`, `ignored`, `duplicate`, `canceled` (kapanan PR), `invalid_signature`, `invalid_payload`, `error` |
| `cisid_commit_statuses_total` | counter | `result` | `sent`, `retry` (sağlayıcı erişilemedi), `failed`, `skipped` (sağlayıcının token'ı yok) |
| `go_*`, `process_*` | | | Go runtime ve süreç metrikleri |

Veritabanına ulaşılamazsa `cisid_jobs` o scrape'te atlanır. Diğer metrikler sunulmaya devam eder.

## Örnek uyarılar

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

## Loglar

Server ve agent yapılandırılmış log yazar (`log/slog`, `key=value` biçiminde). Seviye `CISID_LOG_LEVEL` ile ayarlanır. Secret değerleri, token'lar ve veritabanı şifresi loglara yazılmaz. Trace açıksa bir span içinde yazılan satırlar `trace_id` ve `span_id` taşır ([tracing.md](tracing.md)).

> `CISID_READ_TOKEN` tanımlıysa `/metrics` de bu token'ı ister ([api.md](api.md#okuma-erişimi)). Prometheus'ta:
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
> Tanımlı değilse `/metrics` herkese açıktır; server'ı güvenilmeyen bir ağa açıyorsanız bu yolu reverse proxy'de kısıtlayın.
