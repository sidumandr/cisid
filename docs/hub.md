# Agent Hub

`internal/hub` agent protokolünün server tarafıdır. Agent'ların WebSocket bağlantılarını tutar, bekleyen job'ları boştaki agent'lara dağıtır ve agent'lardan gelen log ve sonuçları veritabanına yazar. Server'da `GET /agent/ws` yolunda çalışır.

## Akış

```
GitHub ──webhook──► jobs (pending) ──Wake──► dispatcher
                                                 │ ClaimJob (FOR UPDATE SKIP LOCKED)
                                                 │ Planner.Plan → adımlar
                                                 ▼
                                     job_assigned ──► agent
                                                       │
            job_logs ◄── AppendJobLog ◄── job_log ◄────┤
            jobs     ◄── FinishJob    ◄── job_result ◄─┘
```

1. Webhook bir job oluşturur (`pending`) ve hub'ı uyandırır (`Wake`). Hub ayrıca 5 saniyede bir kendiliğinden bakar. Böylece başka bir server örneğinin oluşturduğu job'lar da kaçmaz.
2. Dispatcher her boştaki agent için `ClaimJob` çağırır. Bu sorgu en eski `pending` job'u `running` yapar ve agent'a atar. `FOR UPDATE SKIP LOCKED` sayesinde aynı job iki agent'a gitmez, birden fazla server aynı anda çalışsa bile.
3. `Planner` job'un adımlarını üretir ve agent'a `job_assigned` gönderilir. `.cisid.yml` yoksa ya da geçersizse job `failed` olur ve sıradaki job'a geçilir. GitHub'a geçici olarak ulaşılamıyorsa job kuyruğa geri döner.
4. Agent'tan gelen `job_log` mesajları `job_logs` tablosuna, `job_result` mesajı `jobs` tablosuna yazılır. Sonuç gelince agent tekrar boşa çıkar ve hub sıradaki job'a bakar.

## Planner

`internal/pipeline` job'un adımlarını üretir: önce **clone**, sonra pushlanan commit'teki `.cisid.yml` adımları ([pipeline.md](pipeline.md)). Plan geçici bir sebeple üretilemezse job başarısız sayılmaz. Job kuyruğa geri döner ve o dağıtım turu durur, aynı job hemen tekrar alınmaz.

Webhook'tan gelen değerler (clone URL, branch, SHA) script'in içine yazılmaz. Ortam değişkeni olarak verilir ve script içinde sadece tırnak içinde kullanılır (`"$CISID_CLONE_URL"`). Bu yüzden shell injection'a açık değildir.

## Sahiplik kuralları

- Agent sadece **elindeki** job için log ve sonuç gönderebilir. Başka bir job'a ait mesajlar atılır ve uyarı loglanır.
- `FinishJob` sadece job o agent'ta `running` durumundaysa başarılı olur. Tekrar kuyruğa alınmış ya da bitmiş bir job'a sonuç yazılamaz.
- Aynı adla ikinci bir bağlantı gelirse eskisi kapatılır. Bu, kopup yeniden bağlanan agent'ın yarım açık kalan (half-open) eski bağlantısını temizler.

## Yeniden bağlanma ve kayıp job'lar

Agent her bağlantıda `agent_hello` içinde elindeki job'u bildirir (`running_job_id`). Job'un sonucu henüz gönderilmediyse de o job bildirilir. Hub bu bilgiyle iki şey yapar:

- Veritabanında bu agent'a atanmış görünen **diğer** `running` job'ları tekrar kuyruğa alır (`RequeueAgentJobs`). Bu durum iki şekilde oluşur: atama mesajı agent'a hiç ulaşmamıştır, ya da agent yeniden başlamıştır.
- Bildirilen job bitene kadar agent'a yeni iş vermez.

Server yeniden başlarsa agent bağlantıyı tekrar kurar, çalışan job devam eder ve sonucu yeni server'a teslim edilir (`TestIntegrationJobSurvivesServerRestart`).

