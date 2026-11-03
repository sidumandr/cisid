# Trace'ler (OpenTelemetry)

Server ve agent, bir job'un yolculuğunu OpenTelemetry trace'i olarak dışa aktarabilir: webhook'un gelişi, job'un dağıtılması, `.cisid.yml`'ın GitHub'dan okunması, agent'ta servisler ve adımlar, commit status'un raporlanması. Hepsi tek bir trace'te, iki ayrı süreçten gelse de birbirine bağlı görünür. Kod `internal/telemetry` paketindedir.

Varsayılan olarak kapalıdır. Kapalıyken hiçbir span oluşturulmaz ve HTTP katmanına sarmalayıcı eklenmez.

## Açmak

Server'a ve agent'lara bir OTLP/HTTP collector adresi verin:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 go run ./cmd/server
```

| Değişken | Açıklama |
|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Collector'ın temel adresi. Trace'ler `<adres>/v1/traces` yoluna gönderilir. |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Trace'lerin gönderileceği tam adres. Verilirse yukarıdakinin yerine geçer. |
| `OTEL_SDK_DISABLED` | `true` ise adres verilmiş olsa bile trace kapalıdır. |
| `OTEL_SERVICE_NAME` | Servis adı. Varsayılan `cisid-server` ve `cisid-agent`. |
| `OTEL_EXPORTER_OTLP_HEADERS` | Collector'a gönderilecek başlıklar, ör. `authorization=Bearer ...`. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | Örnekleme. Varsayılan `parentbased_always_on`. |
| `OTEL_RESOURCE_ATTRIBUTES` | Ek resource öznitelikleri, ör. `deployment.environment=prod`. |

Bunlar OpenTelemetry'nin standart değişkenleridir. Sertifika ve zaman aşımı gibi diğer `OTEL_EXPORTER_OTLP_*` ayarları da çalışır. Yalnızca OTLP/HTTP (`http/protobuf`) desteklenir. `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` verilirse süreç başlamaz ve nedenini yazar ([ADR 001](adr/001-agent-protokolu-websocket.md)).

Collector'a ulaşılamazsa job'lar etkilenmez, yalnızca span'lar kaybolur. Kapanışta bekleyen span'lar en fazla 5 saniye içinde gönderilir.

Resmi OTLP exporter'ı, yalnızca HTTP kullanılsa da gRPC paketlerini binary'ye ekler. Bu yüzden server ve agent binary'leri yaklaşık 7 MB büyüdü (strip edilmiş: server 14,8 → 22,2 MB, agent 8,4 → 15,5 MB). Kendi exporter'ımızı yazmak yerine bu bedeli kabul ettik.

### Yerelde Jaeger ile denemek

Jaeger, OTLP/HTTP'yi `4318` portunda kabul eder ve arayüzünü `16686` portunda sunar:

```bash
docker run -d --name jaeger -p 16686:16686 -p 4318:4318 jaegertracing/jaeger:2.11.0
```

Server ve agent'ı `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` ile başlatın, bir push gönderin ve `http://localhost:16686` adresinde `cisid-server` servisini açın. Belirli bir job'u bulmak için `cisid.job.id=42` etiketiyle arayın.

Compose stack'te değişken server ve agent'a aktarılır. Collector host'ta çalışıyorsa container'ların içinden `http://host.docker.internal:4318` adresiyle erişilir ([deployment.md](deployment.md)).

## Bir job'un trace'i

```
POST /webhooks/github                         cisid-server
├── dispatch job                              cisid-server
│   ├── HTTP GET   (GitHub: .cisid.yml)       cisid-server
│   └── run job                               cisid-agent
│       ├── service postgres
│       ├── step clone
│       └── step test
└── report commit status   (her durum değişikliğinde)   cisid-server
    └── HTTP POST  (GitHub: statuses)
```

Webhook span'ı job kuyruğa girer girmez biter. Altındaki span'lar dakikalar sonra gelebilir, collector arayüzleri bunları yine aynı ağaçta gösterir.

