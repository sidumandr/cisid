# Job API

[English](../api.md) · **Türkçe**

Server, job'ları ve loglarını bir HTTP API üzerinden sunar (`internal/api/jobs.go`). Job'lar okunabilir, iptal edilebilir ve yeniden çalıştırılabilir. Bütün yanıtlar JSON'dur, canlı log akışı ise [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html) ile gelir.

> Okuma uç noktaları herkese açık ya da giriş isteyen olabilir ([Okuma erişimi](#okuma-erişimi)). Değişiklik yapan uç noktalar `maintainer` rolü ister: API token'ı, bir kullanıcının kişisel token'ı ya da oturumu ([accounts.md](accounts.md)).

## Endpoint'ler

| Yol | Açıklama |
|---|---|
| `GET /api/jobs?limit=50` | Son job'lar, en yenisi önce. `limit` en fazla 200. |
| `GET /api/jobs/{id}` | Tek job |
| `GET /api/jobs/{id}/logs?after=0` | `seq` > `after` olan log parçaları, istek başına en fazla 1000 |
| `GET /api/jobs/{id}/logs/stream` | Canlı log akışı (SSE) |
| `GET /api/jobs/{id}/artifacts` | Job'un artifact'ları: `name`, `size`, `sha256`, `created_at` ([Artifact'lar](#artifactlar)) |
| `GET /api/jobs/{id}/artifacts/{name}` | Bir artifact'ın içeriği, indirme olarak |
| `POST /api/jobs/{id}/cancel` | Job'u iptal et (maintainer) |
| `POST /api/jobs/{id}/rerun` | Aynı commit için yeni job oluştur (maintainer) |
| `POST /api/jobs/{id}/approve` | Onay bekleyen bir fork job'unun agent'larda çalışmasına izin ver (maintainer); beklemiyorsa `409` ([webhook.md](webhook.md#pull-requestler)) |

Hatalar: geçersiz parametre `400`, olmayan job `404`, istemci başına rate limit aşıldıysa `429` (`Retry-After: 1`, bkz. `CISID_RATE_LIMIT`). Gövde `{"status":"error","reason":"..."}` biçimindedir.

### Job

```json
{
  "id": 42,
  "repo": "sidumandr/cisid",
  "clone_url": "https://github.com/sidumandr/cisid.git",
  "branch": "main",
  "commit_sha": "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
  "status": "failed",
  "created_at": "2026-11-03T10:45:01Z",
  "event": "pull_request",
  "pull_request": 7,
  "target_branch": "main",
  "fork": true,
  "agent": "worker-1",
  "started_at": "2026-11-03T10:45:01Z",
  "steps": ["clone", "test"],
  "matrix": {"GO_VERSION": "1.27"},
  "parent_id": 41,
  "exit_code": 1,
  "failed_step": 1,
  "error": "",
  "finished_at": "2026-11-03T10:45:31Z",
  "logs_expired_at": "2027-02-01T09:00:00Z"
}
```

`status`: `pending`, `running`, `success`, `failed`, `canceled`. `event` job'u başlatan olaydır: `push`, `pull_request`, `manual` ya da `schedule`. Pull request job'larında `pull_request` PR numarası, `target_branch` birleştirileceği branch, `branch` PR'ın kendi branch'i, `fork` ise değişikliğin bir fork'tan geldiğidir. `steps`, job'a planlanan adımların adlarıdır, job bir agent'a verilince dolar. `failed_step` ve log parçalarındaki `step` bu listedeki sırayı gösterir (`0` her zaman clone). `when` ile atlanan adımlar listede yer almaz. `matrix` job'un çalıştırdığı kombinasyondur, `parent_id` ise onu oluşturan ilk matrix job'udur ([pipeline.md](pipeline.md#matrix)). `current_step` yalnızca çalışan job'larda bulunur: logu en son akan adımın sırasıdır. `needs_approval` bir maintainer'ı bekleyen fork job'unu işaretler, `approved_by` onu kimin onayladığını söyler. `labels`, job o etiketler için kuyruğa geri döndüyse onu çalıştıracak agent'ta olması gereken etiketlerdir ([pipeline.md](pipeline.md#etiketler)). `step_results`, `steps`'teki her adımın agent'ın bildirdiği şekilde nasıl gittiğini söyler: `state` (`pending`, `running`, `success`, `failed`, `canceled` ya da `skipped`), `exit_code`, `started_at` ve `finished_at` (agent'ın saati); 0.4'ten eski agent'ların çalıştırdığı job'larda bulunmaz. `logs_expired_at` varsa job'un logları saklama süresi dolduğu için o tarihte silinmiştir (`CISID_LOG_RETENTION`, [server.md](server.md)); log uç noktaları boş döner.

### Log parçası

```json
{"job_id": 42, "step": 1, "stream": "stderr", "seq": 17, "data": "+ go test ./...\n"}
```

`seq` job içinde 1'den başlar ve sırayı belirler. Parçalar satır sınırına denk gelmeyebilir. İstemci aynı `stream`'deki parçaları uç uca ekleyerek gösterir.

## Okuma erişimi

Okuma `CISID_READ_ACCESS=signed-in` ise (`CISID_READ_TOKEN` tanımlıyken varsayılan budur) job, log ve canlı akış uç noktaları ile `/metrics` şunlardan birini ister:

- `Authorization: Bearer <token>` başlığı: okuma token'ı, API token'ı ya da bir kullanıcının kişisel token'ı.
- Bir kullanıcının oturum cookie'si ([accounts.md](accounts.md#giriş-ve-oturumlar)).
- `POST /api/login` gövdesi `{"token":"..."}` ile alınan okuma cookie'si. Token doğruysa `204` döner ve server HttpOnly, `SameSite=Strict` bir cookie koyar. Web arayüzü cookie'leri kullanır, çünkü tarayıcının canlı log akışı (EventSource) başlık gönderemez. Bu cookie token'ın kendisini değil özetini taşır ve yalnızca okumaya izin verir; API token'ıyla girilse bile değişiklik yapamaz. `POST /api/logout` cookie'leri siler.

Kimlik yoksa ya da yanlışsa `401` döner. Health check'ler (`/healthz`, `/readyz`) ve webhook her zaman açıktır.

```bash
curl -H "Authorization: Bearer $CISID_READ_TOKEN" http://localhost:8080/api/jobs
```

## Polling

Webhook'u server'a ulaşamayan sağlayıcılar için branch'ler yoklanabilir: `GET`, `POST /api/repos/{owner}/{repo}/watches` ve `DELETE /api/repos/{owner}/{repo}/watches/{branch}`. Ayrıntılar: [polling.md](polling.md).

## Artifact'lar

Build'in sakladığı dosyalar ([pipeline.md](pipeline.md#artifactlar)) job'lar gibi okunur: `GET /api/jobs/{id}/artifacts` listeler, `GET /api/jobs/{id}/artifacts/{name}` birini indirir; addaki `/` olduğu gibi yazılır (`.../artifacts/dist/app`). İndirmeler `application/octet-stream` ve `Content-Disposition: attachment` ile gelir; böylece tarayıcı, içinde ne olursa olsun dosyayı açmak yerine kaydeder. `ETag` içeriğin SHA-256'sıdır.

```bash
curl -fLO http://localhost:8080/api/jobs/42/artifacts/dist/app
```

## Bildirimler ve rozetler

Bir reponun build sonuçlarının nereye bildirileceği (sohbet, webhook, e-posta): `GET /api/repos/{owner}/{repo}/notifications`, `PUT` ve `DELETE .../notifications/{name}`, `POST .../notifications/{name}/test`, hepsi `maintainer` ister. Bir branch'in son sonucu SVG rozet olarak: `GET /api/repos/{owner}/{repo}/badge.svg?branch=main`. Ayrıntılar: [notifications.md](notifications.md).

## Elle ve zamanlanmış build'ler

Bir branch'in son commit'i push beklemeden derlenebilir. Server commit'i ve clone adresini reponun sağlayıcısının API'sinden alır (özel repolar için sağlayıcının token'ı gerekir). Diğer sağlayıcıların repoları adreste önekiyle yazılır, ör. `/api/repos/gitea:office/app/builds` ya da `/api/repos/gitlab:group%2Fsub/project/builds` ([forges.md](forges.md#repo-adları)). Bu uç noktalar `maintainer` ister; zamanlamaları listelemek okuma erişimi ister.

| Yol | Açıklama |
|---|---|
| `POST /api/repos/{owner}/{repo}/builds` | `{"branch":"main"}` ile branch'in son commit'ini hemen derler; `branch` verilmezse varsayılan branch. `201` ve job döner. Repo ya da branch yoksa ya da server o sağlayıcı için yapılandırılmamışsa `404`, sağlayıcıya ulaşılamazsa `503`. |
| `GET /api/repos/{owner}/{repo}/schedules` | Repo'nun zamanlamaları: ad, cron, branch, `next_run_at`, `last_run_at`, `last_job_id` |
| `PUT /api/repos/{owner}/{repo}/schedules/{name}` | `{"cron":"0 3 * * *","branch":"main"}` ile zamanlamayı oluşturur ya da değiştirir. Sonraki çalışma zamanıyla birlikte zamanlamayı döner. |
| `DELETE /api/repos/{owner}/{repo}/schedules/{name}` | `204`, yoksa `404` |

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' http://localhost:8080/api/repos/sidumandr/cisid/builds
```

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"cron":"CRON_TZ=Europe/Istanbul 0 3 * * *"}' http://localhost:8080/api/repos/sidumandr/cisid/schedules/nightly
```

- **Cron:** Beş alanlı standart biçim (`dakika saat gün ay haftanın-günü`, ör. `0 3 * * 1-5`) ya da `@hourly`, `@daily`, `@weekly`, `@monthly`. Saatler UTC'dir; başka bir saat dilimi için başa `CRON_TZ=Europe/Istanbul ` ekleyin. Zamanlama adı küçük harf, rakam, `.`, `_` ve `-` içerebilir.
- **Çalışma:** Server her 30 saniyede vakti gelen zamanlamaları alır ve branch'in son commit'i için bir job açar. Birden fazla server kopyası aynı zamanlamayı iki kez çalıştırmaz: zamanlama, veritabanında kilitlenerek tek bir kopyaya verilir. Server kapalıyken kaçırılan çalışmalar açılınca bir kez yapılır, her kaçırılan zaman için ayrı ayrı değil. Sağlayıcıya ulaşılamazsa o çalışma atlanır, zamanlama sonraki zamanında devam eder.
- **Job'lar:** `event` elle başlatılanlarda `manual`, zamanlananlarda `schedule` olur. `when.event` ile bunlara özel adımlar yazılabilir. Branch'e sınırlı secret'lar bu job'lara push'taki gibi verilir. Commit status'ları ayrı bağlamlarda görünür: `cisid/manual`, `cisid/schedule` ([commit-status.md](commit-status.md)).

## İptal ve yeniden çalıştırma

Bu uç noktalar `maintainer` rolü ister: `CISID_API_TOKEN`, bir maintainer'ın kişisel token'ı (`Authorization: Bearer`) ya da oturumu. Ne API token'ı ne de kullanıcı varsa `403`, kimlik eksik ya da yanlışsa `401`, rol yetmiyorsa `403` döner.

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/jobs/42/cancel
```

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/jobs/42/rerun
```

**İptal:**

| Job durumu | Sonuç |
|---|---|
| `pending` | Hemen `canceled` olur, yanıt `200` |
| `running` | İptal isteği kaydedilir, yanıt `202`. Agent'ın bağlı olduğu server agent'a `job_cancel` gönderir (en geç 2 sn), agent container'ı durdurur ve job `canceled` olur. |
| Bitmiş | `409` |

Her iki durumda da job'un `error` alanı `canceled by request` olur. İstek hangi server'a gelirse gelsin çalışır. Agent bir daha hiç bağlanmazsa iptal isteği olan job, lease dolunca tekrar kuyruğa alınmaz, `canceled` olur.

**Yeniden çalıştırma:** Aynı repo, branch ve commit için yeni bir `pending` job oluşturur. Yanıt `201` olur, `Location` başlığı yeni job'u gösterir.

## Canlı akış

```bash
curl -N http://localhost:8080/api/jobs/42/logs/stream
```

```
retry: 3000

event: status
data: {"id":42,"status":"running",...}

id: 1
event: log
data: {"job_id":42,"step":0,"stream":"stderr","seq":1,"data":"+ git init -q .\n"}

: keepalive

event: status
data: {"id":42,"status":"success",...}
```

- İlk olay, job'un o anki durumunu taşıyan `status` olayıdır. Ardından birikmiş loglar, sonra yeni loglar gelir.
- Job bittiğinde son bir `status` gönderilir ve server akışı kapatır. Bitmiş bir job için akış, logların tamamını oynatıp hemen kapanır.
- Her `log` olayının `id` alanı `seq` değeridir. Bağlantı koparsa tarayıcıdaki `EventSource` `Last-Event-ID` başlığıyla kaldığı yerden devam eder. Elle devam etmek için `?after=<seq>` kullanılabilir.
- Akış 5 sn'de bir `: keepalive` yorumu gönderir, böylece proxy'ler boşta kalan bağlantıyı kapatmaz.
- Server kapanırken açık akışlar kapatılır. Bu yüzden shutdown'ı bekletmezler.

```js
const es = new EventSource("/api/jobs/42/logs/stream");
es.addEventListener("log", (e) => append(JSON.parse(e.data)));
es.addEventListener("status", (e) => {
  const job = JSON.parse(e.data);
  if (["success", "failed", "canceled"].includes(job.status)) es.close();
});
```

## Nasıl canlı?

Log parçası yazıldığında ve job bittiğinde veritabanı `cisid_job_events` kanalına job numarasını `NOTIFY` eder (`internal/db`). Her server bu kanalı tek bir bağlantıyla `LISTEN` eder (`internal/events`) ve ilgili job'un açık akışlarını uyandırır. Akış uyanınca yeni logları veritabanından okur. Sonuç olarak:

- Agent server A'ya, istemci server B'ye bağlı olsa da loglar canlı akar.
- Bildirim dinleyicisi koparsa yeniden bağlanır ve bütün akışları uyandırır. Akışlar ayrıca 5 sn'de bir kendisi de kontrol eder. Bildirim kaçsa bile hiçbir log kaybolmaz, en fazla gecikir.
- Aynı log parçası iki kez gelirse (ack ile yeniden gönderim) sadece ilki yazılır ve sadece ilki bildirilir.

## Neden WebSocket değil?

Akış tek yönlü, server'dan istemciye doğru. SSE bunu düz HTTP ile yapar: `curl` ile izlenebilir, tarayıcıda ek kütüphane gerektirmez ve kopan bağlantıyı `Last-Event-ID` ile otomatik sürdürür. WebSocket agent ↔ server gibi çift yönlü kanallar için kullanılıyor.
