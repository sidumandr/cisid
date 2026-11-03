# Yol Haritası

Her faz bir öncekinin üzerine kurulur. Her madde kendi testleriyle birlikte tamamlanmış sayılır (TDD, bkz. [CLAUDE.md](CLAUDE.md)).

Referanslar: Woodpecker CI (pipeline/runner), Drone CI (agent ↔ server), Tekton (step/workspace modeli).

## Faz 1 — MVP

Hedef: GitHub'a push → container'da komut çalışır → sonuç veritabanında görünür.

- [x] **Webhook handler** — `internal/api/webhook.go`: HMAC-SHA256 doğrulama, push payload'ından repo/branch/SHA, `jobs` tablosuna `pending` kayıt, `202 Accepted`.
- [x] **Server iskeleti** — `cmd/server`: config (env), pgx pool, migration'lar, HTTP router, graceful shutdown.
- [x] **Agent hub + job dağıtımı** — server'da `/agent/ws` endpoint'i, bekleyen job'ları agent'lara atama (`FOR UPDATE SKIP LOCKED`), `job_log` ve `job_result` mesajlarını kaydetme.
- [x] **Docker runner** — `internal/runner/docker.go` (`github.com/moby/moby/client`): image pull (progress callback), container create (env, workdir, mount), start, stdout/stderr `io.Reader`, exit code, timeout, context cancellation. `testify/mock` ile mock Docker client testleri.
- [x] **Pipeline tanımı** — repodaki `.cisid.yml` dosyasını GitHub'dan oku ve parse et (image + komut listesi), clone adımından sonra çalıştır.
- [x] **Agent (temel)** — `cmd/agent`: server'a WebSocket ile bağlan, `job_assigned` dinle, runner'ı çağır, sonucu raporla.

## Faz 2 — Streaming

Hedef: loglar canlı akar, kopan bağlantılar iş kaybettirmez.

- [x] Agent → server canlı log akışı (WebSocket, tek writer goroutine), logların veritabanına yazılması.
- [x] Server → istemci canlı log yayını ve log okuma API'si. WebSocket yerine SSE kullanıldı, çünkü akış tek yönlü ve SSE `Last-Event-ID` ile kaldığı yerden devam ediyor. PostgreSQL `LISTEN/NOTIFY` ile server'lar arası çalışıyor.
- [x] Agent reconnect: exponential backoff + jitter.
- [x] Job lease + heartbeat: hiç geri dönmeyen agent'ın işi lease dolunca tekrar kuyruğa döner, geri dönen agent işini geri alır.
- [x] Mesajların onaylı (ack) teslimi: kopan bağlantıda onaylanmamış mesajlar tekrar gönderilir, server tekrarlara karşı güvenlidir.
- [x] Mesaj protokolü tipleri `pkg/models` altında.

## Faz 3 — Features

- [x] Çok adımlı pipeline'lar, adımlar arası paylaşılan workspace volume.
- [x] Service container'ları (ör. test için PostgreSQL), job başına ayrı Docker ağında.
- [x] Secret yönetimi: şifreli saklama, env olarak enjekte, loglarda maskeleme ([docs/secrets.md](docs/secrets.md)).
- [x] Branch filtreleri (`when.branch`).
- [x] Matrix build: her kombinasyon ayrı, paralel koşabilen bir job ([docs/pipeline.md](docs/pipeline.md#matrix)).
- [x] GitHub commit status raporlama ([docs/commit-status.md](docs/commit-status.md)).
- [x] Job iptal / yeniden çalıştırma API'si (token korumalı).
- [x] Basit web arayüzü: job listesi ve canlı loglu job sayfası ([docs/ui.md](docs/ui.md)).

## Faz 4 — Production

- [ ] ~~Agent ↔ server iletişimini gRPC'ye taşı (`api/proto`).~~ Ertelendi, protokol WebSocket'te kalıyor: [ADR 001](docs/adr/001-agent-protokolu-websocket.md).
- [x] Agent kimlik doğrulama (ortak token) ve TLS (`CISID_TLS_*`, agent'ta `wss://` ve özel CA).
- [x] Gözlemlenebilirlik: `slog` yapılandırılmış log ve Prometheus metrikleri ([docs/metrics.md](docs/metrics.md)).
- [ ] OpenTelemetry trace.
- [x] Container izolasyonu ve kaynak limitleri: bellek, CPU, süreç sayısı, `no-new-privileges`, job başına ağ, OOM tespiti.
- [x] Rate limiting (IP başına token bucket) ve webhook replay koruması (imzalı gövdeye göre tekilleştirme ve 7 günden eski payload'ları reddetme).
- [x] Dağıtım: Docker image'ları ve docker compose stack'i ([docs/deployment.md](docs/deployment.md)).
- [ ] Helm chart.
- [x] cisid'in kendi CI'ını cisid üzerinde çalıştırmak (`.cisid.yml`).
