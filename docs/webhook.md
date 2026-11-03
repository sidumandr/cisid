# GitHub Webhook

`internal/api/webhook.go` GitHub'dan gelen webhook'ları alır ve her branch push'u için `jobs` tablosuna `pending` durumunda bir kayıt ekler.

## GitHub tarafında kurulum

Repo → **Settings → Webhooks → Add webhook**:

| Alan | Değer |
|---|---|
| Payload URL | `https://<sunucu>/webhooks/github` |
| Content type | `application/json` |
| Secret | Rastgele, uzun bir değer (ör. `openssl rand -hex 32`) |
| Events | **Just the push event** |

Aynı secret, server'a da verilmelidir. Secret boşsa `NewWebhookHandler` `ErrEmptySecret` döner, handler oluşturulmaz.

## İstek işleme

1. Sadece `POST` kabul edilir.
2. Body en fazla 25 MB olabilir (GitHub'ın limiti).
3. `X-Hub-Signature-256` başlığı `sha256=<hex>` formatında olmalı ve body'nin secret ile HMAC-SHA256'sına eşit olmalıdır. Karşılaştırma sabit zamanlıdır (`hmac.Equal`).
4. `X-GitHub-Event` başlığına göre:
   - `ping` → `200 {"status":"pong"}`
   - `push` → aşağıya bak
   - diğerleri → `200 {"status":"ignored"}`
5. Push payload'ından alınan alanlar:
   - `repository.full_name` → `jobs.repo`
   - `repository.clone_url` → `jobs.clone_url`
   - `ref` (`refs/heads/` öneki atılır) → `jobs.branch`
   - `after` → `jobs.commit_sha` (40 ya da 64 karakter hex olmalı)

## Yanıtlar

| Durum | Kod | Body |
|---|---|---|
| Job kuyruğa alındı | `202` | `{"status":"queued","job_id":42}` |
| Tag push | `200` | `{"status":"ignored","reason":"not a branch push"}` |
| Branch silindi | `200` | `{"status":"ignored","reason":"branch deleted"}` |
| Ping | `200` | `{"status":"pong"}` |
| Desteklenmeyen event | `200` | `{"status":"ignored","reason":"unsupported event \"issues\""}` |
| İmza eksik / yanlış | `401` | `{"status":"error","reason":"invalid signature"}` |
| Bozuk ya da eksik payload | `400` | `{"status":"error","reason":"invalid push payload"}` |
| Body 25 MB'tan büyük | `413` | `{"status":"error","reason":"payload too large"}` |
| `POST` dışında method | `405` | `{"status":"error","reason":"method not allowed"}` |
| Veritabanı hatası | `500` | `{"status":"error","reason":"could not queue job"}` |

## Elle deneme

```bash
SECRET=s3cr3t-webhook-key
BODY=internal/api/testdata/push.json
SIG="sha256=$(openssl dgst -sha256 -hmac "$SECRET" < "$BODY" | awk '{print $NF}')"

curl -i -X POST http://localhost:8080/webhooks/github \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: push" \
  -H "X-Hub-Signature-256: $SIG" \
  --data-binary @"$BODY"
```

Sunucuyu yerelde çalıştırmak için bkz. [server.md](server.md).

## Testler

`internal/api/webhook_test.go`, `internal/api/testdata/` altındaki gerçekçi GitHub payload'larını kullanır:

| Dosya | Senaryo |
|---|---|
| `push.json` | `main` branch'ine push |
| `push_nested_branch.json` | `feature/webhook` gibi `/` içeren branch |
| `push_branch_deleted.json` | Branch silme (`deleted: true`) |
| `push_tag.json` | Tag push (`refs/tags/...`) |
| `ping.json` | Webhook ilk eklendiğinde gelen ping |

Veritabanı `testify/mock` ile mock'lanır. `internal/db` testi gerçek PostgreSQL ister:

```bash
CISID_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/cisid_test?sslmode=disable go test ./internal/db/...
```
