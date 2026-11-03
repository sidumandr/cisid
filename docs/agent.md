# Agent

`cmd/agent` bir worker süreci başlatır. Agent server'a WebSocket ile bağlanır, atanan job'ları [Docker runner](runner.md) ile çalıştırır, logları anlık olarak server'a gönderir ve sonucu raporlar. Asıl mantık `internal/agent` paketindedir.

## Çalıştırma

```bash
CISID_SERVER_URL=ws://localhost:8080/agent/ws CISID_AGENT_NAME=worker-1 go run ./cmd/agent
```

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_SERVER_URL` | `ws://localhost:8080/agent/ws` | Server'ın agent endpoint'i (`ws://` ya da `wss://`) |
| `CISID_AGENT_NAME` | hostname | Server'a bildirilen ad (`X-Cisid-Agent` başlığı ve `agent_hello`) |
| `DOCKER_HOST` vb. | Docker varsayılanı | Docker daemon bağlantısı (`client.FromEnv`) |

`SIGINT` / `SIGTERM` graceful shutdown başlatır (aşağıya bak).

## Protokol

Her mesaj bir JSON zarfıdır. Tipler `pkg/models/protocol.go` dosyasındadır.

```json
{"id": 17, "type": "<mesaj tipi>", "payload": { ... }}
```

`id` sadece agent'ın gönderdiği `job_log`, `job_result` ve `job_rejected` mesajlarında bulunur (bkz. [Onaylı teslim](#onaylı-teslim)).

