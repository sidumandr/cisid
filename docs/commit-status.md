# Commit Status

cisid her job'un durumunu derlenen commit'e **commit status** olarak yazar (`internal/status`). Commit'in yanında, pull request'lerde ve branch protection kurallarında `cisid` adıyla görünür. Token'ı verilmiş her sağlayıcıya yazılır; aşağıdaki tablo GitHub'ın durum adlarını gösterir, diğerleri [forges.md](forges.md#commit-status)'dedir.

| Job durumu | GitHub durumu | Açıklama |
|---|---|---|
| `pending` | `pending` | Waiting for an agent |
| `running` | `pending` (GitLab'da `running`) | Running on worker-1 |
| `success` | `success` | Passed in 1m23s |
| `failed` | `failure` | Step "test" failed with exit code 1 · Step "clone" failed with exit code 128 · Failed: no .cisid.yml in repository |
| `canceled` | `error` | Canceled |

Matrix job'larının her biri kendi bağlamını alır, örneğin `cisid (GO_VERSION=1.27)`. Böylece kombinasyonların durumları birbirinin üzerine yazılmaz ve branch protection'da tek tek zorunlu tutulabilir.

Pull request build'leri `cisid/pr` bağlamını kullanır (matrix'te `cisid/pr (GO_VERSION=1.27)`). Aynı commit hem bir branch'e push edilip hem bir PR'da derlendiğinde iki sonuç birbirini ezmez. Merge'den önce testlerin geçmesini şart koşmak için branch protection'da `cisid/pr`'ı zorunlu tutun. Elle başlatılan ve zamanlanmış build'ler de kendi bağlamlarını kullanır: `cisid/manual` ve `cisid/schedule`.

## Kurulum

| Değişken | Açıklama |
|---|---|
| `CISID_GITHUB_TOKEN` (ya da diğer sağlayıcıların token'ı) | Zorunlu. Token'ı olmayan sağlayıcılara status yazılmaz. GitHub fine-grained token'ında **Commit statuses: Read and write** ve **Contents: Read-only** yetkileri gerekir. Classic token'da `repo:status` yeterlidir, `.cisid.yml` okumak için özel repolarda `repo` gerekir. Diğer sağlayıcılar: [forges.md](forges.md). |
| `CISID_PUBLIC_URL` | İsteğe bağlı. Verilirse her status, web arayüzündeki job sayfasına bağlanır (`<url>/jobs/<id>`). |

## Güvenilirlik

- Her job'un sağlayıcıya en son bildirilen durumu `jobs.reported_status` alanında tutulur. Reporter, bu alan gerçek durumdan farklı olan job'ları bulup bildirir.
- Reporter job oluşturulunca, bir agent'a atanınca ve bitince hemen uyandırılır, ayrıca 5 sn'de bir kendisi de kontrol eder.
- **Sağlayıcı geçici olarak erişilemezse** (5xx, rate limit, ağ hatası) o tur durur ve bildirim sonra tekrar denenir. Server yeniden başlasa bile hiçbir durum geçişi kaybolmaz.
- **Kalıcı hatalarda** (yetkisiz token, silinmiş commit) uyarı loglanır ve o durumdan vazgeçilir. Aynı istek sonsuza kadar tekrarlanmaz.
- Bir günden eski job'lar için bildirim yapılmaz. Commit status'lar bu özellikten önce oluşmuş job'lar için de gönderilmez (migration `004`).
- Birden fazla server aynı durumu iki kez gönderebilir. Bunun bir zararı yoktur, son yazılan durum geçerlidir.
- Token'ı olmayan bir sağlayıcının job'ları sessizce atlanır (`cisid_commit_statuses_total{result="skipped"}`).
