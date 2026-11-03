# cisid

Go ile yazılmış, Docker tabanlı, self-hosted bir CI/CD sistemi.

> Durum: proje iskeleti. Henüz çalışan bir özellik yok.

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
│   ├── runner/      # Docker entegrasyonu
│   └── db/          # PostgreSQL erişimi
├── pkg/
│   └── models/      # Paylaşılan tipler
└── api/
    └── proto/       # gRPC tanımları (ileride)
```

## Derleme

```bash
go build ./...
```
