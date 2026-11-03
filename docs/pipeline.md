# Pipeline (`.cisid.yml`)

Her push için cisid önce repoyu clone'lar, sonra reponun kökündeki `.cisid.yml` dosyasında tanımlı adımları sırayla çalıştırır.

## Örnek

```yaml
steps:
  - name: lint
    image: golang:1.27
    commands:
      - go vet ./...
    environment:
      CGO_ENABLED: 0
    timeout: 5m

  - name: test
    image: golang:1.27
    commands:
      - go test ./...
```

cisid kendi testlerini de bu şekilde çalıştırır: [/.cisid.yml](../.cisid.yml).

## Alanlar

| Alan | Zorunlu | Açıklama |
|---|---|---|
| `steps` | evet | 1 ile 50 arası adım, yazıldıkları sırayla çalışır |
| `services` | hayır | Adımlardan önce başlatılan en fazla 10 servis ([Servisler](#servisler)) |
| `steps[].name` | evet | Harf, rakam, `.`, `_`, `-`. En fazla 63 karakter, benzersiz olmalı. `clone` adı ayrılmıştır. |
| `steps[].image` | evet | Adımın çalışacağı Docker image'ı. `/bin/sh` içermeli. |
| `steps[].commands` | evet | Shell komutları |
| `steps[].environment` | hayır | Ortam değişkenleri. Sayı ve bool değerler string'e çevrilir. cisid'in kendi tanımladığı değişkenler (aşağıdaki tablo) ayrılmıştır. |
| `steps[].timeout` | hayır | Go süre formatında (`90s`, `10m`, `1h30m`). Varsayılan `1h`, en fazla `24h`. |
| `steps[].secrets` | hayır | Adıma ortam değişkeni olarak verilecek repo secret'ları, loglarda maskelenir ([secrets.md](secrets.md)) |
| `steps[].when.branch` | hayır | Adımı sadece bu branch'lere yapılan push'larda çalıştırır. Tek değer ya da liste olabilir (bkz. [Koşullar](#koşullar)). |

Bilinmeyen alanlar hatadır. Örneğin `command:` yazılırsa job hemen `failed` olur ve hata mesajı yazım hatasını gösterir. Bütün sorunlar tek mesajda listelenir.

## Koşullar

```yaml
steps:
  - name: test
    image: golang:1.27
    commands: [go test ./...]

  - name: deploy
    image: alpine:3.22
    commands: [./deploy.sh]
    when:
      branch: [main, "release/*"]
```

`when.branch` desenleri branch adının **tamamıyla** eşleşir:

| Desen | Eşleşir | Eşleşmez |
|---|---|---|
| `main` | `main` | `mainline` |
| `release/*` | `release/1.0` | `release/1.0/rc1` (`*` tek bir parçada kalır) |
| `hotfix/**` | `hotfix/a/b` | `hot/fix` |
| `v?.x` | `v1.x` | `v10.x` |

Eşleşmeyen adımlar job'a hiç eklenmez, sonraki adımlar normal sırayla çalışır. `when` altında şimdilik sadece `branch` desteklenir. Başka bir anahtar (ör. `event`) yazılırsa dosya geçersiz sayılır.

## Servisler

```yaml
services:
  - name: postgres
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: test

steps:
  - name: test
    image: golang:1.27
    environment:
      DATABASE_URL: postgres://postgres:test@postgres:5432/postgres?sslmode=disable
    commands:
      - go test ./...
```

- Servisler job'un ilk adımından önce, her job için oluşturulan ayrı bir Docker ağında başlatılır. Adımlar da bu ağda çalışır ve servise **adıyla** ulaşır (`postgres:5432`).
- Servis adı bir host adı olmalıdır: küçük harf, rakam ve `-`, en fazla 63 karakter.
- `environment` ortam değişkenlerini, `command` ise image'ın varsayılan komutunu değiştirir (ör. `[redis-server, --save, ""]`).
- Servisin hazır olması beklenmez. Hemen bağlanan bir adım önce beklemelidir, örneğin: `until pg_isready -h postgres; do sleep 1; done`.
- Job bitince servisler, ağ ve workspace silinir, job iptal edilse ya da başarısız olsa bile.
- Bir servis başlatılamazsa (ör. image bulunamadı) hiçbir adım çalışmaz. Job `failed` olur, hata mesajında servisin adı yazar.
- Servislerin çıktısı job loglarına eklenmez. Servisler secret alamaz.

cisid'in kendi CI'ı da veritabanı testlerini bir postgres servisine karşı çalıştırır: [/.cisid.yml](../.cisid.yml).

## Çalışma şekli

- **Clone:** İlk adım her zaman `clone`'dur. Pushlanan commit `alpine/git:2.52.0` ile `/workspace` altına çekilir (`--depth=1`). Branch adı yerel branch olarak ayarlanır.
- **Workspace:** Bütün adımlar aynı job volume'unu `/workspace` altında paylaşır ve orada çalışır. Bir adımın ürettiği dosyalar sonraki adımlarda görünür.
- **Komutlar:** Bir adımın komutları tek bir `/bin/sh` script'i olarak çalışır (`set -ex`). İlk hatalı komutta adım durur ve her komut çalışmadan önce stderr'a yazılır.
- **Başarısızlık:** Bir adım başarısız olursa sonraki adımlar çalışmaz.
- **Ortam değişkenleri:** Her adımda şunlar tanımlıdır:

| Değişken | Değer |
|---|---|
| `CI`, `CISID` | `true` |
| `CISID_JOB_ID` | Job numarası |
| `CISID_STEP` | Adım adı |
| `CISID_REPO` | `owner/name` |
| `CISID_BRANCH` | Pushlanan branch |
| `CISID_COMMIT_SHA` | Pushlanan commit |

## Dosyanın okunması

Server, job'u bir agent'a vermeden hemen önce `.cisid.yml`'yi **pushlanan commit'ten** GitHub API ile okur (`internal/github`). Agent dosyayla hiç uğraşmaz.

| Durum | Sonuç |
|---|---|
| Dosya yok | Job `failed`: `no .cisid.yml in repository` |
| Dosya geçersiz | Job `failed`, bütün hatalar `error` alanında |
| GitHub'a ulaşılamıyor, 5xx ya da rate limit | Job **başarısız sayılmaz**, kuyruğa geri döner ve sonraki turda (en geç 5 sn) tekrar denenir |
| Server kapanırken okuma yarıda kaldı | Job kuyruğa geri döner |

Herkese açık repolar token olmadan okunur. Ancak GitHub kimliksiz isteklerde IP başına saatte 60 istek sınırı uygular. Özel repolar ya da yoğun kullanım için token verilmelidir:

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_GITHUB_TOKEN` | — | Contents okuma yetkili token. Loglarda sadece `github_token_set=true` görünür. |
| `CISID_GITHUB_API_URL` | `https://api.github.com` | GitHub Enterprise Server için `https://<host>/api/v3` |

Dosya en fazla 64 KB olabilir.