Agent bir işi reddederse (`job_rejected`) job tekrar kuyruğa alınır. Agent bir sonuç bildirene ya da yeniden bağlanana kadar yeni iş almaz. Bu, "meşgul agent'a sürekli iş gönderip reddedilme" döngüsünü önler.

## Onaylı teslim

Hub agent'tan gelen her numaralı mesajı işledikten sonra (yazdıktan ya da bilerek attıktan sonra) onaylar. Writer goroutine'i yoğunlukta onayları birleştirir ve en son işlenen `id` için tek bir `ack` gönderir. Agent onaylanmamış mesajları yeni bağlantıda tekrar gönderir. Ayrıntılar [agent.md](agent.md#onaylı-teslim) dosyasında.

## Graceful shutdown

WebSocket kütüphanesinin `Close` metodu, kapanış el sıkışmasını beklerken karşıdan gelen veri mesajlarını **okuyup atar**. Hub bağlantıyı kendisi kapatsaydı, agent'ın o anda gönderdiği log ve sonuçlar kaybolurdu. Bu yüzden kapanış şöyle işler:

1. Hub her agent'a `server_closing` gönderir.
2. Agent o an yazdığı mesajı bitirir, outbox'tan yeni mesaj almayı bırakır ve bağlantıyı **kendisi** kapatır.
3. Hub, agent'ın kapanış çerçevesine kadar gelen her mesajı işler ve onaylar. Gönderilmemiş ya da onaylanmamış mesajlar agent'ın outbox'ında kalır ve yeni server'a iletilir.
4. Bunu yapmayan (eski) bir agent'ın bağlantısı 5 sn sonra zorla kapatılır (`WithDrainTimeout`).

## Canlılık ve lease

Hub her bağlantıya 30 sn'de bir WebSocket ping gönderir. Pong 10 sn içinde gelmezse bağlantı kapatılır ve ölü bağlantılar fark edilir.

Her `running` job'un bir **lease** süresi vardır (`jobs.lease_expires_at`, varsayılan 5 dk, `WithLease`):

- `ClaimJob` job'u lease ile birlikte verir.
- Hub her lease/3 sürede kendisine bağlı agent'ların job'larının lease'ini uzatır (`RenewLeases`).
- Aynı turda lease'i dolmuş job'ları kuyruğa geri alır (`RequeueExpired`) ve hemen dağıtır. Lease'i dolan bir job'un agent'ı bu süre boyunca hiçbir server'a bağlı olmamıştır.
- Bağlantısı kopan agent'ın job'u lease dolana kadar beklenir. Böylece kısa kopmalar ve server yeniden başlatmaları job'u yeniden çalıştırmaz.
- Geri dönen agent hello'da job'unu bildirirse job ona geri bağlanır (`AdoptJob`), lease süresi dolmuş olsa bile. Tek koşul, job'un bu arada başka bir agent'a verilmemiş olmasıdır. Verilmişse eski agent'ın sonucu atılır.

Bütün zamanlar veritabanının saatine göre hesaplanır (`now()`). Server'ların saatleri arasındaki farklar sonucu etkilemez.

## Bilinen sınırlamalar (Faz 2)

- Her log parçası ayrı bir `INSERT` ile yazılır. Yoğun log üreten job'larda toplu yazma (batch) gerekebilir.

## Testler

| Test | Kapsam |
|---|---|
| `hub_test.go` | Mock store ve planner, ham WebSocket istemcisi: job yaşam döngüsü, hello zorunluluğu, yeniden bağlanma, reddetme, sahiplik, plan hatası, çoklu agent, bağlantı değişimi, drain ile ve drain olmadan kapanış |
| `hub_integration_test.go` | Gerçek PostgreSQL ve gerçek agent: başarılı ve başarısız job'lar, job sürerken server'ın yeniden başlaması, kaybolan agent'ın job'unun lease dolunca başka agent'a geçmesi |
| `cmd/server` `TestRunEndToEnd` | Gerçek server, gerçek agent ve imzalı webhook, sonuç ve loglar veritabanında |

```bash
CISID_TEST_DATABASE_URL='postgres://cisid:cisid@localhost:5432/cisid?sslmode=disable' go test ./internal/hub/ ./cmd/server/
```
