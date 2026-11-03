# cisid

Go ile yazılmış, Docker tabanlı, self-hosted bir CI/CD sistemi.

> Durum: Faz 1–5 tamam, Faz 6 sürüyor. Agent protokolünün gRPC'ye taşınması ertelendi ([ADR 001](docs/adr/001-agent-protokolu-websocket.md)). Ayrıntılar için [ROADMAP.md](ROADMAP.md).

## Dokümanlar

- [ROADMAP.md](ROADMAP.md): fazlar ve ilerleme
- [CLAUDE.md](CLAUDE.md): kod ve commit kuralları
- [docs/pipeline.md](docs/pipeline.md): `.cisid.yml` formatı ve adımların çalışma şekli
- [docs/ui.md](docs/ui.md): Web arayüzü
- [docs/api.md](docs/api.md): Job API ve canlı log akışı (SSE)
- [docs/secrets.md](docs/secrets.md): Şifreli repo secret'ları ve loglarda maskeleme
- [docs/metrics.md](docs/metrics.md): Prometheus metrikleri
- [docs/tracing.md](docs/tracing.md): OpenTelemetry trace'leri: webhook'tan adımlara tek trace
- [docs/polling.md](docs/polling.md): Webhook'suz push build'leri: branch'leri yoklama
- [docs/forges.md](docs/forges.md): GitHub, Gitea, Forgejo, GitLab ve Bitbucket kurulumu, repo adları ve özel repolar
- [docs/commit-status.md](docs/commit-status.md): Job sonuçlarının sağlayıcılarda commit status olarak gösterilmesi
- [docs/webhook.md](docs/webhook.md): Webhook kurulumu ve davranışı
- [docs/runner.md](docs/runner.md): Docker runner kullanımı ve yaşam döngüsü
- [docs/server.md](docs/server.md): Server yapılandırması, endpoint'ler ve migration'lar
- [docs/agent.md](docs/agent.md): Agent yapılandırması, WebSocket protokolü ve yeniden bağlanma
- [docs/hub.md](docs/hub.md): Server tarafında job dağıtımı, sahiplik kuralları ve graceful shutdown
- [docs/deployment.md](docs/deployment.md): Docker image'ları, docker compose ve Kubernetes ile kurulum
- [docs/release.md](docs/release.md): Sürüm yayımlama: image'lar ve chart GitHub Actions ile ghcr.io'ya
- [deployments/helm/cisid](deployments/helm/cisid/README.md): Helm chart
- [docs/security.md](docs/security.md): Güven sınırları, güvenlik önlemleri ve production kontrol listesi
- [docs/adr/](docs/adr/): Mimari karar kayıtları (ör. agent protokolü neden gRPC değil)

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
               ▼             │  (WebSocket, bkz. docs/adr/001)
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

1. Sağlayıcının (GitHub, Gitea, Forgejo, GitLab, Bitbucket) push webhook'u `server`'a ulaşır, job PostgreSQL'e `pending` olarak yazılır.
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
│   ├── forge/       # Sağlayıcılardan bağımsız arayüz, repo anahtarları
│   ├── github/      # GitHub API istemcisi
│   ├── gitea/       # Gitea ve Forgejo API istemcisi
│   ├── gitlab/      # GitLab API istemcisi
│   ├── bitbucket/   # Bitbucket Cloud API istemcisi
│   ├── pipeline/    # .cisid.yml ayrıştırma, job → adımlar
│   ├── secrets/     # AES-256-GCM ile şifreli secret'lar
│   ├── status/      # Commit status raporlama
│   ├── telemetry/   # OpenTelemetry trace kurulumu ve trace bağlamı taşıma
│   ├── trigger/     # Elle, zamanlanmış ve yoklanan (polling) build'ler
│   ├── web/         # Gömülü web arayüzü
│   ├── runner/      # Docker entegrasyonu
│   └── db/          # PostgreSQL erişimi (pgx)
│       ├── dbtest/      # Testler için geçici veritabanları
│       └── migrations/  # SQL şema dosyaları
├── pkg/
│   └── models/      # Paylaşılan tipler
├── api/
│   └── proto/       # gRPC tanımları (ileride)
├── deployments/
│   └── helm/
│       ├── cisid/       # Helm chart
│       └── charttest/   # Chart testleri (ayrı Go modülü)
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
docker compose -f compose.stack.yaml up -d --build --wait
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

Helm chart testleri ayrı bir Go modülündedir (Helm SDK'sı ana modüle bağımlılık eklemesin diye):

```bash
cd deployments/helm/charttest && go test ./...
```

## Lisans

[MIT](LICENSE)
