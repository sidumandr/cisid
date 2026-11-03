# Git sağlayıcıları

cisid beş sağlayıcıdaki repoları derler: **GitHub**, **Gitea**, **Forgejo**, **GitLab** (GitLab.com ya da kendi kurulumunuz) ve **Bitbucket Cloud**. Her sağlayıcının istemcisi kendi paketindedir (`internal/github`, `internal/gitea`, `internal/gitlab`, `internal/bitbucket`). Ortak arayüz `internal/forge`'dadır.

Her sağlayıcı için üç şey ayarlanabilir, hepsi isteğe bağlıdır:

| Ayar | Ne işe yarar |
|---|---|
| Adres | Gitea ve Forgejo'nun adresi verilince açılırlar. GitHub, GitLab.com ve Bitbucket Cloud her zaman açıktır. |
| Token | Özel repoları okumak ve clone'lamak, rate limit'i yükseltmek ve commit status yazmak için. |
| Webhook secret | Verilirse o sağlayıcının webhook adresi açılır: `/webhooks/<sağlayıcı>`. Verilmezse build'ler elle ya da zamanlamayla başlatılır. |

## Repo adları

Bir repo cisid'de **repo anahtarı** ile anılır: GitHub'da `owner/name`, diğerlerinde başına sağlayıcının adı gelir.

| Sağlayıcı | Örnek anahtar |
|---|---|
| GitHub | `sidumandr/cisid` |
| Gitea | `gitea:office/app` |
| Forgejo | `forgejo:office/app` |
| GitLab | `gitlab:group/subgroup/project` (en fazla 20 seviye) |
| Bitbucket | `bitbucket:workspace/repo` |

Anahtar job'larda (`repo`), adımlarda (`CISID_REPO`), secret'larda, zamanlamalarda ve önbellekte aynıdır. API ve arayüz adreslerinde son parça `{repo}`, geri kalanı `{owner}` olur; `{owner}` içindeki `:` ve `/` URL'de kodlanır:

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' \
  "http://localhost:8080/api/repos/gitlab:group%2Fsubgroup/project/builds"
```

GitHub anahtarları önekli değildir, bu yüzden cisid'in eski sürümlerinde oluşturulan job'lar, secret'lar ve zamanlamalar olduğu gibi çalışmaya devam eder.

## GitHub

| Değişken | Açıklama |
|---|---|
| `CISID_GITHUB_TOKEN` | Fine-grained token: **Contents: Read-only** ve **Commit statuses: Read and write** |
| `CISID_GITHUB_WEBHOOK_SECRET` | `/webhooks/github`'ı açar |
| `CISID_GITHUB_API_URL` | GitHub Enterprise Server için, ör. `https://ghe.example.com/api/v3` |

Webhook: Repo → **Settings → Webhooks → Add webhook**, içerik tipi `application/json`, olaylar *Pushes* ve *Pull requests*. Ayrıntılar: [webhook.md](webhook.md).

## Gitea ve Forgejo

Forgejo, Gitea'nın API'sini kullanır; ikisi için aynı istemci çalışır. İkisi aynı anda da kullanılabilir.

| Değişken | Açıklama |
|---|---|
| `CISID_GITEA_URL` / `CISID_FORGEJO_URL` | Sunucunun web adresi, ör. `https://git.example.com`. Verilmezse sağlayıcı kapalıdır. |
| `CISID_GITEA_TOKEN` / `CISID_FORGEJO_TOKEN` | Erişim token'ı: `read:repository`, commit status için `write:repository` |
| `CISID_GITEA_WEBHOOK_SECRET` / `CISID_FORGEJO_WEBHOOK_SECRET` | `/webhooks/gitea` ya da `/webhooks/forgejo`'yu açar |

Webhook: Repo → **Settings → Webhooks → Add webhook → Gitea** (Forgejo'da *Forgejo*). Target URL `https://<cisid>/webhooks/gitea`, HTTP method `POST`, içerik tipi `application/json`, secret yukarıdaki değer. Tetikleyiciler: *Push* ve *Pull request* olayları (*Custom events* altında *Push*, *Pull Request* ve *Pull Request Synchronized*). İmza `X-Gitea-Signature` (Forgejo'da `X-Forgejo-Signature`) başlığında gövdenin HMAC-SHA256'sıdır.

