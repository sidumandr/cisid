# Dağıtım

cisid iki binary'den oluşur: `cisid-server` (API, hub, web arayüzü) ve `cisid-agent` (job'ları Docker'da çalıştıran worker). İkisi de tek bir `Dockerfile` ile derlenir. Tek makinede docker compose, Kubernetes'te Helm chart'ı kullanılır ([Kubernetes](#kubernetes)).

## Image'lar

```bash
docker build --target server -t cisid-server .
```

```bash
docker build --target agent -t cisid-agent .
```

| Image | Taban | Kullanıcı | Not |
|---|---|---|---|
| `server` | `alpine:3.22` | uid 10001 | Ayrıcalık gerektirmez, `8080` portunu dinler |
| `agent` | `alpine:3.22` | root | Host'un Docker socket'ine erişmek için |

Binary'ler `CGO_ENABLED=0` ile statik derlenir. Image'larda Go toolchain ve kaynak kod yer almaz.

## docker compose ile tüm stack

`compose.stack.yaml` PostgreSQL, server ve bir agent başlatır. PostgreSQL tanımını `compose.yaml`'dan alır. `compose.yaml` tek başına yalnızca geliştirme için PostgreSQL'i çalıştırır ve hiçbir değişken istemez.

Proje kökünde bir `.env` dosyası oluşturun (git'e eklemeyin):

```
CISID_GITHUB_WEBHOOK_SECRET=uzun-rastgele-bir-değer
CISID_AGENT_TOKEN=en-az-16-karakter-rastgele-token
CISID_API_TOKEN=en-az-16-karakter-yazma-token
CISID_SECRET_KEY=base64-32-byte-anahtar
CISID_GITHUB_TOKEN=commit-status-için-github-token
CISID_PUBLIC_URL=https://ci.example.com
```

`CISID_GITHUB_WEBHOOK_SECRET` ve `CISID_AGENT_TOKEN` zorunludur, eksikse compose başlamaz. Diğerleri isteğe bağlıdır, anlamları için [server.md](server.md) ve [secrets.md](secrets.md). Her komutta `-f` yazmamak için `.env`'e `COMPOSE_FILE=compose.stack.yaml` ekleyebilirsiniz.

```bash
docker compose -f compose.stack.yaml up -d --build --wait
```

Server `http://localhost:8080` adresinde açılır (`CISID_HTTP_PORT` ile değiştirilir). GitHub webhook adresi `/webhooks/github` yoludur ([webhook.md](webhook.md)). `.env`'deki `OTEL_EXPORTER_OTLP_ENDPOINT` server'a ve agent'a aktarılır ve trace'leri açar ([tracing.md](tracing.md)).

Daha fazla agent:

```bash
docker compose -f compose.stack.yaml up -d --scale agent=3
```

Her agent kendi container hostname'ini ad olarak kullanır. Sabit bir ad isterseniz `CISID_AGENT_NAME` verin, ama o zaman `--scale` kullanmayın: aynı adla bağlanan ikinci agent ilkinin bağlantısını devralır.

Durdurmak (veritabanı volume'u korunur):

```bash
docker compose -f compose.stack.yaml down
```

### Agent ve Docker socket

Agent, step ve service container'larını host'un Docker daemon'ında başlatır (`/var/run/docker.sock` bağlanır). Bu, agent container'ına host üzerinde root eşdeğeri yetki verir. Agent'ı yalnızca CI için ayrılmış bir makinede çalıştırın ve güvenmediğiniz repoların job'larını bu makinede çalıştırmayın. Container sınırları için [runner.md](runner.md).

Step'ler agent container'ının içinde değil host daemon'ında çalıştığı için workspace bir Docker volume'udur, agent'ın dosya sistemi kullanılmaz.

### TLS

Compose stack'te agent server'a compose'un özel ağı üzerinden `ws://server:8080` ile bağlanır ve token'ı şifresiz gönderdiği için başlangıçta bir uyarı yazar. Agent'lar başka makinelerdeyse server'ı TLS ile çalıştırın (`CISID_TLS_CERT_FILE`, `CISID_TLS_KEY_FILE`) ya da bir reverse proxy arkasına koyun ve agent'ları `wss://` ile bağlayın ([agent.md](agent.md)).

## Production notları

- Server durumsuzdur: tüm durum PostgreSQL'dedir. Server yeniden başlatıldığında agent'lar yeniden bağlanır ve çalışan job'lar kaldıkları yerden devam eder ([hub.md](hub.md)).
- Tasarım birden fazla server kopyasına uygundur: job'lar `FOR UPDATE SKIP LOCKED` ile dağıtılır, sahiplik lease'lerle veritabanında tutulur ve log akışı PostgreSQL `LISTEN/NOTIFY` ile tüm kopyalara ulaşır. Çok kopyalı kurulum henüz uçtan uca test edilmedi.
- Veritabanı yedeğini düzenli alın. Secret'lar `CISID_SECRET_KEY` olmadan çözülemez, anahtarı ayrı saklayın.
- Server internete açıksa `CISID_READ_TOKEN` tanımlayın; job'lar, loglar ve `/metrics` o zaman token ister ([api.md](api.md#okuma-erişimi)). `/healthz` ve `/readyz` açık kalır.

## Kubernetes

[deployments/helm/cisid](../deployments/helm/cisid/README.md) chart'ı server'ı bir Deployment olarak, agent'ları ise Docker-in-Docker sidecar'lı bir StatefulSet olarak kurar. Deneme için tek kopya bir PostgreSQL de kurabilir. Kurulum, TLS, harici veritabanı ve güvenlik notları chart'ın README'sindedir.
