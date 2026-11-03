# GitHub Commit Status

cisid her job'un durumunu pushlanan commit'e GitHub **commit status** olarak yazar (`internal/status`). Commit'in yanında, pull request'lerde ve branch protection kurallarında `cisid` adıyla görünür.

| Job durumu | GitHub durumu | Açıklama |
|---|---|---|
| `pending` | `pending` | Waiting for an agent |
| `running` | `pending` | Running on worker-1 |
| `success` | `success` | Passed in 1m23s |
| `failed` | `failure` | Step 2 failed with exit code 1 · Clone failed with exit code 128 · Failed: no .cisid.yml in repository |
| `canceled` | `error` | Canceled |

Adım numaraları `.cisid.yml`'deki sıradır, clone adımı `0` sayılır.

## Kurulum

| Değişken | Açıklama |
|---|---|
| `CISID_GITHUB_TOKEN` | Zorunlu. Token yoksa commit status kapalıdır. Fine-grained token'da **Commit statuses: Read and write** ve **Contents: Read-only** yetkileri gerekir. Classic token'da `repo:status` yeterlidir, `.cisid.yml` okumak için özel repolarda `repo` gerekir. |
| `CISID_PUBLIC_URL` | İsteğe bağlı. Verilirse her status job'a bağlanır (`<url>/api/jobs/<id>`). |

## Güvenilirlik

- Her job'un GitHub'a en son bildirilen durumu `jobs.reported_status` alanında tutulur. Reporter, bu alan gerçek durumdan farklı olan job'ları bulup bildirir.
- Reporter job oluşturulunca, bir agent'a atanınca ve bitince hemen uyandırılır, ayrıca 5 sn'de bir kendisi de kontrol eder.
- **GitHub geçici olarak erişilemezse** (5xx, rate limit, ağ hatası) o tur durur ve bildirim sonra tekrar denenir. Server yeniden başlasa bile hiçbir durum geçişi kaybolmaz.
- **Kalıcı hatalarda** (yetkisiz token, silinmiş commit) uyarı loglanır ve o durumdan vazgeçilir. Aynı istek sonsuza kadar tekrarlanmaz.
- Bir günden eski job'lar için bildirim yapılmaz. Commit status'lar bu özellikten önce oluşmuş job'lar için de gönderilmez (migration `004`).
- Birden fazla server aynı durumu iki kez gönderebilir. GitHub'da bunun bir zararı yoktur, son yazılan durum geçerlidir.
