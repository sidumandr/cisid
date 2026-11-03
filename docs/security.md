# Güvenlik

Bu belge cisid'in güvenlik önlemlerini tek yerde toplar. Ayrıntılar ilgili belgelerdedir.

## Güven sınırları

| Taraf | Güven | Nasıl doğrulanır |
|---|---|---|
| GitHub, Gitea, Forgejo, Bitbucket (webhook) | Secret'ı bilen gönderici | Gövdenin HMAC-SHA256 imzası |
| GitLab (webhook) | Secret'ı bilen gönderici | `X-Gitlab-Token` başlığı, sabit zamanlı karşılaştırma. GitLab imzalamaz: adres HTTPS olmalı. |
| Agent | Agent token'ını bilen süreç | `Authorization: Bearer`, isteğe bağlı TLS |
| API istemcisi (yazma) | API token'ını bilen | `Authorization: Bearer` |
| API istemcisi (okuma), web arayüzü | `CISID_READ_TOKEN` varsa token sahipleri, yoksa herkes | `Authorization: Bearer` ya da oturum cookie'si |
| Repodaki kod (`.cisid.yml` adımları) | Push yetkisi olan herkes | Container sınırları, ama agent makinesine erişebilir |

Repoya push edebilen biri, o repo için tanımlı secret'ları okuyabilir ve agent makinesinde kod çalıştırabilir. cisid bunu engellemez, etkisini sınırlar.

## Webhook

