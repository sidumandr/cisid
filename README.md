# cisid

Go ile yazılmış, Docker tabanlı, self-hosted bir CI/CD sistemi.

> Durum: Faz 1, 2 ve 3 tamam, Faz 4 (production) geliştiriliyor. Ayrıntılar için [ROADMAP.md](ROADMAP.md).

## Dokümanlar

- [ROADMAP.md](ROADMAP.md): fazlar ve ilerleme
- [CLAUDE.md](CLAUDE.md): kod ve commit kuralları
- [docs/pipeline.md](docs/pipeline.md): `.cisid.yml` formatı ve adımların çalışma şekli
- [docs/ui.md](docs/ui.md): Web arayüzü
- [docs/api.md](docs/api.md): Job API ve canlı log akışı (SSE)
- [docs/secrets.md](docs/secrets.md): Şifreli repo secret'ları ve loglarda maskeleme
- [docs/metrics.md](docs/metrics.md): Prometheus metrikleri
- [docs/commit-status.md](docs/commit-status.md): Job sonuçlarının GitHub'da commit status olarak gösterilmesi
- [docs/webhook.md](docs/webhook.md): GitHub webhook kurulumu ve davranışı
- [docs/runner.md](docs/runner.md): Docker runner kullanımı ve yaşam döngüsü
- [docs/server.md](docs/server.md): Server yapılandırması, endpoint'ler ve migration'lar
- [docs/agent.md](docs/agent.md): Agent yapılandırması, WebSocket protokolü ve yeniden bağlanma
- [docs/hub.md](docs/hub.md): Server tarafında job dağıtımı, sahiplik kuralları ve graceful shutdown
- [docs/deployment.md](docs/deployment.md): Docker image'ları ve docker compose ile kurulum

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
        │  internal/hub   dispatch  │
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

1. GitHub push webhook'u `server`'a ulaşır, job PostgreSQL'e `pending` olarak yazılır.
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
│   │   └── testdata/    # Örnek GitHub webhook payload'ları
│   ├── config/      # Ortam değişkenlerinden server yapılandırması
│   ├── events/      # PostgreSQL LISTEN/NOTIFY ile job değişikliği bildirimleri
│   ├── hub/         # Agent bağlantıları ve job dağıtımı (server tarafı)
│   ├── metrics/     # Prometheus metrikleri
│   ├── github/      # GitHub API istemcisi (.cisid.yml okuma)
│   ├── pipeline/    # .cisid.yml ayrıştırma, job → adımlar
│   ├── secrets/     # AES-256-GCM ile şifreli secret'lar
│   ├── status/      # GitHub commit status raporlama
│   ├── web/         # Gömülü web arayüzü
│   ├── runner/      # Docker entegrasyonu
│   └── db/          # PostgreSQL erişimi (pgx)
│       ├── dbtest/      # Testler için geçici veritabanları
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

```bash
go run ./cmd/agent
```

Agent `ws://localhost:8080/agent/ws` adresine bağlanır. Bir push geldiğinde job'u alır, repoyu clone'lar ve [`.cisid.yml`](docs/pipeline.md) adımlarını Docker'da çalıştırır.

Ayrıntılar: [docs/server.md](docs/server.md), [docs/agent.md](docs/agent.md).

## Docker ile çalıştırma

PostgreSQL, server ve bir agent'tan oluşan stack'i kaynak koddan derleyip başlatır (`.env` içinde en az `CISID_GITHUB_WEBHOOK_SECRET` ve `CISID_AGENT_TOKEN` gerekir):

```bash
docker compose --profile stack up -d --build --wait
```

Ayrıntılar: [docs/deployment.md](docs/deployment.md).

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
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/db/ ./internal/hub/ ./cmd/server/
```

Gerçek Docker'a karşı entegrasyon testleri (alpine image'ını çeker):

```bash
CISID_DOCKER_TESTS=1 go test ./internal/runner/ ./internal/agent/ ./internal/pipeline/ -run Integration -v
```
