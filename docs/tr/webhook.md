# Webhook'lar

[English](../webhook.md) · **Türkçe**

`internal/api/webhook.go` sağlayıcılardan gelen webhook'ları alır. Her branch push'u ve her pull request değişikliği için `jobs` tablosuna `pending` durumunda bir kayıt ekler. Her sağlayıcının kendi adresi vardır: `/webhooks/github`, `/webhooks/gitea`, `/webhooks/forgejo`, `/webhooks/gitlab`, `/webhooks/bitbucket`. Bir sağlayıcının adresi, webhook secret'ı verildiğinde açılır.

Bu belge GitHub'ı anlatır; diğer sağlayıcıların kurulumu ve farkları [forges.md](forges.md)'dedir. Tekilleştirme, fork kuralı, PR iptali ve yanıtlar hepsinde aynıdır.

## GitHub tarafında kurulum

Repo → **Settings → Webhooks → Add webhook**:

| Alan | Değer |
|---|---|
| Payload URL | `https://<sunucu>/webhooks/github` |
| Content type | `application/json` |
| Secret | Rastgele, uzun bir değer (ör. `openssl rand -hex 32`) |
| Events | **Let me select individual events** → *Pushes* ve *Pull requests* |

Aynı secret, server'a da verilmelidir. Secret boşsa `NewWebhookHandler` `ErrEmptySecret` döner, handler oluşturulmaz.

## İstek işleme

1. Sadece `POST` kabul edilir.
2. Body en fazla 25 MB olabilir (GitHub'ın limiti).
3. `X-Hub-Signature-256` başlığı `sha256=<hex>` formatında olmalı ve body'nin secret ile HMAC-SHA256'sına eşit olmalıdır. Karşılaştırma sabit zamanlıdır (`hmac.Equal`).
4. `X-GitHub-Event` başlığına göre:
   - `ping` → `200 {"status":"pong"}`
   - `push` → aşağıya bak
   - `pull_request` → [Pull request'ler](#pull-requestler)
   - diğerleri → `200 {"status":"ignored"}`
5. Push payload'ından alınan alanlar:
   - `repository.full_name` → `jobs.repo`
   - `repository.clone_url` → `jobs.clone_url`
   - `ref` (`refs/heads/` öneki atılır) → `jobs.branch`
   - `after` → `jobs.commit_sha` (40 ya da 64 karakter hex olmalı)

## Pull request'ler

`pull_request` olayında yalnızca şu eylemler job oluşturur: `opened`, `reopened` ve yeni commit'lerin geldiği `synchronize`. Diğerleri (`labeled`, `edited` gibi) yok sayılır.

- Derlenen commit, PR'ın son commit'idir (`pull_request.head.sha`). Commit ana repodan çekilir; GitHub her PR'ın commit'lerini ana repoda da sunduğu için fork'un adresi hiç kullanılmaz.
- `jobs.branch` PR'ın kendi branch'idir (`head.ref`), `target_branch` birleştirileceği branch (`base.ref`), `pull_request` PR numarasıdır.
- PR'a yeni bir commit gelince aynı PR'ın bitmemiş eski job'ları iptal edilir: bekleyenler hemen, çalışanlar agent'ları tarafından durdurularak.
- PR kapanınca (merge dahil) bitmemiş job'ları iptal edilir.
- **Fork'lar:** Başka bir repodan gelen PR'lar varsayılan olarak derlenmez, çünkü dışarıdan gelen kodu agent'larda çalıştırır. `CISID_BUILD_FORKS=true` ile açılır. Açıksa da fork PR'larına hiçbir secret verilmez ([secrets.md](secrets.md)).
- **Fork onayı:** `CISID_BUILD_FORKS=approve` ile fork PR'ları kuyruğa alınır ama bir maintainer her job'u onaylayana kadar hiçbir agent çalıştırmaz; onay job sayfasındaki **Approve** düğmesiyle ya da `POST /api/jobs/{id}/approve` ile verilir ([api.md](api.md)). Commit status onay beklendiğini söyler. PR'a gelen her yeni commit yeni kod getirdiği için yeniden onay ister; PR'ın kendi reposundaki branch'ler hiçbir zaman onay istemez.
- `pull_request.updated_at` 7 günden eskiyse olay yok sayılır (`stale delivery`).

## Tekrar (replay) koruması

- Bir push payload'ı yalnızca bir kez job oluşturur. Tekilleştirme anahtarı **imzalı gövdenin SHA-256 özetidir**, `X-GitHub-Delivery` başlığı değildir. O başlık imzaya dahil olmadığı için saldırgan onu değiştirebilir, ama gövdeyi imzayı bozmadan değiştiremez. Kayıt ve job aynı transaction'da oluşur, bu yüzden GitHub'ın "Redeliver" ile yaptığı tekrar gönderimler de güvenlidir.
- `repository.pushed_at` 7 günden eskiyse payload yok sayılır (`stale delivery`). Tekilleştirme kayıtları 8 gün tutulur ve sonra silinir. Böylece yakalanmış eski bir payload hiçbir zaman ikinci kez job oluşturamaz.

## Yanıtlar

| Durum | Kod | Body |
|---|---|---|
| Job kuyruğa alındı | `202` | `{"status":"queued","job_id":42}` (birden fazla job açıldıysa ayrıca `"job_ids":[42,43]`) |
| Tag push | `200` | `{"status":"ignored","reason":"not a branch push"}` |
| Branch silindi | `200` | `{"status":"ignored","reason":"branch deleted"}` |
| Aynı payload daha önce işlendi | `200` | `{"status":"ignored","reason":"duplicate delivery"}` |
| `pushed_at` 7 günden eski | `200` | `{"status":"ignored","reason":"stale delivery"}` |
| PR'da derleme gerektirmeyen eylem | `200` | `{"status":"ignored","reason":"pull request action \"labeled\" does not build"}` |
| Fork'tan PR, `CISID_BUILD_FORKS` kapalı | `200` | `{"status":"ignored","reason":"pull request from a fork (CISID_BUILD_FORKS is off)"}` |
| PR kapandı | `200` | `{"status":"canceled","reason":"pull request closed","canceled":[41,42]}` |
| Rate limit aşıldı | `429` | `{"status":"error","reason":"too many requests"}`, `Retry-After: 1` |
| Ping | `200` | `{"status":"pong"}` |
| Desteklenmeyen event | `200` | `{"status":"ignored","reason":"unsupported event \"issues\""}` |
| İmza eksik / yanlış | `401` | `{"status":"error","reason":"invalid signature"}` |
| Bozuk ya da eksik payload | `400` | `{"status":"error","reason":"invalid push payload"}` (PR'da `invalid pull request payload`) |
| Body 25 MB'tan büyük | `413` | `{"status":"error","reason":"payload too large"}` |
| `POST` dışında method | `405` | `{"status":"error","reason":"method not allowed"}` |
| Veritabanı hatası | `500` | `{"status":"error","reason":"could not queue job"}` |
| Sağlayıcının API'sine ulaşılamadı (yalnızca Bitbucket PR'larında) | `503` | `{"status":"error","reason":"bitbucket is unavailable, redeliver later"}` |

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
