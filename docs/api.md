# Job API

Server, job'ları ve loglarını salt-okunur bir HTTP API üzerinden sunar (`internal/api/jobs.go`). Bütün yanıtlar JSON'dur, canlı log akışı ise [Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html) ile gelir.

> Kimlik doğrulama yok (Faz 4). Server'ı güvenilmeyen bir ağa açmadan önce önüne erişim kontrolü koyun.

## Endpoint'ler

| Yol | Açıklama |
|---|---|
| `GET /api/jobs?limit=50` | Son job'lar, en yenisi önce. `limit` en fazla 200. |
| `GET /api/jobs/{id}` | Tek job |
| `GET /api/jobs/{id}/logs?after=0` | `seq` > `after` olan log parçaları, istek başına en fazla 1000 |
| `GET /api/jobs/{id}/logs/stream` | Canlı log akışı (SSE) |

Hatalar: geçersiz parametre `400`, olmayan job `404`. Gövde `{"status":"error","reason":"..."}` biçimindedir.

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
  "agent": "worker-1",
  "started_at": "2026-11-03T10:45:01Z",
  "exit_code": 1,
  "failed_step": 1,
  "error": "",
  "finished_at": "2026-11-03T10:45:31Z"
}
```

`status`: `pending`, `running`, `success`, `failed`, `canceled`. `failed_step` adım sırasıdır: `0` clone adımı, `1` ise `.cisid.yml`'deki ilk adım.

### Log parçası

```json
{"job_id": 42, "step": 1, "stream": "stderr", "seq": 17, "data": "+ go test ./...\n"}
```

`seq` job içinde 1'den başlar ve sırayı belirler. Parçalar satır sınırına denk gelmeyebilir. İstemci aynı `stream`'deki parçaları uç uca ekleyerek gösterir.

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
