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

## Yapılandırma (`internal/config`)

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_DATABASE_URL` | **zorunlu** | `postgres://` bağlantı adresi |
| `CISID_GITHUB_WEBHOOK_SECRET` | **zorunlu** | GitHub webhook secret'ı ([webhook.md](webhook.md)) |
| `CISID_HTTP_ADDR` | `:8080` | Dinlenecek adres |
| `CISID_SHUTDOWN_TIMEOUT` | `15s` | Kapanışta devam eden istekler için beklenecek süre |
| `CISID_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Eksik ya da hatalı bütün değerler tek bir hata mesajında raporlanır. Yapılandırma loglanırken secret ve veritabanı şifresi maskelenir (`slog.LogValuer`).

## Endpoint'ler (`internal/api/router.go`)

| Yol | Açıklama |
|---|---|
| `POST /webhooks/github` | GitHub webhook'ları ([webhook.md](webhook.md)) |
| `GET /healthz` | Liveness: süreç ayakta ise `200 {"status":"ok"}` |
| `GET /readyz` | Readiness: veritabanı 2 sn içinde yanıt verirse `200`, vermezse `503 {"status":"unavailable","reason":"database unreachable"}` |

Bütün istekler iki middleware'den geçer:

- **Loglama:** Her istek için bir log satırı yazılır: method, path, status, süre ve uzak adres. Wrapper `Hijack` ve `Flush` çağrılarını alttaki writer'a iletir, böylece WebSocket upgrade'leri çalışmaya devam eder.
- **Panic kurtarma:** Bir handler panic ederse stack trace loglanır ve istemciye `500` döner. Sunucu çalışmaya devam eder.

## Migration'lar (`internal/db/migrate.go`)

- SQL dosyaları `internal/db/migrations/` altındadır ve binary'ye gömülüdür (`embed`). Ad formatı: `<sürüm>_<ad>.sql`, örneğin `002_create_logs.sql`.
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
