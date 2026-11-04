# cisid Helm chart

[English](README.md) · **Türkçe**

cisid'i Kubernetes'e kurar:

- **server:** Deployment ve Service. İsteğe bağlı olarak Ingress ve ServiceMonitor.
- **agent:** StatefulSet. Her pod'da adımları çalıştıran bir Docker-in-Docker sidecar'ı vardır.
- **PostgreSQL:** Deneme için tek kopya bir StatefulSet. İsteğe bağlıdır, production'da harici bir veritabanı kullanılır.

Kubernetes 1.29 veya üstü gerekir: Docker-in-Docker, native sidecar olarak çalışır.

## Önce: image'lar

Etiketlenen her sürümde image'lar ve chart `ghcr.io/sidumandr` altına yayımlanır ([release.md](../../../docs/tr/release.md)). Chart'ın varsayılan değerleri bu image'ları kullanır ve yayımlanan chart doğrudan kurulabilir:

```bash
helm install ci oci://ghcr.io/sidumandr/charts/cisid --version 0.1.0 --namespace ci
```

Henüz yayımlanmış bir sürüm yoksa ya da kendi değişikliklerinizi kullanacaksanız, image'ları proje kökünden derleyip kendi registry'nize gönderin:

```bash
docker build --target server -t ghcr.io/<hesap>/cisid-server:0.1.0 .
```

```bash
docker build --target agent -t ghcr.io/<hesap>/cisid-agent:0.1.0 .
```

```bash
docker push ghcr.io/<hesap>/cisid-server:0.1.0 && docker push ghcr.io/<hesap>/cisid-agent:0.1.0
```

Etiket varsayılan olarak chart'ın `appVersion` değeridir (`0.1.0`). Farklıysa `server.image.tag` ve `agent.image.tag` ile verin. Özel registry için `imagePullSecrets` kullanın.

## Kurulum

Agent pod'ları privileged bir container (`hostSocket` modunda bir hostPath volume'u) kullandığı için namespace'in Pod Security seviyesi `privileged` olmalıdır:

```bash
kubectl create namespace ci && kubectl label namespace ci pod-security.kubernetes.io/enforce=privileged
```

```bash
helm install ci deployments/helm/cisid --namespace ci \
  --set server.image.repository=ghcr.io/<hesap>/cisid-server \
  --set agent.image.repository=ghcr.io/<hesap>/cisid-agent \
  --set-string secrets.webhookSecret=$(openssl rand -hex 32) \
  --set-string secrets.agentToken=$(openssl rand -hex 24) \
  --set-string postgresql.auth.password=$(openssl rand -hex 16)
```

Secret'ları `--set-string` ile verin. Yalnızca rakamlardan oluşan bir değer `--set` ile sayıya dönüşür ve şema doğrulamasından geçmez.

Kurulumdan sonra Helm, GitHub webhook adresini ve webhook secret'ını okuma komutunu yazar. Server'ın veritabanına ulaştığını denemek için:

```bash
helm test ci --namespace ci
```

## Değerler

Tamamı ve açıklamaları [values.yaml](values.yaml)'dadır. `values.schema.json` hatalı yazılmış anahtarları ve yanlış türleri kurulumda reddeder.

