# Yol Haritası

[English](ROADMAP.md) · **Türkçe**

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
- [x] Secret yönetimi: şifreli saklama, env olarak enjekte, loglarda maskeleme ([docs/secrets.md](docs/tr/secrets.md)).
- [x] Branch filtreleri (`when.branch`).
- [x] Matrix build: her kombinasyon ayrı, paralel koşabilen bir job ([docs/pipeline.md](docs/tr/pipeline.md#matrix)).
- [x] GitHub commit status raporlama ([docs/commit-status.md](docs/tr/commit-status.md)).
- [x] Job iptal / yeniden çalıştırma API'si (token korumalı).
- [x] Basit web arayüzü: job listesi ve canlı loglu job sayfası ([docs/ui.md](docs/tr/ui.md)).

## Faz 4 — Production

- [ ] ~~Agent ↔ server iletişimini gRPC'ye taşı (`api/proto`).~~ Ertelendi, protokol WebSocket'te kalıyor: [ADR 001](docs/tr/adr/001-agent-protokolu-websocket.md).
- [x] Agent kimlik doğrulama (ortak token) ve TLS (`CISID_TLS_*`, agent'ta `wss://` ve özel CA).
- [x] Gözlemlenebilirlik: `slog` yapılandırılmış log ve Prometheus metrikleri ([docs/metrics.md](docs/tr/metrics.md)).
- [x] OpenTelemetry trace: webhook, dağıtım, GitHub istekleri, agent'ta servisler ve adımlar, commit status tek bir trace'te; OTLP/HTTP ile dışa aktarım ([docs/tracing.md](docs/tr/tracing.md)).
- [x] Container izolasyonu ve kaynak limitleri: bellek, CPU, süreç sayısı, `no-new-privileges`, job başına ağ, OOM tespiti.
- [x] Rate limiting (IP başına token bucket) ve webhook replay koruması (imzalı gövdeye göre tekilleştirme ve 7 günden eski payload'ları reddetme).
- [x] Dağıtım: Docker image'ları ve docker compose stack'i ([docs/deployment.md](docs/tr/deployment.md)).
- [x] Helm chart: server, Docker-in-Docker sidecar'lı agent'lar, isteğe bağlı PostgreSQL, TLS, ingress; testleri Helm SDK ile ([deployments/helm/cisid](deployments/helm/cisid/README.tr.md)).
- [x] cisid'in kendi CI'ını cisid üzerinde çalıştırmak (`.cisid.yml`).

## Faz 5 — Kullanıma hazırlık

- [x] MIT lisansı.
- [x] Log saklama süresi: biten job'ların logları `CISID_LOG_RETENTION` sonunda silinir ([docs/server.md](docs/tr/server.md)).
- [x] Pull request build'leri: PR'a yeni commit gelince eski build'ler iptal edilir; fork'lar isteğe bağlıdır ve secret almaz ([docs/webhook.md](docs/tr/webhook.md#pull-requestler)).
- [x] Secret'ları server tarafında branch'lere sınırlama; sınırlı secret'lar pull request'lere verilmez ([docs/secrets.md](docs/tr/secrets.md)).
- [x] Okuma erişimi için token (`CISID_READ_TOKEN`, arayüz için oturum cookie'si) ve agent token'ı rotasyonu ([docs/api.md](docs/tr/api.md#okuma-erişimi)).
- [x] Job'lar arası önbellek: `.cisid.yml`'de `cache`, repo başına Docker volume'ları; fork PR'larına salt okunur ([docs/pipeline.md](docs/tr/pipeline.md#önbellek)).
- [x] Elle ve zamanlanmış build'ler: API ve arayüzden branch build'i, veritabanında cron zamanlamaları; birden fazla server kopyası aynı çalışmayı iki kez başlatmaz ([docs/api.md](docs/tr/api.md#elle-ve-zamanlanmış-buildler)).
- [x] Birden fazla server kopyasının uçtan uca testi: aynı veritabanında iki server, kopyalar arası dağıtım, canlı log ve iptal (`TestRunTwoServers`).
- [x] Sürüm yayımlama: `v*` etiketinde GitHub Actions testleri çalıştırır, image'ları (amd64, arm64) ve chart'ı ghcr.io'ya gönderir, release oluşturur ([docs/release.md](docs/tr/release.md)).

## Faz 6 — Herkes için

- [x] İlk sürüm: v0.1.0, image'lar ve Helm chart ghcr.io'da ([docs/release.md](docs/tr/release.md)).
- [x] Birden fazla git sağlayıcısı: GitHub, Gitea, Forgejo, GitLab ve Bitbucket Cloud; webhook'lar, commit status, özel repoları clone'lama ([docs/forges.md](docs/tr/forges.md)).
- [x] Webhook'suz push build'leri: branch'leri yoklama; birden fazla server kopyası aynı commit'i iki kez derlemez ([docs/polling.md](docs/tr/polling.md)).
- [x] Kullanıcı hesapları ve roller (viewer, maintainer, admin), şifreyle giriş, kişisel API token'ları, CSRF koruması ([docs/accounts.md](docs/tr/accounts.md)).
- [x] Sağlayıcılarla (OAuth) giriş: GitHub, Gitea, Forgejo, GitLab, Bitbucket; hesaplar sağlayıcının değişmeyen kullanıcı numarasıyla eşleşir, isteğe bağlı otomatik hesap açma ([docs/accounts.md](docs/tr/accounts.md#sağlayıcıyla-giriş)).
- [x] Dokümanlar İngilizce ve Türkçe: İngilizceler her zamanki yerlerinde, Türkçeler `docs/tr/` altında ve `.tr.md` dosyalarında.

## Faz 7: Build'lerden haberdar olmak

- [x] Bildirimler: Slack uyumlu sohbetler, imzalı webhook'lar ve e-posta; her kırılmada, kırılma ve düzelmede ya da her sonuçta, branch'e göre ve isteğe bağlı olarak pull request'ler için; birden fazla server kopyasında da her sonuç bir kez gönderilir ([docs/notifications.md](docs/tr/notifications.md)).
- [x] README'ler için build rozetleri; okumanın giriş istediği server'lar için `CISID_PUBLIC_BADGES` ([docs/notifications.md](docs/tr/notifications.md#rozetler)).
