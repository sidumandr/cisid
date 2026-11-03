# Secret'lar

Deploy token'ı gibi gizli değerler repo başına saklanır ve sadece onları isteyen adımlara ortam değişkeni olarak verilir (`internal/secrets`).

## Kurulum

| Değişken | Açıklama |
|---|---|
| `CISID_SECRET_KEY` | 32 byte'lık AES-256 anahtarı, base64. Bununla üretilir: `openssl rand -base64 32`. Tanımlı değilse secret'lar kapalıdır. |
| `CISID_API_TOKEN` | Secret API'si için gereklidir ([api.md](api.md#iptal-ve-yeniden-çalıştırma)). |

> Anahtar kaybolursa saklanan secret'lar çözülemez ve yeniden girilmeleri gerekir. Anahtarı veritabanından ayrı bir yerde saklayın.

## Secret ekleme ve silme

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"value":"..."}' http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

```bash
curl -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets
```

```bash
curl -X DELETE -H "Authorization: Bearer $CISID_API_TOKEN" http://localhost:8080/api/repos/sidumandr/cisid/secrets/DEPLOY_TOKEN
```

| Uç nokta | Yanıt |
|---|---|
| `PUT …/secrets/{name}` | `204`. Var olan değerin üzerine yazar. |
| `GET …/secrets` | `200 [{"name":"DEPLOY_TOKEN","updated_at":"…"}]`. Değerler **hiçbir zaman** geri dönmez. |
| `DELETE …/secrets/{name}` | `204`, yoksa `404` |

Kurallar:

- Ad bir ortam değişkeni adı olmalıdır: harf, rakam ve `_`, rakamla başlamaz, en fazla 100 karakter. cisid'in adımlara kendi verdiği değişkenler (`CI`, `CISID`, `CISID_JOB_ID`, `CISID_STEP`, `CISID_REPO`, `CISID_BRANCH`, `CISID_COMMIT_SHA`, `CISID_CLONE_URL`) kullanılamaz.
- Değer en fazla 64 KB olabilir.

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
- **Hangi job'lar secret görür:** Push build'leri ve repodaki bir branch'ten açılan pull request'ler, adımlarının istediği secret'ları alır. Fork'tan gelen pull request'lere (`CISID_BUILD_FORKS`) hiçbir secret verilmez; secret isteyen adımlar o değişkenler olmadan çalışır.
- **Taşıma:** Secret'lar agent'a WebSocket üzerinden gider. Agent ile server arasında TLS (`wss://`) kullanın. Agent'lar token ile doğrulanır ([agent.md](agent.md#kimlik-doğrulama-ve-tls)).
