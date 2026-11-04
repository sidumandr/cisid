# Server

[English](../server.md) · **Türkçe**

`cmd/server` API sunucusunu başlatır. Sunucu yapılandırmayı ortam değişkenlerinden okur, PostgreSQL'e bağlanır, migration'ları uygular ve HTTP isteklerini karşılar. `SIGINT` / `SIGTERM` gelince graceful shutdown yapar.

## Hızlı başlangıç

```bash
docker compose up -d --wait postgres
```

```bash
CISID_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' CISID_GITHUB_WEBHOOK_SECRET=dev-secret go run ./cmd/server
```

```bash
curl http://localhost:8080/readyz
```

PostgreSQL, server ve agent'ı birlikte container olarak çalıştırmak için [deployment.md](deployment.md).

## Yapılandırma (`internal/config`)

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_DATABASE_URL` | **zorunlu** | `postgres://` bağlantı adresi |
| `CISID_GITHUB_WEBHOOK_SECRET` | — | GitHub webhook secret'ı; `/webhooks/github`'ı açar ([webhook.md](webhook.md)). Webhook'suz bir server elle ve zamanlanmış build'ler çalıştırır. |
| `CISID_HTTP_ADDR` | `:8080` | Dinlenecek adres |
| `CISID_SHUTDOWN_TIMEOUT` | `15s` | Kapanışta devam eden istekler için beklenecek süre |
| `CISID_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CISID_GITHUB_TOKEN` | — | GitHub token'ı: özel repolar, rate limit ve commit status için ([forges.md](forges.md#github)) |
| `CISID_GITHUB_API_URL` | `https://api.github.com` | GitHub Enterprise Server için API adresi |
| `CISID_GITEA_URL`, `CISID_GITEA_TOKEN`, `CISID_GITEA_WEBHOOK_SECRET` | — | Gitea sunucusu; adres verilince açılır ([forges.md](forges.md#gitea-ve-forgejo)) |
| `CISID_FORGEJO_URL`, `CISID_FORGEJO_TOKEN`, `CISID_FORGEJO_WEBHOOK_SECRET` | — | Forgejo sunucusu; adres verilince açılır |
| `CISID_GITLAB_URL`, `CISID_GITLAB_TOKEN`, `CISID_GITLAB_WEBHOOK_SECRET` | `https://gitlab.com` | GitLab ([forges.md](forges.md#gitlab)) |
| `CISID_BITBUCKET_TOKEN`, `CISID_BITBUCKET_WEBHOOK_SECRET` | — | Bitbucket Cloud ([forges.md](forges.md#bitbucket-cloud)) |
| `CISID_AGENT_TOKEN` | — | Agent'ların bağlanırken göstermesi gereken token, en az 16 karakter. Virgülle ayrılmış birden fazla token verilebilir; hepsi kabul edilir, böylece token'lar agent'lar kesilmeden değiştirilir ([agent.md](agent.md#kimlik-doğrulama-ve-tls)). Tanımlı değilse her istemci agent olarak bağlanabilir ve server bunu açılışta uyarı olarak loglar. |
| `CISID_READ_TOKEN` | — | Okuyucu (viewer) token'ı, en az 16 karakter. Tanımlıysa okuma varsayılan olarak giriş ister ([api.md](api.md#okuma-erişimi)). |
| `CISID_TLS_CERT_FILE`, `CISID_TLS_KEY_FILE` | — | İkisi birlikte verilirse server HTTPS üzerinden servis eder (en az TLS 1.2). Agent'lar `wss://` ile bağlanır. |
| `CISID_RATE_LIMIT` | `10` | Webhook ve `/api` için istemci IP'si başına saniyedeki istek sayısı (ani yük için 2 katı kadar izin verilir). `0` limiti kapatır. Aşılırsa `429` döner. |
| `CISID_TRUST_PROXY` | `false` | `true` ise istemci IP'si `X-Forwarded-For`'daki ilk adresten alınır. Sadece bu başlığı kendisi yazan bir reverse proxy arkasında açın, yoksa başlık sahte olabilir. |
| `CISID_API_TOKEN` | — | Server'ın admin token'ı: her şeyi yapar, ilk kullanıcıları da oluşturabilir; en az 16 karakter ([accounts.md](accounts.md)) |
| `CISID_READ_ACCESS` | read token varsa `signed-in`, yoksa `public` | `public`: herkes okur; `signed-in`: okumak giriş ya da token ister ([accounts.md](accounts.md#okuma-erişimi)) |
| `CISID_ADMIN_USER`, `CISID_ADMIN_PASSWORD` | `admin`, — | Şifre verilirse server açılışta bu admin'i oluşturur (varsa dokunmaz); şifre en az 10 karakter |
| `CISID_SESSION_TTL` | `7d` | Bir girişin süresi (`12h`, `30d` gibi) |
| `CISID_<SAĞLAYICI>_OAUTH_CLIENT_ID`, `..._SECRET` | — | Sağlayıcıyla giriş: `GITHUB`, `GITEA`, `FORGEJO`, `GITLAB`, `BITBUCKET`; `CISID_PUBLIC_URL` gerekir ([accounts.md](accounts.md#sağlayıcıyla-giriş)) |
| `CISID_OAUTH_SIGNUP_ROLE` | — | `viewer` ya da `maintainer`: hesabı olmayan sağlayıcı kullanıcılarına hesap açılır |
| `CISID_SIGNUP_ROLE` | yok | `viewer` ya da `maintainer`: herkes şifreyle hesap açabilir. Tanımlı değilse kayıtla yalnızca hiç kullanıcısı olmayan server'ın ilk hesabı (admin) açılır ([accounts.md](accounts.md#kaydolmak)) |
| `CISID_SECRET_KEY` | — | Secret'ları şifreleyen 32 byte'lık anahtar, base64 ([secrets.md](secrets.md)) |
| `CISID_PUBLIC_URL` | — | Kullanıcıların server'a eriştiği adres. Commit status bağlantılarında kullanılır ([commit-status.md](commit-status.md)). |
| `CISID_BUILD_FORKS` | `false` | `true` ise fork'lardan gelen pull request'ler de derlenir; bu job'lara secret verilmez. `approve` ise her birini bir maintainer onaylayınca derler ([webhook.md](webhook.md#pull-requestler)) |
| `CISID_POLL_INTERVAL` | `1m` | İzlenen branch'lerin yoklanma aralığı, en az `10s`; `0` kapatır ([polling.md](polling.md)) |
| `CISID_SMTP_HOST`, `CISID_SMTP_PORT`, `CISID_SMTP_TLS`, `CISID_SMTP_USERNAME`, `CISID_SMTP_PASSWORD`, `CISID_SMTP_FROM` | yok, `587`, `starttls` | E-posta bildirimlerinin gittiği SMTP sunucusu; host yoksa e-posta kapalıdır ([notifications.md](notifications.md#e-posta)) |
| `CISID_ARTIFACT_MAX_SIZE` | `100MB` | Kabul edilen en büyük artifact; byte olarak ya da `KB`, `MB`, `GB` ile (1024'ün kuvvetleri), en fazla `10GB` ([pipeline.md](pipeline.md#artifactlar)) |
| `CISID_PUBLIC_BADGES` | `false` | `true` ise okumak giriş istese de build rozetlerini herkes görür ([notifications.md](notifications.md#rozetler)) |
| `CISID_LOG_RETENTION` | `90d` | Biten job'ların loglarının saklanma süresi (`30d`, `72h` gibi). `0` logları sonsuza dek saklar. Süresi dolan loglar saatlik temizlikte silinir, job kaydı kalır. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` vb. | — | Trace'lerin gönderileceği OTLP/HTTP collector. Verilmezse trace kapalıdır ([tracing.md](tracing.md)). |

Eksik ya da hatalı bütün değerler tek bir hata mesajında raporlanır. Yapılandırma loglanırken secret ve veritabanı şifresi maskelenir (`slog.LogValuer`).

## Endpoint'ler (`internal/api/router.go`)

| Yol | Açıklama |
|---|---|
| `POST /webhooks/github` | GitHub webhook'ları ([webhook.md](webhook.md)) |
| `POST /webhooks/{gitea,forgejo,gitlab,bitbucket}` | Diğer sağlayıcıların webhook'ları ([forges.md](forges.md)); secret'ı verilmeyen sağlayıcınınki `404` döner |
| `GET /agent/ws` | Agent WebSocket bağlantıları ([hub.md](hub.md)) |
| `PUT /agent/jobs/{id}/artifacts/{name}` | Agent'lardan artifact yüklemeleri, agent token'ıyla ([agent.md](agent.md#job-yürütme)) |
| `GET /api/jobs/...` | Job'lar, loglar ve canlı log akışı ([api.md](api.md)) |
| `/api/repos/{owner}/{repo}/secrets` | Repo secret'ları ([secrets.md](secrets.md)) |
| `/api/repos/{owner}/{repo}/builds`, `…/schedules` | Elle ve zamanlanmış build'ler ([api.md](api.md#elle-ve-zamanlanmış-buildler)) |
| `GET /`, `GET /jobs/{id}` | Web arayüzü ([ui.md](ui.md)) |
| `GET /metrics` | Prometheus metrikleri ([metrics.md](metrics.md)) |
| `GET /auth/{sağlayıcı}/login`, `/callback` | Sağlayıcıyla giriş ([accounts.md](accounts.md#sağlayıcıyla-giriş)) |
| `GET /healthz` | Liveness: süreç ayakta ise `200 {"status":"ok"}` |
| `GET /readyz` | Readiness: veritabanı 2 sn içinde yanıt verirse `200`, vermezse `503 {"status":"unavailable","reason":"database unreachable"}` |

Bütün istekler iki middleware'den geçer:

- **Loglama:** Her istek için bir log satırı yazılır: method, path, status, süre ve uzak adres. Wrapper `Hijack` ve `Flush` çağrılarını alttaki writer'a iletir, böylece WebSocket upgrade'leri çalışmaya devam eder.
- **Panic kurtarma:** Bir handler panic ederse stack trace loglanır ve istemciye `500` döner. Sunucu çalışmaya devam eder.

## Migration'lar (`internal/db/migrate.go`)

- SQL dosyaları `internal/db/migrations/` altındadır ve binary'ye gömülüdür (`embed`). Ad formatı: `<sürüm>_<ad>.sql`, örneğin `002_job_execution.sql`.
- Sunucu her açılışta henüz uygulanmamış migration'ları sürüm sırasıyla uygular. Uygulananlar `schema_migrations` tablosuna yazılır.
- Her migration kendi transaction'ında çalışır. Hata olursa o migration tamamen geri alınır ve sunucu açılmaz.
- Aynı veritabanını paylaşan birden fazla sunucu aynı anda açılırsa PostgreSQL advisory lock ile sıraya girerler. Her migration tam bir kez uygulanır.

## Açılış ve kapanış

```
config.Load ─► pgxpool + Ping (10 sn) ─► Migrate ─► net.Listen ─► Serve
                                                                   │
                                     SIGTERM ──────────────────────┘
                                        │
                     Shutdown: yeni bağlantılar reddedilir,
                     devam eden istekler CISID_SHUTDOWN_TIMEOUT kadar beklenir
```

Süre dolarsa kalan bağlantılar zorla kapatılır ve süreç hata koduyla çıkar.

## Testler

```bash
go test ./cmd/server/ ./internal/api/ ./internal/config/
```

Veritabanı gerektiren testler (migration'lar, store, uçtan uca sunucu testi):

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/db/ ./cmd/server/
```

`internal/db` testlerinin her biri geçici bir veritabanı oluşturur (`cisid_test_*`) ve test bitince siler. `TestRunEndToEnd` gerçek sunucuyu başlatır, readiness'i bekler, imzalı bir webhook gönderir, job'un veritabanına yazıldığını kontrol eder ve sunucuyu kapatır.
