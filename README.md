# cisid

Go ile yazılmış, Docker tabanlı, self-hosted bir CI/CD sistemi.

> Durum: Faz 1 (MVP) geliştiriliyor. Ayrıntılar için [ROADMAP.md](ROADMAP.md).

## Dokümanlar

- [ROADMAP.md](ROADMAP.md): fazlar ve ilerleme
- [CLAUDE.md](CLAUDE.md): kod ve commit kuralları
- [docs/webhook.md](docs/webhook.md): GitHub webhook kurulumu ve davranışı
- [docs/runner.md](docs/runner.md): Docker runner kullanımı ve yaşam döngüsü
- [docs/server.md](docs/server.md): Server yapılandırması, endpoint'ler ve migration'lar
- [docs/agent.md](docs/agent.md): Agent yapılandırması, WebSocket protokolü ve yeniden bağlanma

## Mimari

```
   ┌──────────────┐        ┌──────────────┐
   │  Git Webhook │        │  CLI / Web   │
   └──────┬───────┘        └──────┬───────┘
          │        HTTP           │
          └───────────┬───────────┘
                      ▼
        ┌───────────────────────────┐
        │     cmd/server (API)      │
        │                           │
        │  internal/api   handlers  │
        │  internal/db    storage   │
        └──────┬─────────────▲──────┘
               │             │
       SQL     │             │  job_assigned ▼ / ▲ job_log, job_result
               ▼             │  (WebSocket, ileride gRPC: api/proto)
      ┌────────────────┐     │
      │   PostgreSQL   │     │
      │ pipelines,     │     │
      │ jobs, logs     │     │
      └────────────────┘     │
                             │
        ┌────────────────────┴──────┐
        │     cmd/agent (worker)    │   ×N
        │                           │
        │  internal/agent   ws, job │
        │  internal/runner  docker  │
        └─────────────┬─────────────┘
                      │ Docker API
                      ▼
        ┌───────────────────────────┐
        │     Docker Engine         │
        │  ┌───────┐ ┌───────┐      │
        │  │ step1 │ │ step2 │ ...  │
        │  └───────┘ └───────┘      │
        └───────────────────────────┘

        pkg/models: server ve agent arasında paylaşılan tipler
```

**Akış**

1. Bir webhook ya da kullanıcı isteği `server`'a ulaşır, pipeline ve job kayıtları PostgreSQL'e yazılır.
2. `agent` süreçleri bekleyen job'ları `server`'dan alır.
3. `runner`, her adımı ayrı bir Docker container'ında çalıştırır.
4. Loglar ve sonuçlar `server` üzerinden veritabanına geri yazılır.

## Dizin yapısı

```
.
├── cmd/
│   ├── server/      # API sunucusu (main)
│   └── agent/       # Worker binary (main)
├── internal/
│   ├── agent/       # Agent: WebSocket bağlantısı, job yürütme
│   ├── api/         # HTTP router, handler'lar, middleware
│   ├── config/      # Ortam değişkenlerinden server yapılandırması
│   │   └── testdata/    # Örnek GitHub webhook payload'ları
│   ├── runner/      # Docker entegrasyonu
│   └── db/          # PostgreSQL erişimi (pgx)
│       └── migrations/  # SQL şema dosyaları
├── pkg/
│   └── models/      # Paylaşılan tipler
├── api/
│   └── proto/       # gRPC tanımları (ileride)
└── docs/            # Modül dokümanları
```

## Gereksinimler

- Go 1.25+
- PostgreSQL 14+
- Docker (runner ve agent için)

## Yerelde çalıştırma

```bash
docker compose up -d --wait postgres
```

```bash
CISID_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' CISID_GITHUB_WEBHOOK_SECRET=dev-secret go run ./cmd/server
```

Agent hub (`/agent/ws`) henüz server'da yok. Agent şimdilik bağlanamaz ve backoff ile tekrar dener (bkz. [ROADMAP.md](ROADMAP.md)).

Ayrıntılar: [docs/server.md](docs/server.md), [docs/agent.md](docs/agent.md).

## Derleme ve test

```bash
go build ./...
```

```bash
go vet ./...
```

```bash
go test ./...
```

Race detector cgo gerektirir. Linux/macOS'ta doğrudan çalışır:

```bash
go test -race ./...
```

C derleyicisi olmayan Windows'ta Docker ile çalıştırılabilir (PowerShell, proje kökünden):

```bash
docker run --rm -v "${PWD}:/src" -v cisid-gomod:/go/pkg/mod -w /src golang:1.27 go test -race ./...
```

PostgreSQL gerektiren testler (`docker compose up -d postgres` sonrası):

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/db/ ./cmd/server/
```

Gerçek Docker'a karşı entegrasyon testleri (alpine image'ını çeker):

```bash
CISID_DOCKER_TESTS=1 go test ./internal/runner/ ./internal/agent/ -run Integration -v
```
