# Server

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
| `CISID_GITHUB_WEBHOOK_SECRET` | **zorunlu** | GitHub webhook secret'ı ([webhook.md](webhook.md)) |
| `CISID_HTTP_ADDR` | `:8080` | Dinlenecek adres |
| `CISID_SHUTDOWN_TIMEOUT` | `15s` | Kapanışta devam eden istekler için beklenecek süre |
| `CISID_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CISID_GITHUB_TOKEN` | — | `.cisid.yml` okumak için GitHub token'ı. Özel repolar ve rate limit için gerekir ([pipeline.md](pipeline.md)). |
| `CISID_GITHUB_API_URL` | `https://api.github.com` | GitHub Enterprise Server için API adresi |
| `CISID_AGENT_TOKEN` | — | Agent'ların bağlanırken göstermesi gereken token, en az 16 karakter. Virgülle ayrılmış birden fazla token verilebilir; hepsi kabul edilir, böylece token'lar agent'lar kesilmeden değiştirilir ([agent.md](agent.md#kimlik-doğrulama-ve-tls)). Tanımlı değilse her istemci agent olarak bağlanabilir ve server bunu açılışta uyarı olarak loglar. |
| `CISID_READ_TOKEN` | — | Tanımlıysa job'ları, logları ve `/metrics`'i okumak bu token'ı ya da API token'ını ister, en az 16 karakter ([api.md](api.md#okuma-erişimi)). Tanımlı değilse server'a ulaşan herkes okur. |
| `CISID_TLS_CERT_FILE`, `CISID_TLS_KEY_FILE` | — | İkisi birlikte verilirse server HTTPS üzerinden servis eder (en az TLS 1.2). Agent'lar `wss://` ile bağlanır. |
| `CISID_RATE_LIMIT` | `10` | Webhook ve `/api` için istemci IP'si başına saniyedeki istek sayısı (ani yük için 2 katı kadar izin verilir). `0` limiti kapatır. Aşılırsa `429` döner. |
| `CISID_TRUST_PROXY` | `false` | `true` ise istemci IP'si `X-Forwarded-For`'daki ilk adresten alınır. Sadece bu başlığı kendisi yazan bir reverse proxy arkasında açın, yoksa başlık sahte olabilir. |
| `CISID_API_TOKEN` | — | İptal ve yeniden çalıştırma uç noktalarını açar, en az 16 karakter ([api.md](api.md#iptal-ve-yeniden-çalıştırma)) |
| `CISID_SECRET_KEY` | — | Secret'ları şifreleyen 32 byte'lık anahtar, base64 ([secrets.md](secrets.md)) |
| `CISID_PUBLIC_URL` | — | Kullanıcıların server'a eriştiği adres. Commit status bağlantılarında kullanılır ([commit-status.md](commit-status.md)). |
| `CISID_BUILD_FORKS` | `false` | `true` ise fork'lardan gelen pull request'ler de derlenir; bu job'lara secret verilmez ([webhook.md](webhook.md#pull-requestler)) |
| `CISID_LOG_RETENTION` | `90d` | Biten job'ların loglarının saklanma süresi (`30d`, `72h` gibi). `0` logları sonsuza dek saklar. Süresi dolan loglar saatlik temizlikte silinir, job kaydı kalır. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` vb. | — | Trace'lerin gönderileceği OTLP/HTTP collector. Verilmezse trace kapalıdır ([tracing.md](tracing.md)). |

Eksik ya da hatalı bütün değerler tek bir hata mesajında raporlanır. Yapılandırma loglanırken secret ve veritabanı şifresi maskelenir (`slog.LogValuer`).

## Endpoint'ler (`internal/api/router.go`)

| Yol | Açıklama |
|---|---|
| `POST /webhooks/github` | GitHub webhook'ları ([webhook.md](webhook.md)) |
| `GET /agent/ws` | Agent WebSocket bağlantıları ([hub.md](hub.md)) |
| `GET /api/jobs/...` | Job'lar, loglar ve canlı log akışı ([api.md](api.md)) |
| `/api/repos/{owner}/{repo}/secrets` | Repo secret'ları ([secrets.md](secrets.md)) |
| `GET /`, `GET /jobs/{id}` | Web arayüzü ([ui.md](ui.md)) |
| `GET /metrics` | Prometheus metrikleri ([metrics.md](metrics.md)) |
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