| Değer | Varsayılan | Açıklama |
|---|---|---|
| `secrets.webhookSecret` | — | GitHub webhook secret'ı; `/webhooks/github`'ı açar |
| `secrets.agentToken` | — | **Zorunlu.** Agent token'ı, en az 16 karakter |
| `secrets.apiToken` | — | İptal, yeniden çalıştırma ve secret uç noktalarını açar |
| `secrets.readToken` | — | Job'ları, logları ve metrikleri okumak için token ister. Yoksa server'a ulaşan herkes okur. |
| `secrets.secretKey` | yok | Repo secret'larını ve bildirimlerin nereye gittiğini şifreleyen anahtar (base64, 32 byte); ikisini de açar. Bir kopyasını ayrıca saklayın. |
| `secrets.githubToken` | — | Özel repolar, rate limit ve commit status için |
| `secrets.giteaToken`, `secrets.giteaWebhookSecret` | — | Gitea'nın token'ı ve webhook secret'ı (`server.giteaURL` ile) |
| `secrets.forgejoToken`, `secrets.forgejoWebhookSecret` | — | Forgejo'nun token'ı ve webhook secret'ı (`server.forgejoURL` ile) |
| `secrets.gitlabToken`, `secrets.gitlabWebhookSecret` | — | GitLab'ın token'ı ve webhook secret'ı |
| `secrets.bitbucketToken`, `secrets.bitbucketWebhookSecret` | — | Bitbucket Cloud'un token'ı ve webhook secret'ı |
| `secrets.oauthClientSecrets.<sağlayıcı>` | — | Sağlayıcıyla giriş için OAuth uygulamasının client secret'ı (`github`, `gitea`, `forgejo`, `gitlab`, `bitbucket`) |
| `secrets.adminPassword` | — | İlk açılışta `server.adminUser`'ı admin olarak oluşturur, en az 10 karakter ([hesaplar](../../../docs/tr/accounts.md)) |
| `secrets.smtpPassword` | yok | `server.smtp.username`'in şifresi |
| `secrets.existingSecret` | — | Yukarıdakileri sizin yönettiğiniz bir Secret'tan okur (aşağıya bakın) |
| `server.replicaCount` | `1` | Server kopyası |
| `server.giteaURL`, `server.forgejoURL` | — | Gitea ve Forgejo sunucularının adresi; verilince açılırlar |
| `server.gitlabURL` | GitLab.com | Kendi GitLab kurulumunuzun adresi |
| `server.readAccess` | — | `public`: herkes okur; `signed-in`: okumak giriş ya da token ister. Boşsa read token varken `signed-in`, yokken `public`. |
| `server.sessionTTL` | `7d` | Bir girişin süresi |
| `server.adminUser` | `admin` | `secrets.adminPassword`'ün oluşturduğu hesap |
| `server.oauthClientIDs.<sağlayıcı>` | — | OAuth uygulamasının client id'si; secret'ı ve `server.publicURL` (ya da ingress) gerekir |
| `server.oauthSignupRole` | — | `viewer` ya da `maintainer`: hesabı olmayan sağlayıcı kullanıcılarına ilk girişte hesap açılır |
| `agent.labels` | `[]` | Bu agent'ların sunduğu etiketler (`CISID_AGENT_LABELS`); farklı etiketler için chart'ı her agent türü için ayrı kurun ([pipeline](../../../docs/tr/pipeline.md#etiketler)) |
| `server.artifactMaxSize` | `100MB` | Server'ın kabul ettiği en büyük artifact; artifact'lar PostgreSQL'de saklanır ([pipeline](../../../docs/tr/pipeline.md#artifactlar)) |
| `server.publicBadges` | `false` | Okumak giriş istese de build rozetlerini herkese açar ([bildirimler](../../../docs/tr/notifications.md#rozetler)) |
| `server.smtp.host`, `.port`, `.username`, `.from`, `.tls` | yok, `0` (587, `tls` ile 465), yok, yok, `starttls` | E-posta bildirimleri için SMTP sunucusu; host ile `from` zorunludur. Şifre `secrets.smtpPassword`'e yazılır ([bildirimler](../../../docs/tr/notifications.md#e-posta)). |
| `server.pollInterval` | `1m` | İzlenen branch'lerin yoklanma aralığı; `0` polling'i kapatır ([polling](../../../docs/tr/polling.md)) |
| `server.publicURL` | ilk ingress host'u | Commit status bağlantıları için dış adres |
| `server.trustProxy` | ingress varsa `true` | İstemci IP'sini `X-Forwarded-For`'dan alır |
| `server.tls.secretName` | — | Server için HTTPS, agent'lar için `wss://` |
| `server.buildForks` | `false` | Fork'lardan gelen pull request'leri de derler, secret vermeden |
| `server.approveForks` | `false` | `buildForks` ile birlikte: her fork build'i çalışmadan önce bir maintainer onaylar ([webhook](../../../docs/tr/webhook.md#pull-requestler)) |
| `server.logRetention` | `90d` | Biten job'ların loglarının saklanma süresi, `0` sonsuza dek saklar |
| `agent.replicaCount` | `1` | Agent sayısı. Her agent aynı anda bir job çalıştırır. |
| `agent.stepMemory`, `agent.stepCPUs`, `agent.stepPids` | —, —, `4096` | Adım ve servis container'larının sınırları |
| `agent.docker.mode` | `dind` | `dind` ya da `hostSocket` |
| `agent.docker.dind.persistence.enabled` | `false` | Çekilen image'ları ve job önbelleklerini agent başına bir volume'da saklar |
| `postgresql.enabled` | `true` | Dahili PostgreSQL. Kapalıysa `database.url` gerekir. |
| `postgresql.auth.password` | — | Dahili PostgreSQL'in şifresi. Yalnızca harf, rakam ve `. _ ~ -` içerebilir. |
| `ingress.enabled`, `ingress.hosts`, `ingress.tls` | kapalı | Webhook ve arayüz için dış erişim |
| `tracing.otlpEndpoint` | — | OTLP/HTTP collector ([tracing.md](../../../docs/tr/tracing.md)) |
| `metrics.serviceMonitor.enabled` | `false` | Prometheus Operator için ServiceMonitor |
| `metrics.serviceMonitor.authorize` | `false` | Metrikleri okuma token'ıyla çeker; `secrets.readToken` varsa açın |

## Var olan bir Secret

Secret'ları bir secrets operator'ı ya da GitOps aracıyla yönetiyorsanız `secrets.existingSecret` ile adını verin. Chart o zaman kendi Secret'ını oluşturmaz ve şu anahtarları okur:

| Anahtar | |
|---|---|
| `webhook-secret` | İsteğe bağlı |
| `agent-token` | Zorunlu |
| `postgres-password` | Dahili PostgreSQL ile zorunlu |
| `database-url` | Harici veritabanıyla zorunlu (`postgres://...`) |
| `api-token`, `read-token`, `secret-key`, `github-token`, `gitea-token`, `gitea-webhook-secret`, `forgejo-token`, `forgejo-webhook-secret`, `gitlab-token`, `gitlab-webhook-secret`, `bitbucket-token`, `bitbucket-webhook-secret`, `admin-password`, `smtp-password` | İsteğe bağlı |

Agent token'ını değiştirmek için anahtara önce `yeni,eski` yazın: server ikisini de kabul eder, agent'lar ilkini gönderir. Bütün agent'lar yeniden başladıktan sonra yalnızca `yeni`yi bırakın.

## Agent'lar Docker'a nasıl ulaşır

**`dind` (varsayılan):** Her agent pod'unda bir `docker:dind` sidecar'ı çalışır. Agent ve adımlar yalnızca bu Docker'ı görür. Docker yalnızca pod içinde paylaşılan bir Unix socket'i dinler, TCP portu açmaz. Sidecar native sidecar olduğu için agent'tan önce başlar ve ondan sonra durur. Böylece kapanışta iptal edilen job'un container'ları temizlenebilir.

Sidecar `privileged` çalışır. Bir container kaçışı node'u etkileyebilir. Agent'ları CI'a ayrılmış node'larda çalıştırın:

```yaml
agent:
  nodeSelector:
    workload: ci
  tolerations:
    - key: workload
      value: ci
      effect: NoSchedule
```

Docker'ın verileri (image'lar, adımların container'ları) varsayılan olarak pod'un geçici deposunda, yani node'un diskinde tutulur ve pod yeniden başladığında silinir. `agent.docker.dind.resources` ile `ephemeral-storage` sınırı verebilirsiniz. `agent.docker.dind.persistence.enabled=true`, her agent'a image'ların saklandığı bir PersistentVolume verir.

**`hostSocket`:** Agent, node'un Docker socket'ini kullanır. Yalnızca Docker Engine çalıştıran node'larda işe yarar (çoğu cluster containerd kullanır). Adımlar o node'daki bütün container'ları yönetebilir.

Agent'lar StatefulSet olduğu için her biri pod adını (`ci-cisid-agent-0` gibi) agent adı olarak kullanır. Yeniden başlayan bir agent aynı adla bağlanır ve server onun yarım kalan job'unu lease süresini beklemeden kuyruğa geri koyar.

## TLS

Cluster içinde agent'lar token'ı varsayılan olarak şifresiz `ws://` ile gönderir ve açılışta bunu uyarı olarak loglar. TLS için bir `kubernetes.io/tls` Secret'ı verin. Sertifika server Service'inin adını kapsamalıdır: `<fullname>-server.<namespace>.svc`. Kurulumdan sonraki notlar adın tamamını yazar. cert-manager ile:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: cisid-server
  namespace: ci
spec:
  secretName: cisid-server-tls
  dnsNames:
    - ci-cisid-server.ci.svc
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
```

```bash
helm upgrade ci deployments/helm/cisid --namespace ci --reuse-values --set server.tls.secretName=cisid-server-tls
```

Agent'lar Secret'ın yalnızca `ca.crt` anahtarını görür, private key'i görmez. Kendinden imzalı bir sertifikada `ca.crt` yoksa `server.tls.caKey=tls.crt` verin. Ingress kullanıyorsanız controller'a backend'in HTTPS olduğunu söyleyin. ingress-nginx için bu annotation `nginx.ingress.kubernetes.io/backend-protocol: HTTPS` şeklindedir.

## Veritabanı

Dahili PostgreSQL tek kopyadır, yedeklenmez ve yüksek erişilebilirlik sağlamaz. Denemek için uygundur. Production'da yönetilen ya da replikalı bir veritabanı kullanın:

```bash
--set postgresql.enabled=false --set-string database.url='postgres://cisid:...@db.example.com:5432/cisid?sslmode=require'
```

Dahili PostgreSQL'e yalnızca server pod'larının bağlanmasına izin veren bir NetworkPolicy kurulur. Adımlar da cluster içinde çalıştığı için bu önemlidir. NetworkPolicy'yi uygulayan bir CNI yoksa bu kural etkisizdir.

## Ingress

```yaml
ingress:
  enabled: true
  className: nginx
  hosts: [ci.example.com]
  tls:
    - secretName: ci-example-com-tls
      hosts: [ci.example.com]
```

Ingress açıkken `server.publicURL` ilk host'tan türetilir (`https://ci.example.com`) ve server istemci IP'sini `X-Forwarded-For`'dan alır. Böylece rate limit, ingress controller'ın değil istemcinin adresine göre uygulanır. GitHub webhook adresi `https://ci.example.com/webhooks/github` olur.

## Yükseltme ve kaldırma

- Server güncellenirken önce yeni pod hazır olur, sonra eskisi kapanır. Bağlı agent'lar yeni server'a yeniden bağlanır ve job'lar kesilmez.
- Agent'lar sırayla güncellenir. Kapanan bir agent'ın üzerindeki job **iptal edilir** (`canceled`). Gerekirse yeniden çalıştırın.
- `helm uninstall`, StatefulSet'lerin PersistentVolumeClaim'lerini (veritabanı, image önbelleği) silmez. Verileri silmek için onları ayrıca silin.

## Doğrulama

Chart, [charttest](../charttest) modülündeki testlerle cluster olmadan doğrulanır:

```bash
cd deployments/helm/charttest && go test ./...
```

Testler chart'ı Helm SDK ile render eder ve `helm lint` kurallarını çalıştırır. Her nesneyi API server gibi, alan adlarına büyük-küçük harf duyarlı ve bilinmeyen alanları hata sayarak Kubernetes tipine çevirir. Nesneler arası bağlantıları da denetler: Service seçicileri, Secret anahtarları, volume'lar, probe portları, `$(VAR)` sırası, Ingress ve NetworkPolicy hedefleri. cisid'in kendi CI'ı bu testleri her push'ta çalıştırır.

Chart henüz gerçek bir cluster'a kurulmadı. Pod'ların güvenlik ayarları Docker'da denendi: salt okunur dosya sistemi, bütün capability'ler kapalı, PostgreSQL için uid 70 ve server için uid 10001. Bu ayarlarla `hostSocket` modundaki bir agent, TLS üzerinden cisid'in kendi CI'ını başarıyla çalıştırdı.

Docker-in-Docker sidecar'ı da chart'taki ayarlarla Docker'da denendi. Agent, pod'daki gibi sidecar'ın ağını paylaştı. Sidecar'daki Docker yalnızca Unix socket'ini dinledi, TCP portu açmadı. cisid'in CI'ı sidecar içinde geçti: yarış dedektörüyle testler, job ağındaki PostgreSQL servisi ve chart testleri. Job bitince sidecar'da container, ağ ya da volume kalmadı. Job sürerken durdurulan agent job'u `canceled` olarak raporladı. Container'larını hâlâ çalışan sidecar üzerinden temizledi ve 16 saniyede çıktı (süre sınırı 60 saniye).
