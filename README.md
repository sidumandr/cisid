# cisid

Go ile yazılmış, Docker tabanlı, self-hosted bir CI/CD sistemi.

> Durum: Faz 1 (MVP) geliştiriliyor. Ayrıntılar için [ROADMAP.md](ROADMAP.md).

## Dokümanlar

- [ROADMAP.md](ROADMAP.md): fazlar ve ilerleme
- [CLAUDE.md](CLAUDE.md): kod ve commit kuralları
- [docs/webhook.md](docs/webhook.md): GitHub webhook kurulumu ve davranışı
- [docs/runner.md](docs/runner.md): Docker runner kullanımı ve yaşam döngüsü

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
       SQL     │             │  job dağıtımı / durum raporu
               ▼             │  (HTTP, ileride gRPC: api/proto)
      ┌────────────────┐     │
      │   PostgreSQL   │     │
      │ pipelines,     │     │
      │ jobs, logs     │     │
      └────────────────┘     │
                             │
        ┌────────────────────┴──────┐
        │     cmd/agent (worker)    │   ×N
        │                           │
        │  internal/runner          │
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
│   ├── api/         # HTTP handler'ları
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

Race detector cgo gerektirir. Windows'ta bir C derleyicisi (ör. MinGW-w64) kurulu olmalı ya da testler Linux/WSL üzerinde çalıştırılmalıdır:

```bash
go test -race ./...
```