| Tip | Yön | Payload |
|---|---|---|
| `agent_hello` | agent → server | `{"name":"worker-1","capacity":1,"running_job_id":42}`, her bağlantıda ilk mesaj. `running_job_id`: elindeki ya da sonucu henüz gönderilmemiş job (yoksa alan yazılmaz) |
| `job_assigned` | server → agent | `{"job_id":42,"steps":[{"name":"test","image":"golang:1.25","commands":["go test ./..."],"env":{},"secrets":{},"timeout_seconds":600}],"services":[{"name":"postgres","image":"postgres:18-alpine","env":{}}]}` |
| `job_rejected` | agent → server | `{"job_id":43,"reason":"agent busy"}` |
| `job_log` | agent → server | `{"job_id":42,"step":0,"stream":"stdout","seq":17,"data":"ok ...\n"}` |
| `job_result` | agent → server | `{"job_id":42,"status":"failed","exit_code":3,"failed_step":0,"error":"..."}` |
| `job_cancel` | server → agent | `{"job_id":42}`. Job'u durdur, sonucu `canceled` ve `canceled by request` olarak raporla |
| `ack` | server → agent | `{"id":17}`. 17 ve öncesindeki bütün mesajlar işlendi |
| `server_closing` | server → agent | `{}`. Server kapanıyor: agent göndermeyi bırakır ve bağlantıyı kendisi kapatır (bkz. [hub.md](hub.md#graceful-shutdown)) |

`seq` her job içinde 1'den başlar ve her log parçasında bir artar. Server sıralamayı ve kayıp parçaları (boşlukları) bununla tespit edebilir.

## Job yürütme

- Agent aynı anda **bir** job çalıştırır. Meşgulken gelen atamaya `job_rejected` döner. Kapanış sırasında gelen atamaya da `job_rejected` döner, sebep `"agent shutting down"` olur.
- Atamada servis varsa agent önce job'a özel bir Docker ağı oluşturur ve servisleri bu ağda, adlarını host adı olarak kullanarak başlatır. Adımlar aynı ağda çalışır. Job bitince servisler ve ağ silinir ([pipeline.md](pipeline.md#servisler)).
- Adımlar sırayla çalışır ve ilk başarısız adımda durulur.
- Her job'a özel bir Docker volume oluşturulur (`cisid-job-<id>-<rastgele>`). Volume tüm adımlarda `/workspace` yoluna bağlanır ve çalışma dizini olarak kullanılır. Job bitince volume silinir.
- Komutlar tek bir shell script olarak çalıştırılır: `/bin/sh -c "set -ex\n<komutlar>"`. Böylece ilk hatalı komutta durulur ve her komut çalışmadan önce stderr'a yazılır.
- Adımın istediği secret'lar ortam değişkeni olarak eklenir ve adımın çıktısında `***` ile maskelenir ([secrets.md](secrets.md#loglarda-maskeleme)).
- Her adıma şu ortam değişkenleri eklenir: `CI=true`, `CISID=true`, `CISID_JOB_ID`, `CISID_STEP`. Bunları adımın kendi `env` alanı ezemez.
- `timeout_seconds` runner'ın `Step.Timeout` alanına aktarılır.

| Sonuç | `status` | `exit_code` | `error` |
|---|---|---|---|
| Tüm adımlar 0 ile bitti | `success` | 0 | - |
| Bir adım 0 dışı kodla bitti | `failed` | adımın kodu | - |
| Runner hatası (pull, timeout, ...) | `failed` | -1 | hata mesajı |
| Geçersiz atama (adım, image ya da komut yok) | `failed` | -1 | `invalid job ...` |
| Agent kapatıldı | `canceled` | -1 | `context canceled` |

## Bağlantı ve yeniden bağlanma

- Bağlantı koparsa ya da kurulamazsa agent **exponential backoff + jitter** ile tekrar dener. Bekleme süresi 1 sn'den başlar, her denemede ikiye katlanır ve en fazla 1 dk olur. Gerçek bekleme `[d/2, d]` aralığında rastgele seçilir.
- Başarılı bir bağlantıdan sonra backoff sıfırlanır.
- Job'lar bağlantıya değil agent'ın yaşam süresine bağlıdır. Bağlantı koparsa çalışan job devam eder.
- Giden mesajlar bağlantılar arasında yaşayan bir **outbox**'ta bekler. Outbox en fazla 4096 bekleyen ya da onaylanmamış mesaj tutar. Kopukluk sırasında üretilen loglar ve sonuçlar yeniden bağlanınca gönderilir.
- Outbox dolarsa yeni **log** parçaları atılır, job yavaşlamaz ve atılan parçalar `seq` boşluğu olarak görünür. **Sonuç** mesajları atılmaz, yer açılmasını bekler.

## Onaylı teslim

- Agent her mesaja, gönderdiği anda artan bir `id` verir. Bu yüzden `id` sırası gönderim sırasıyla aynıdır.
- Mesaj, server `ack` ile onaylayana kadar outbox'ta tutulur. `ack` kümülatiftir: `{"id":17}` 17 ve öncesindeki bütün mesajları onaylar.
- Bağlantı nasıl koparsa kopsun (ağ hatası, server çökmesi) onaylanmamış mesajlar yeni bağlantıda önce ve aynı `id` ile tekrar gönderilir.
- Server tekrarlara karşı güvenlidir:
  - Log parçaları `(job_id, seq)` anahtarıyla bir kez yazılır.
  - İkinci kez gelen sonuç ya da ret yok sayılır ve yine onaylanır.
- Sonucu henüz onaylanmamış job hello'da `running_job_id` olarak bildirilmeye devam eder.
- Kapanışta `FlushTimeout` boyunca bütün mesajların **onaylanması** beklenir.

`TestIntegrationLogsSurviveNetworkFailures`, 300 satırlık log akışı sırasında TCP bağlantısını üç kez keser ve her parçanın sırayla tam bir kez yazıldığını doğrular.

## Eşzamanlılık

| Goroutine | Görev | Ne zaman biter |
|---|---|---|
| executor | Job'ları sırayla çalıştırır | Agent context'i iptal edilince |
| writer | Bağlantıya yazan **tek** goroutine | Bağlantı kapanınca |
| reader | Mesajları okur, job'ları executor'a verir | Bağlantı kapanınca |
| flush | Kapanışta outbox'ı boşaltır, sonra bağlantıyı kapatır | Executor bitince |

Writer ve reader bir `errgroup` içinde çalışır. Biri hata verirse diğeri de durur.

## Graceful shutdown

1. Agent context'i iptal edilir (`SIGTERM`).
2. Çalışan adımın container'ı runner tarafından durdurulur: önce `SIGTERM`, grace period sonunda `SIGKILL`.
3. `canceled` sonucu outbox'a yazılır, workspace volume silinir.
4. Outbox en fazla `FlushTimeout` (10 sn) boyunca boşaltılır.
5. Bağlantı `1000 Normal Closure` ile kapatılır.

## Bilinen sınırlamalar (Faz 2)

- Agent 5 dakikadan (lease) uzun süre bağlanamazsa job'u başka bir agent'a verilir. Geri döndüğünde ilk job'un sonucu atılır (bkz. [hub.md](hub.md#canlılık-ve-lease)).
- Kimlik doğrulama yok (Faz 4).

## Testler

| Test | Senaryo |
|---|---|
| `TestAgentRunsAssignedJob` | İki adım; runner'a giden `Step`, log sırası ve `seq`, sonuç, volume temizliği |
| `TestAgentReportsFailedStep` | 0 dışı exit kodu, sonraki adım çalışmaz |
| `TestAgentReportsRunnerError` | Image pull hatası |
| `TestAgentFailsInvalidAssignment` | Adım, image ya da komut eksik |
| `TestAgentRejectsJobWhileBusy` | İkinci atama reddedilir |
| `TestAgentReconnectsAfterConnectionLoss` | Server bağlantıyı kapatır, agent yeniden bağlanır |
| `TestAgentRetriesWhileServerUnavailable` | Handshake 3 kez 503 alır, 4. denemede bağlanır |
| `TestAgentDeliversResultAfterReconnect` | Kopukluk sırasında biten job'un sonucu yeni bağlantıdan gelir |
| `TestAgentShutdownCancelsRunningJob` | Kapanışta `canceled` sonucu gönderilir, `Run` temiz döner |
| `TestAgentIgnoresUnknownAndMalformedMessages` | Bozuk ya da bilinmeyen mesajlar agent'ı düşürmez |
| `TestAgentHelloReportsUndeliveredResult` | Sonucu gönderilememiş job, yeniden bağlanınca hello'da bildirilir |
| `TestAgentClosesConnectionWhenServerDrains` | `server_closing` gelince agent temiz kapatır ve yeniden bağlanır |
| `TestAgentResendsUnacknowledgedMessages` | Onaylanmamış log ve sonuç yeni bağlantıda aynı `id` ile tekrar gönderilir |
| `TestOutbox*` | Kümülatif ack, outbox sınırı |
| `TestBackoff*` | Büyüme, üst sınır, overflow, jitter aralığı |
| `TestDockerIntegrationJob` | Gerçek Docker: adımlar workspace paylaşır, exit kodu raporlanır |

Server tarafı `httptest` ile kurulan gerçek bir WebSocket endpoint'idir. Runner `testify/mock` ile mock'lanır.

```bash
go test ./internal/agent/
```

```bash
CISID_DOCKER_TESTS=1 go test ./internal/agent/ -run Integration -v
```
