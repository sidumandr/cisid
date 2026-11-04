# Pipeline (`.cisid.yml`)

[English](../pipeline.md) · **Türkçe**

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

cisid kendi testlerini de bu şekilde çalıştırır: [/.cisid.yml](../../.cisid.yml).

## Alanlar

| Alan | Zorunlu | Açıklama |
|---|---|---|
| `steps` | evet | 1 ile 50 arası adım; yazıldıkları sırayla ya da `depends_on`'a göre çalışır ([Paralel adımlar](#paralel-adımlar)) |
| `services` | hayır | Adımlardan önce başlatılan en fazla 10 servis ([Servisler](#servisler)) |
| `matrix` | hayır | Pipeline'ı her değer kombinasyonu için ayrı bir job olarak çalıştırır ([Matrix](#matrix)) |
| `cache` | hayır | Repo'nun job'ları arasında saklanacak en fazla 10 dizin, mutlak yol olarak ([Önbellek](#önbellek)) |
| `artifacts` | hayır | Adımlar geçince saklanacak en fazla 20 dosya ya da dizin; workspace'e göre yol, joker karakterler kullanılabilir ([Artifact'lar](#artifactlar)) |
| `steps[].name` | evet | Harf, rakam, `.`, `_`, `-`. En fazla 63 karakter, benzersiz olmalı. `clone` adı ayrılmıştır. |
| `steps[].image` | evet | Adımın çalışacağı Docker image'ı. `/bin/sh` içermeli. |
| `steps[].commands` | evet | Shell komutları |
| `steps[].environment` | hayır | Ortam değişkenleri. Sayı ve bool değerler string'e çevrilir. cisid'in kendi tanımladığı değişkenler (aşağıdaki tablo) ayrılmıştır. |
| `steps[].timeout` | hayır | Go süre formatında (`90s`, `10m`, `1h30m`). Varsayılan `1h`, en fazla `24h`. |
| `steps[].secrets` | hayır | Adıma ortam değişkeni olarak verilecek repo secret'ları, loglarda maskelenir ([secrets.md](secrets.md)) |
| `steps[].when.branch` | hayır | Adımı sadece bu branch'lerde çalıştırır; pull request'lerde PR'ın kendi branch'ine bakılır. Tek değer ya da liste olabilir (bkz. [Koşullar](#koşullar)). |
| `steps[].when.event` | hayır | Adımı sadece bu olaylarda çalıştırır: `push`, `pull_request`, `manual`, `schedule`. Verilmezse her olayda çalışır. |
| `steps[].depends_on` | hayır | Bu adımdan önce yazılmış ve o başlamadan geçmesi gereken adımlar; tek bir ad ya da liste. Herhangi bir adımda kullanılırsa pipeline adımları paralel çalıştırır ([Paralel adımlar](#paralel-adımlar)). |

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
      event: push
      branch: [main, "release/*"]
```

`when.branch` desenleri branch adının **tamamıyla** eşleşir:

| Desen | Eşleşir | Eşleşmez |
|---|---|---|
| `main` | `main` | `mainline` |
| `release/*` | `release/1.0` | `release/1.0/rc1` (`*` tek bir parçada kalır) |
| `hotfix/**` | `hotfix/a/b` | `hot/fix` |
| `v?.x` | `v1.x` | `v10.x` |

`when.event` adımı belirli olaylarla sınırlar: `push`, `pull_request`, elle başlatılan `manual` ve zamanlanmış `schedule` build'leri ([api.md](api.md#elle-ve-zamanlanmış-buildler)). Örneğin uzun bir test yalnızca gece build'inde `event: schedule` ile çalışabilir. Birden fazla koşul verilirse hepsinin tutması gerekir.

Pull request'lerde `when.branch` PR'ın hedef branch'ine değil, **kendi branch'ine** bakar. Deploy gibi adımları yukarıdaki gibi `event: push` ile sınırlayın; yoksa adı `main` olan bir branch'ten açılan PR'da da çalışırlar.

Eşleşmeyen adımlar job'a hiç eklenmez, sonraki adımlar normal sırayla çalışır. `when` altında `branch` ve `event` desteklenir. Başka bir anahtar yazılırsa dosya geçersiz sayılır.

## Paralel adımlar

`depends_on` yoksa adımlar yazıldıkları sırayla, birbiri ardına çalışır. Bir adımda `depends_on` kullanıldığı anda her adım yalnızca adını verdiği adımları bekler; birbirine ihtiyacı olmayan adımlar aynı anda çalışır:

```yaml
steps:
  - name: lint
    image: golang:1.27
    commands: [go vet ./...]

  - name: build
    image: golang:1.27
    commands: [go build ./...]
    depends_on: []

  - name: unit
    image: golang:1.27
    commands: [go test ./...]
    depends_on: [build]

  - name: report
    image: alpine:3.22
    commands: [./report.sh]
    depends_on: [lint, unit]
```

Burada `lint` ve `build` clone'dan hemen sonra birlikte başlar, `unit` `build` geçince, `report` ise hem `lint` hem `unit` geçince başlar.

- Bir adım yalnızca **kendinden önce yazılmış** adımlara bağlanabilir; bu döngüyü imkânsız kılar. `clone` her zaman önce yapılır, adı da verilebilir.
- `depends_on`'u olmayan adım yalnızca clone'u bekler; `depends_on: []` aynı şeyi açıkça söyler.
- Bağlı olduğu adım `when` ile atlanırsa, adım onun yerine o adımın bağlı olduklarını bekler.
- **İlk kırılma job'u durdurur:** çalışmakta olan adımlar durdurulur (`canceled`), başlamamış adımlar çalışmaz (`skipped`). Job'un `failed_step`'i kırılan adımdır.
- Aynı anda çalışan adımlar `/workspace`'i ve servisleri paylaşır: aynı dosyaya yazan iki adım birbirini bozar.
- Job sayfasında her adım kendi durumunu ve ne kadar sürdüğünü gösterir ([ui.md](ui.md#adım-şeridi)).

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

cisid'in kendi CI'ı da veritabanı testlerini bir postgres servisine karşı çalıştırır: [/.cisid.yml](../../.cisid.yml).

## Matrix

```yaml
matrix:
  GO_VERSION: ["1.26", "1.27"]
  POSTGRES: ["17", "18"]

services:
  - name: postgres
    image: postgres:${POSTGRES}-alpine
    environment:
      POSTGRES_PASSWORD: test

steps:
  - name: test
    image: golang:${GO_VERSION}
    commands:
      - go test ./...
```

- Her kombinasyon (yukarıda 2 × 2 = 4) **ayrı bir job** olarak çalışır. Job'lar farklı agent'larda paralel koşabilir, her biri kendi workspace'ini ve servislerini alır.
- Push ile oluşan job ilk kombinasyonu alır. Diğer kombinasyonlar, bu job planlanırken kardeş job olarak oluşturulur (`parent_id` ilk job'u gösterir). Kombinasyonlar eksen adına göre sıralanır, değerler yazıldıkları sırayla kullanılır.
- Her eksen adımlara ve servislere ortam değişkeni olarak verilir (`$GO_VERSION`). `image` ve `environment` değerlerinde `${EKSEN}` yazılabilir. Bir image var olmayan bir eksene başvurursa dosya geçersiz sayılır.
- `${...}` içeren bir değer akış (flow) biçiminde yazılıyorsa tırnak içine alınmalıdır: `{image: "golang:${GO_VERSION}"}`.
- En fazla 8 eksen ve 16 kombinasyon olabilir. Eksen adları ortam değişkeni adı olmalıdır, ayrılmış adlar kullanılamaz ve bir adımın `environment` alanıyla çakışamaz.
- Her kombinasyon GitHub'da kendi commit status'unu alır, örneğin `cisid (GO_VERSION=1.27, POSTGRES=18)` ([commit-status.md](commit-status.md)).
- Bir matrix job'u yeniden çalıştırılırsa sadece kendi kombinasyonu tekrar koşar.

## Artifact'lar

```yaml
steps:
  - name: build
    image: golang:1.27
    commands: [go build -o dist/ ./cmd/...]
  - name: test
    image: golang:1.27
    commands: [go test -coverprofile=coverage.out ./...]

artifacts:
  - dist
  - coverage.out
  - reports/*.xml
```

Bütün adımlar geçince cisid bir adım daha çalıştırır: `artifacts`. Bu adım listelenen dosyaları `/workspace`'ten toplar ve server'a yükler. Dosyalar job sayfasında, her biri için indirme bağlantısıyla listelenir ([ui.md](ui.md)); API'de de bulunur ([api.md](api.md#artifactlar)).

- Yollar `/workspace`'e göredir. `*`, `?` ve `[...]` bir yol parçasının içinde eşleşir (`reports/*.xml`); bir dizin içindeki bütün dosyaları saklar. Hiçbir şeyle eşleşmeyen yol adımın loguna yazılır, job'u kırmaz.
- Her dosya ayrı bir artifact'tır ve yoluyla adlanır, ör. `dist/app`. Bir job en fazla 100 dosya saklar, her biri en fazla `CISID_ARTIFACT_MAX_SIZE` (varsayılan 100 MB, [server.md](server.md)). Sınırı aşan dosya `artifacts` adımını, dolayısıyla job'u kırar.
- Bir adım kırılırsa ya da job iptal edilirse artifact toplanmaz.
- Adım clone image'ında (`alpine/git`) çalışır, logu yüklediği her dosyayı gösterir. `artifacts` adı bu adıma ayrılmıştır.
- Artifact'lar PostgreSQL'de saklanır ve `CISID_LOG_RETENTION` dolunca job'un loglarıyla birlikte silinir. Veritabanının boyutunu buna göre planlayın.
- Fork'lardan gelen pull request'ler de artifact saklar; job'u okuyabilen herkes indirebilir.

## Önbellek

```yaml
cache:
  - /go/pkg/mod
  - /root/.cache/go-build
```

Listedeki her dizin, repo'ya ait isimli bir Docker volume'u olarak bütün adımlara bağlanır. Volume job bitince silinmez; aynı Docker host'unda çalışan bir sonraki job onu hazır bulur. Bağımlılıkları her job'da baştan indirmek gerekmez.

- Yollar temiz ve mutlak olmalıdır. `/` ve `/workspace` önbelleğe alınamaz, workspace içindeki bir dizin (ör. `/workspace/node_modules`) alınabilir.
- Önbellek repo başınadır ve her agent'ın Docker host'unda ayrı tutulur. Aynı host'taki eşzamanlı job'lar aynı volume'u paylaşır; Go'nun modül ve derleme önbellekleri buna uygundur.
- Fork'tan gelen pull request'ler önbelleği salt okunur görür. Böylece repo'nun kendi build'lerine dosya bırakamazlar.
- Volume'lar `cisid.cache=<anahtar>` etiketini taşır. Hepsini silmek için: `docker volume prune -a --filter label=cisid.cache` ([agent.md](agent.md#önbellek-volumeları)).

## Çalışma şekli

- **Clone:** İlk adım her zaman `clone`'dur. Derlenen commit `alpine/git:2.52.0` ile `/workspace` altına çekilir (`--depth=1`): push'larda pushlanan commit, pull request'lerde PR'ın son commit'i. Branch adı yerel branch olarak ayarlanır.
  - Commit'i SHA'sıyla vermeyen bir sunucuda branch'in (pull request'lerde PR ref'inin, ör. `refs/pull/7/head` ya da GitLab'da `refs/merge-requests/7/head`) son 50 commit'i çekilir; derlenen commit bunların içinde olmalıdır.
  - Sağlayıcının token'ı verilmişse özel repolar o token'la clone'lanır. Token yalnızca clone adımına secret olarak verilir, çıktıda maskelenir ve `.git/config`'e yazılmaz ([forges.md](forges.md#özel-repolar)).
- **Workspace:** Bütün adımlar aynı job volume'unu `/workspace` altında paylaşır ve orada çalışır. Bir adımın ürettiği dosyalar ondan sonra çalışan adımlarda görünür.
- **Komutlar:** Bir adımın komutları tek bir `/bin/sh` script'i olarak çalışır (`set -ex`). İlk hatalı komutta adım durur ve her komut çalışmadan önce stderr'a yazılır.
- **Sıra:** Adımlar birbiri ardına ya da `depends_on`'a göre aynı anda birkaç tane çalışır ([Paralel adımlar](#paralel-adımlar)).
- **Başarısızlık:** Bir adım başarısız olursa başlamamış adımlar çalışmaz, aynı anda çalışan adımlar durdurulur.
- **Ortam değişkenleri:** Her adımda şunlar tanımlıdır:

| Değişken | Değer |
|---|---|
| `CI`, `CISID` | `true` |
| `CISID_JOB_ID` | Job numarası |
| `CISID_STEP` | Adım adı |
| `CISID_REPO` | Repo anahtarı: GitHub'da `owner/name`, diğer sağlayıcılarda ör. `gitlab:group/project` ([forges.md](forges.md#repo-adları)) |
| `CISID_EVENT` | `push`, `pull_request`, `manual` ya da `schedule` |
| `CISID_BRANCH` | Pushlanan branch; pull request'lerde PR'ın kendi branch'i |
| `CISID_COMMIT_SHA` | Derlenen commit |
| `CISID_PULL_REQUEST` | PR numarası (yalnızca pull request'lerde) |
| `CISID_TARGET_BRANCH` | PR'ın birleştirileceği branch (yalnızca pull request'lerde) |

## Dosyanın okunması

Server, job'u bir agent'a vermeden hemen önce `.cisid.yml`'yi **derlenen commit'ten** reponun sağlayıcısının API'siyle okur (`internal/forge`). Agent dosyayla hiç uğraşmaz.

| Durum | Sonuç |
|---|---|
| Dosya yok | Job `failed`: `no .cisid.yml in repository` |
| Dosya geçersiz | Job `failed`, bütün hatalar `error` alanında |
| Sağlayıcıya ulaşılamıyor, 5xx ya da rate limit | Job **başarısız sayılmaz**, kuyruğa geri döner ve sonraki turda (en geç 5 sn) tekrar denenir |
| Server kapanırken okuma yarıda kaldı | Job kuyruğa geri döner |

Herkese açık repolar token olmadan okunur. Ancak sağlayıcılar kimliksiz isteklere düşük bir sınır uygular (GitHub'da IP başına saatte 60 istek). Özel repolar ya da yoğun kullanım için sağlayıcının token'ı verilmelidir: `CISID_GITHUB_TOKEN`, `CISID_GITEA_TOKEN`, `CISID_FORGEJO_TOKEN`, `CISID_GITLAB_TOKEN` ya da `CISID_BITBUCKET_TOKEN` ([forges.md](forges.md)). Loglarda token'ların yalnızca tanımlı olup olmadığı görünür.

Dosya en fazla 64 KB olabilir.