## GitLab

| Değişken | Açıklama |
|---|---|
| `CISID_GITLAB_URL` | Kendi GitLab kurulumunuzun adresi. Verilmezse `https://gitlab.com`. |
| `CISID_GITLAB_TOKEN` | Personal, group ya da project access token, `api` kapsamıyla (yalnızca okuma için `read_api` ve `read_repository` yeter, ama commit status `api` ister) |
| `CISID_GITLAB_WEBHOOK_SECRET` | `/webhooks/gitlab`'ı açar |

Webhook: Proje → **Settings → Webhooks → Add new webhook**. URL `https://<cisid>/webhooks/gitlab`, *Secret token* yukarıdaki değer, tetikleyiciler *Push events* ve *Merge request events*.

- GitLab webhook'ları imzalamaz; secret'ı `X-Gitlab-Token` başlığında olduğu gibi gönderir. Bu yüzden webhook adresine **HTTPS** üzerinden ulaşılmalıdır, yoksa secret yolda okunabilir.
- Merge request'ler açılınca, yeniden açılınca ve yeni commit gelince derlenir; başlık gibi değişiklikler derleme başlatmaz. Kapanınca ya da merge edilince bitmemiş job'ları iptal edilir.
- Fork'tan gelen merge request'ler `CISID_BUILD_FORKS=true` ile derlenir. Commit, hedef projenin `refs/merge-requests/<iid>/head` ref'inden çekilir.

## Bitbucket Cloud

| Değişken | Açıklama |
|---|---|
| `CISID_BITBUCKET_TOKEN` | Repository, project ya da workspace **access token**'ı: *Repositories: Read*, commit status için *Repositories: Write* |
| `CISID_BITBUCKET_WEBHOOK_SECRET` | `/webhooks/bitbucket`'ı açar |

Webhook: Repo → **Repository settings → Webhooks → Add webhook**. URL `https://<cisid>/webhooks/bitbucket`, *Secret* yukarıdaki değer, tetikleyiciler *Repository: Push* ve *Pull request: Created, Updated, Merged, Declined*.

- İmza `X-Hub-Signature` başlığında (`sha256=<hex>`) gelir.
- Bir push birden fazla branch'i güncellediyse her branch için ayrı bir job açılır. Yanıtta hepsi `job_ids` olarak listelenir.
- Bitbucket pull request olaylarında commit'i kısaltılmış hash ile yazar; cisid tam hash'i API'den alır. Bitbucket o an erişilemezse webhook `503` döner ve Bitbucket teslimi daha sonra tekrar deneyebilir.
- Fork'tan gelen pull request'ler Bitbucket'ta derlenmez: fork'un commit'leri ana repoda bulunmaz.
- Commit status bir bağlantı ister. `CISID_PUBLIC_URL` verilmezse status repo sayfasına bağlanır.
- Bitbucket Data Center (kendi sunucunuza kurulan Bitbucket) desteklenmez.

## Özel repolar

Token verilmiş bir sağlayıcının repoları clone adımında o token'la çekilir. Token yalnızca clone adımına secret olarak verilir, çıktıda `***` ile maskelenir ve `.git/config`'e yazılmaz: git kimlik bilgilerini ortam değişkenlerinden okuyan bir yardımcıyla (`GIT_CONFIG_*`) alır. Kullanıcı adımları token'ı görmez. Ayrıntılar: [pipeline.md](pipeline.md#çalışma-şekli).

## Commit status

Token'ı olan her sağlayıcıya job'ların durumu commit status olarak yazılır, token'ı olmayanlar atlanır. Her sağlayıcının durum adları farklıdır:

| Job | GitHub | Gitea / Forgejo | GitLab | Bitbucket |
|---|---|---|---|---|
| `pending` | `pending` | `pending` | `pending` | `INPROGRESS` |
| `running` | `pending` | `pending` | `running` | `INPROGRESS` |
| `success` | `success` | `success` | `success` | `SUCCESSFUL` |
| `failed` | `failure` | `failure` | `failed` | `FAILED` |
| `canceled` | `error` | `error` | `canceled` | `STOPPED` |

Ayrıntılar: [commit-status.md](commit-status.md).
