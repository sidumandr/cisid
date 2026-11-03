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
- [ ] Server → istemci log yayını (WebSocket) ve log okuma API'si.
- [x] Agent reconnect: exponential backoff + jitter.
- [x] Job lease + heartbeat: hiç geri dönmeyen agent'ın işi lease dolunca tekrar kuyruğa döner, geri dönen agent işini geri alır.
- [ ] Mesajların onaylı (ack) teslimi. Planlı server kapanışında kayıp `server_closing` ile önlendi, ani ağ kopmaları için ack gerekiyor.
- [x] Mesaj protokolü tipleri `pkg/models` altında.

## Faz 3 — Features

- [x] Çok adımlı pipeline'lar, adımlar arası paylaşılan workspace volume.
- [ ] Service container'ları (ör. test için PostgreSQL).
- [ ] Secret yönetimi (şifreli saklama, env olarak enjekte, loglarda maskeleme).
- [ ] Branch / event filtreleri, matrix build.
- [ ] GitHub commit status raporlama.
- [ ] Job iptal / yeniden çalıştırma API'si, basit web arayüzü veya CLI.

## Faz 4 — Production

- [ ] Agent ↔ server iletişimini gRPC'ye taşı (`api/proto`).
- [ ] Agent kimlik doğrulama (token), TLS.
- [ ] Gözlemlenebilirlik: `slog` yapılandırılmış log, Prometheus metrikleri, OpenTelemetry trace.
- [ ] Container izolasyonu ve kaynak limitleri (CPU, bellek, ağ).
- [ ] Rate limiting, webhook replay koruması (`X-GitHub-Delivery`).
- [ ] Dağıtım: Docker image'ları, docker-compose, Helm chart.
- [x] cisid'in kendi CI'ını cisid üzerinde çalıştırmak (`.cisid.yml`).
