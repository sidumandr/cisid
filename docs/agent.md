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
{"type": "<mesaj tipi>", "payload": { ... }}
```

| Tip | Yön | Payload |
|---|---|---|
| `agent_hello` | agent → server | `{"name":"worker-1","capacity":1}`, her bağlantıda ilk mesaj |
| `job_assigned` | server → agent | `{"job_id":42,"steps":[{"name":"test","image":"golang:1.25","commands":["go test ./..."],"env":{},"timeout_seconds":600}]}` |
| `job_rejected` | agent → server | `{"job_id":43,"reason":"agent busy"}` |
| `job_log` | agent → server | `{"job_id":42,"step":0,"stream":"stdout","seq":17,"data":"ok ...\n"}` |
| `job_result` | agent → server | `{"job_id":42,"status":"failed","exit_code":3,"failed_step":0,"error":"..."}` |

`seq` her job içinde 1'den başlar ve her log parçasında bir artar. Server sıralamayı ve kayıp parçaları (boşlukları) bununla tespit edebilir.

## Job yürütme

- Agent aynı anda **bir** job çalıştırır. Meşgulken gelen atamaya `job_rejected` döner. Kapanış sırasında gelen atamaya da `job_rejected` döner, sebep `"agent shutting down"` olur.
- Adımlar sırayla çalışır ve ilk başarısız adımda durulur.
- Her job'a özel bir Docker volume oluşturulur (`cisid-job-<id>-<rastgele>`). Volume tüm adımlarda `/workspace` yoluna bağlanır ve çalışma dizini olarak kullanılır. Job bitince volume silinir.
- Komutlar tek bir shell script olarak çalıştırılır: `/bin/sh -c "set -ex\n<komutlar>"`. Böylece ilk hatalı komutta durulur ve her komut çalışmadan önce stderr'a yazılır.
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
- Giden mesajlar bağlantılar arasında yaşayan bir **outbox** kuyruğunda (4096 mesaj) bekler. Kopukluk sırasında üretilen loglar ve sonuçlar yeniden bağlanınca gönderilir.
- Kuyruk dolarsa yeni **log** parçaları atılır, job yavaşlamaz ve atılan parçalar `seq` boşluğu olarak görünür. **Sonuç** mesajları atılmaz, kuyrukta yer açılmasını bekler.

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

- Mesajlar onaylı (ack) teslim edilmez. Yazılırken bağlantı koparsa o mesaj kaybolur.
- Heartbeat ya da lease yok. Server, ölen bir agent'ın job'unu henüz tekrar kuyruğa almıyor.
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
| `TestBackoff*` | Büyüme, üst sınır, overflow, jitter aralığı |
| `TestDockerIntegrationJob` | Gerçek Docker: adımlar workspace paylaşır, exit kodu raporlanır |

Server tarafı `httptest` ile kurulan gerçek bir WebSocket endpoint'idir. Runner `testify/mock` ile mock'lanır.

```bash
go test ./internal/agent/
```

```bash
CISID_DOCKER_TESTS=1 go test ./internal/agent/ -run Integration -v
```