| Span | Süreç | Öznitelikler |
|---|---|---|
| `POST /webhooks/github` | server | `cisid.repo`, `cisid.branch`, `cisid.commit`, `cisid.job.id`, `github.delivery_id` |
| `GET /api/jobs/{id}` vb. | server | `http.route` ve standart HTTP öznitelikleri |
| `dispatch job` | server | `cisid.job.id`, `cisid.repo`, `cisid.commit`, `cisid.agent`, `cisid.matrix` |
| `HTTP GET`, `HTTP POST` (GitHub) | server | Standart HTTP istemci öznitelikleri |
| `run job` | agent | `cisid.job.id`, `cisid.agent`, `cisid.job.status` |
| `service <ad>` | agent | `cisid.service.name`, `cisid.step.image` |
| `step <ad>` | agent | `cisid.step.index`, `cisid.step.name`, `cisid.step.image`, `cisid.step.exit_code` |
| `report commit status` | server | `cisid.job.id`, `cisid.job.status` |

Başarısız adımlar, planlanamayan job'lar ve GitHub hataları span durumunu `Error` yapar. Başarısız olan job'un `run job` span'ı da `Error` olur. İptal edilen job'lar hata sayılmaz, durumları `cisid.job.status` özniteliğinde görünür.

## Trace bağlamı nasıl taşınır

Bir job, onu kuyruğa koyan isteğin span'ını W3C `traceparent` biçiminde `jobs.trace_parent` sütununda saklar (migration `010_job_trace.sql`). Job dakikalar sonra, belki başka bir server kopyasında dağıtılsa bile `dispatch job` span'ı bu değeri ebeveyn olarak kullanır. Server, agent'a gönderdiği `job_assigned` mesajına `trace_parent` alanını ekler ([agent.md](agent.md#protokol)). Agent'ın span'ları da böylece aynı trace'e bağlanır.

- Matrix job'larının hepsi ilk job'un trace'ini paylaşır.
- Yeniden çalıştırılan bir job, `rerun` isteğinin trace'ine bağlanır.
- Webhook ve API istekleri, gelen bir W3C `traceparent` başlığını sürdürür. GitHub'a yapılan isteklere trace başlığı eklenmez.
- Trace kapalı bir server'dan job alan bir agent, her job için yeni bir trace başlatır.

## Log'larla eşleştirme

Bir span içinde yazılan log satırları `trace_id` ve `span_id` alanlarını taşır:

```
level=INFO msg="job queued" delivery=... job_id=42 ... trace_id=4bf92f3577b34da6a3ce929d0e0e4736 span_id=00f067aa0ba902b7
```

## Trace edilmeyenler

- Veritabanı sorguları ve Docker API çağrıları.
- Agent WebSocket bağlantıları, canlı log akışları (SSE), web arayüzü, `/metrics` ve health check'ler. Bu bağlantılar dakikalarca açık kalır ya da çok sık çağrılır, trace'e bir şey katmaz.
- Adım çıktısı ve ortam değişkenleri. Span'larda secret değeri yer almaz.

## Testler

| Test | Ne doğrulanıyor |
|---|---|
| `internal/telemetry` | Adres yokken kapalı olması, hatalı yapılandırmanın reddi, gerçek bir OTLP/HTTP collector'a dışa aktarım, `traceparent` taşıma, log'lara trace ID eklenmesi |
| `TestRouterTraces` | Hangi isteklerin span aldığı, route'a göre adlandırma, gelen `traceparent`'ın sürdürülmesi |
| `TestWebhookStoresRequestTrace` | Webhook'un trace bağlamını job'a yazması |
| `TestStoreKeepsTraceParent` | Kuyruk, matrix ve rerun'da `trace_parent` |
| `TestHubTracesDispatch` | Dağıtım span'ının job'un trace'ini sürdürmesi, planner'ın span'ı görmesi, agent'a gönderilen `trace_parent` |
| `TestAgentTracesJob` | `run job`, servis ve adım span'ları, başarısız adımın işaretlenmesi |
| `TestReporterTracesReports` | Commit status span'ı |
| `TestRunEndToEnd` | Gerçek server ve agent: tüm span'ların collector'a ulaşması ve tek bir trace oluşturması |

`internal/telemetry/telemetrytest` paketi testler için süreç içinde çalışan bir OTLP/HTTP collector sağlar.
