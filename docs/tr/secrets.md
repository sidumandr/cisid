# Secret'lar

[English](../secrets.md) · **Türkçe**

Deploy token'ı gibi gizli değerler repo başına saklanır ve sadece onları isteyen adımlara ortam değişkeni olarak verilir (`internal/secrets`).

## Kurulum

| Değişken | Açıklama |
|---|---|
| `CISID_SECRET_KEY` | 32 byte'lık AES-256 anahtarı, base64. Bununla üretilir: `openssl rand -base64 32`. Tanımlı değilse secret'lar kapalıdır. |
| `CISID_API_TOKEN` ya da bir maintainer | Secret API'si `maintainer` rolü ister ([accounts.md](accounts.md)). |

> Anahtar kaybolursa saklanan secret'lar çözülemez ve yeniden girilmeleri gerekir. Anahtarı veritabanından ayrı bir yerde saklayın.

## Secret ekleme ve silme

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"value":"...","branches":["main"]}' http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

```bash
curl -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets
```

```bash
curl -X DELETE -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

| Uç nokta | Yanıt |
|---|---|
| `PUT …/secrets/{name}` | `204`. Var olan değerin ve branch listesinin üzerine yazar. `branches` isteğe bağlıdır. |
| `GET …/secrets` | `200 [{"name":"DEPLOY_TOKEN","updated_at":"…","branches":["main"]}]`. Değerler **hiçbir zaman** geri dönmez. |
| `DELETE …/secrets/{name}` | `204`, yoksa `404` |

Kurallar:

- Ad bir ortam değişkeni adı olmalıdır: harf, rakam ve `_`, rakamla başlamaz, en fazla 100 karakter. cisid'in adımlara kendi verdiği değişkenler (`CI`, `CISID`, `CISID_JOB_ID`, `CISID_STEP`, `CISID_REPO`, `CISID_BRANCH`, `CISID_COMMIT_SHA`, `CISID_CLONE_URL`) kullanılamaz.
- Değer en fazla 64 KB olabilir.
- `branches` en fazla 20 desen alır. Desenler `when.branch` ile aynı söz dizimini kullanır (`main`, `release/*`).

## Secret'ı branch'lere sınırlamak

`branches` verilen bir secret yalnızca bu branch'lerdeki push build'lerine gider, pull request'lere hiç gitmez. Bu kararı server verir.

`when.branch` ise bir güvenlik sınırı değildir. `.cisid.yml` derlenen commit'ten okunur, repoya push edebilen biri koşulu kendi branch'inde silebilir. Deploy token'ı gibi hassas secret'ları korumalı branch'lere `branches` ile sınırlayın:

| Job | `branches` yok | `branches: ["main"]` |
|---|---|---|
| `main`'e push | verilir | verilir |
| `feature/x`'e push | verilir | verilmez |
| Repodan açılan pull request | verilir | verilmez |
| Fork'tan pull request | verilmez | verilmez |

Secret'ı alamayan adım yine çalışır, yalnızca o ortam değişkeni tanımlı olmaz.

## Kullanım

```yaml
steps:
  - name: deploy
    image: alpine:3.22
    commands:
      - ./deploy.sh
    secrets: [DEPLOY_TOKEN]
    when:
      branch: main
```

- Adım `DEPLOY_TOKEN` ortam değişkenini görür. Diğer adımlar görmez.
- Bir secret aynı adımın `environment` alanında da tanımlanamaz.
- İstenen secret repoda yoksa job başlamadan `failed` olur: `secret not found: DEPLOY_TOKEN is not set for sidumandr/cisid`.
- `when` ile atlanan adımların secret'ları okunmaz.

## Loglarda maskeleme

Agent, adımın çıktısını server'a göndermeden önce secret değerlerini `***` ile değiştirir. Değerler veritabanındaki loglara hiç ulaşmaz.

- Değer çıktıda iki parçaya bölünmüş olsa da maskelenir. Bir parçanın sonu bir secret'ın başlangıcı olabilecekse, o kısım bir sonraki parçaya kadar bekletilir.
- Çok satırlı değerlerin (ör. özel anahtar) her satırı ayrıca maskelenir.
- 3 karakterden kısa değerler maskelenmez, çünkü maskelemek logu okunmaz hale getirir.

Maskeleme bir güvenlik ağıdır, sınır değildir. Adım değeri dönüştürerek yazarsa (base64, ters çevirme) maske bunu yakalayamaz.

## Güvenlik modeli

- **Saklama:** Değerler AES-256-GCM ile şifrelenir, her değer için rastgele bir nonce kullanılır. Şifreli veri `repo/ad` bilgisine bağlıdır. Veritabanında başka bir satıra taşınan ya da değiştirilen bir değer çözülemez.
- **Server'da:** Secret değerleri ve API token'ı server loglarına yazılmaz.
- **Hangi job'lar secret görür:** Push build'leri ve repodaki bir branch'ten açılan pull request'ler, adımlarının istediği secret'ları alır. `branches` ile sınırlanan secret'lar yalnızca o branch'lerdeki push'lara verilir. Fork'tan gelen pull request'lere (`CISID_BUILD_FORKS`) hiçbir secret verilmez; secret isteyen adımlar o değişkenler olmadan çalışır.
- **Taşıma:** Secret'lar agent'a WebSocket üzerinden gider. Agent ile server arasında TLS (`wss://`) kullanın. Agent'lar token ile doğrulanır ([agent.md](agent.md#kimlik-doğrulama-ve-tls)).