- İmza sabit zamanlı karşılaştırılır (`hmac.Equal`): GitHub'da `X-Hub-Signature-256`, Gitea'da `X-Gitea-Signature`, Forgejo'da `X-Forgejo-Signature`, Bitbucket'ta `X-Hub-Signature`. GitLab secret'ı `X-Gitlab-Token` başlığında açık gönderir. Bir sağlayıcının webhook adresi yalnızca secret'ı verilince açılır.
- `repository.pushed_at` yalnızca GitHub push'larında vardır; diğer sağlayıcıların push'larında 7 günlük tekrar penceresi uygulanmaz, imzalı gövdenin tekilleştirmesi uygulanır.
- Gövde en fazla 25 MB okunur.
- Aynı imzalı gövde bir kez job oluşturur. Tekilleştirme anahtarı gövdenin SHA-256 özetidir, kayıt job ile aynı transaction'da yazılır. `repository.pushed_at` 7 günden eskiyse istek reddedilir. Kayıtlar 8 gün sonra silinir ([webhook.md](webhook.md#tekrar-replay-koruması)).
- Tüm repolar tek bir webhook secret'ı kullanır.
- Pull request'ler de derlenir. Fork'tan gelenler varsayılan olarak derlenmez; `CISID_BUILD_FORKS=true` ile derlenir ama hiçbir secret almaz ([webhook.md](webhook.md#pull-requestler)).

## Agent bağlantısı

- `CISID_AGENT_TOKEN` tanımlıysa token'sız ya da yanlış token'lı agent `401` alır. Karşılaştırma sabit zamanlıdır. Token en az 16 karakter olmalıdır. Tanımlı değilse server bir uyarı yazar ve her agent'ı kabul eder.
- Server `CISID_TLS_CERT_FILE` ve `CISID_TLS_KEY_FILE` ile TLS 1.2+ sunar. Agent `wss://` ile bağlanır, özel bir CA için `CISID_SERVER_CA_FILE` kullanılır. Agent token'ı yerel olmayan bir adrese `ws://` ile gönderiyorsa uyarır.
- Tüm agent'lar ortak bir token kullanır, agent başına kimlik yoktur. `CISID_AGENT_TOKEN` virgülle birden fazla token alır; böylece token, agent'lar kesilmeden değiştirilir ve eskisi listeden çıkarılarak iptal edilir ([agent.md](agent.md#kimlik-doğrulama-ve-tls)).

## API ve web arayüzü

- Değişiklik yapan uç noktalar (iptal, yeniden çalıştırma, secret'lar) `CISID_API_TOKEN` ister. Token tanımlı değilse bu uç noktalar kapalıdır (`403`).
- Okuma uç noktaları (`/api/jobs`, loglar, canlı akış) ve `/metrics`, `CISID_READ_TOKEN` tanımlıysa okuma ya da API token'ı ister; tanımlı değilse herkese açıktır. Job listesi repo adlarını, commit'leri ve logları gösterir. Tarayıcı `POST /api/login` ile HttpOnly, `SameSite=Strict` bir cookie alır; cookie token'ın özetini taşır ve yalnızca okumaya izin verir ([api.md](api.md#okuma-erişimi)). `/healthz` ve `/readyz` her zaman açıktır.
- `/webhooks/` ve `/api/` IP başına token bucket ile sınırlıdır (`CISID_RATE_LIMIT`, varsayılan 10 istek/sn). Reverse proxy arkasında `CISID_TRUST_PROXY=true` ile istemci adresi `X-Forwarded-For`'dan alınır. Proxy yoksa bu ayarı açmayın, başlık sahte olabilir.
- Web arayüzü `Content-Security-Policy: default-src 'self'` ile sunulur, inline script yoktur. API'den gelen her değer `textContent` ile eklenir. API token'ı tarayıcıda yalnızca `sessionStorage`'da tutulur ([ui.md](ui.md#güvenlik)).

## Secret'lar

- AES-256-GCM ile şifrelenir. Şifreli veri `repo/ad` bilgisine bağlıdır (AAD), başka bir satıra kopyalanan değer çözülemez. Anahtar `CISID_SECRET_KEY`'dir, veritabanında tutulmaz.
- API secret değerlerini hiçbir zaman geri döndürmez, yalnızca adlarını listeler.
- Değerler yalnızca `secrets:` ile isteyen adımlara ortam değişkeni olarak verilir. Bir secret, server tarafında `branches` ile korumalı branch'lere sınırlanabilir; o zaman pull request'lere de verilmez ([secrets.md](secrets.md#secretı-branchlere-sınırlamak)). `when.branch` bir güvenlik sınırı değildir, çünkü `.cisid.yml` derlenen commit'ten okunur.
- Agent, adımların çıktısında secret değerlerini `***` ile maskeler, parçalara bölünmüş değerler dahil. 3 karakterden kısa değerler maskelenmez. Maskeleme bir güvenlik sınırı değildir: değeri dönüştürüp (ör. base64) yazdıran bir adımı durdurmaz ([secrets.md](secrets.md)).

## Clone token'ları

- Sağlayıcının token'ı özel repoları clone'lamak için yalnızca clone adımına secret olarak verilir. Çıktıda maskelenir, `.git/config`'e yazılmaz (git onu `GIT_CONFIG_*` ortam değişkenleriyle tanımlanan bir yardımcıdan okur) ve kullanıcı adımlarına verilmez.
- Token'ı olabildiğince dar yetkiyle oluşturun: okuma ve commit status. Bir sağlayıcının token'ı o sağlayıcıdaki bütün repolar için kullanılır.

## Step container'ları

- Her adım ve servis ayrı bir container'da, `no-new-privileges` ile çalışır. `--privileged` kullanılmaz.
- Bellek (swap dahil), CPU ve süreç sayısı sınırlanabilir. Süreç sınırı varsayılan olarak 4096'dır. Bellek aşımı `out of memory` olarak raporlanır ([runner.md](runner.md)).
- Servisli job'lar kendi Docker ağında çalışır. Workspace volume'u, ağ ve container'lar job bitince silinir.
- Önbellek volume'ları repo başınadır ve job'lar arasında kalır. Repo'ya push edebilen biri önbelleğe dosya bırakabilir; fork PR'ları önbelleği yalnızca okuyabilir ([pipeline.md](pipeline.md#önbellek)).
- Clone adımı repo adresini ve commit'i ortam değişkeni olarak alır, kabuk komutuna gömmez.
- Agent host'un Docker daemon'ına tam erişimle çalışır. Bu root eşdeğeridir: kötü niyetli bir adım Docker API'sine ulaşamasa da container kaçışı ya da çekirdek açığı host'u etkiler. Agent'ları CI'a ayrılmış, yalıtılmış makinelerde ya da VM'lerde çalıştırın ([deployment.md](deployment.md#agent-ve-docker-socket)).
- Kubernetes'te her agent pod'u privileged bir Docker-in-Docker sidecar'ı çalıştırır. Docker yalnızca pod içinde paylaşılan bir Unix socket'i dinler. Agent container'ının kendisi salt okunur dosya sistemiyle ve bütün capability'ler kapalı çalışır. Dahili PostgreSQL'e yalnızca server'ın bağlanmasına izin veren bir NetworkPolicy kurulur ([Helm chart](../deployments/helm/cisid/README.md)).

## Veritabanı ve loglar

- SQL metni hiçbir zaman istek verisiyle birleştirilmez, tüm değerler sorgu parametresi olarak gönderilir.
- Secret değerleri, token'lar ve veritabanı şifresi server ve agent loglarına yazılmaz. Başlangıçta yazılan yapılandırmada veritabanı adresinin şifresi `xxxxx` ile gizlenir, token'lar için yalnızca tanımlı olup olmadıkları yazılır.
- Job logları veritabanında açık metin olarak saklanır (maskelenmiş hâliyle).
- Trace'ler repo, branch, commit, adım adı ve image bilgisi taşır. Secret değerleri, ortam değişkenleri ve adım çıktısı span'lara yazılmaz. Collector adresindeki kullanıcı bilgisi loglarda gizlenir ([tracing.md](tracing.md)).

## Production kontrol listesi

- [ ] `CISID_GITHUB_WEBHOOK_SECRET`, `CISID_AGENT_TOKEN` ve `CISID_API_TOKEN` için uzun, rastgele değerler.
- [ ] Server TLS ile ya da TLS sonlandıran bir reverse proxy arkasında. Agent'lar `wss://` ile bağlanıyor.
- [ ] `CISID_READ_TOKEN` tanımlı ya da server erişim kontrolü olan bir reverse proxy arkasında.
- [ ] `CISID_SECRET_KEY` veritabanı yedeklerinden ayrı saklanıyor.
- [ ] Agent'lar ayrı makinelerde, `CISID_STEP_MEMORY` ve `CISID_STEP_CPUS` tanımlı.
- [ ] Hassas secret'lar `branches` ile korumalı branch'lere sınırlı.

## Bilinen eksikler

- Okuma erişimi tek bir paylaşılan token'la korunur; kullanıcı hesabı ve repoya göre yetki yoktur.
- Kullanıcı ya da rol kavramı yok: API token'ı olan her şeyi yapabilir.
- Agent başına kimlik yok; token değişikliği server'ın yeniden başlatılmasını gerektirir (rotasyon listesi kesintiyi önler).
