# Bildirimler ve rozetler

[English](../notifications.md) · **Türkçe**

cisid, kimse arayüzü izlemeden build'lerin nasıl gittiğini haber verebilir: bir branch kırıldığında ya da düzeldiğinde sohbete bir mesaj, bir webhook'a JSON isteği ya da e-posta. Rozet ise bir branch'in son sonucunu README'de gösterir.

## Bildirimler

Her reponun kendi bildirimleri vardır. Bir bildirim, sonuçların nereye ve hangi sonuçların gideceğini söyler:

| Ayar | Değerler |
|---|---|
| Ad | Küçük harf, rakam, `.`, `_` ve `-`, en fazla 63 karakter. Aynı adla yeniden kaydetmek bildirimi değiştirir. |
| Tür | `slack`: Slack incoming webhook'u ya da onun mesajlarını kabul eden bir sohbet (Mattermost, Rocket.Chat, sonuna `/slack` eklenmiş Discord webhook adresi). `webhook`: herhangi bir adrese JSON isteği. `email`: server'ın SMTP sunucusu üzerinden. |
| Hedef | Adres (`slack`, `webhook`) ya da 1 ile 10 arası e-posta adresi (`email`) |
| Ne zaman (`on`) | Şunlardan herhangi biri: `failed` (kırılan her build), `broken` (geçen bir build'den sonra gelen kırılma ya da kırılan ilk build), `fixed` (kırılan bir build'den sonra geçen build), `passed` (geçen her build). Varsayılan: `failed` ve `fixed`. |
| Branch'ler | `when.branch` gibi glob desenleri (`main`, `release/*`); boşsa her branch |
| Pull request'ler | Varsayılan olarak kapalı. Açıksa pull request build'leri de, birleşecekleri branch'e göre eşleşerek bildirilir. |
| İmza secret'ı | Yalnızca `webhook` için, isteğe bağlı: gövde `X-Cisid-Signature` başlığında imzalanır |

Bildirimler `CISID_SECRET_KEY` ister ([secrets.md](secrets.md)): bir sohbetin webhook adresi bir kimlik bilgisidir, bu yüzden hedefler secret'larla aynı anahtarla şifrelenerek saklanır ve bir daha gösterilmez. Arayüz ve API yalnızca adresin host'unu ya da e-posta adreslerini gösterir.

### Arayüzde

Repo sayfasındaki **Notifications** sekmesi bildirimleri, her birinin en son ne zaman gönderildiğiyle ve son deneme başarısız olduysa nedeniyle listeler. **Add notification** bir form açar; **Test** hemen bir deneme mesajı gönderir. Bildirimleri yönetmek `maintainer` ister.

### API ile

| Yol | Açıklama |
|---|---|
| `GET /api/repos/{owner}/{repo}/notifications` | Bildirimler: `name`, `kind`, `target` (host ya da adresler), `on`, `branches`, `pull_requests`, `last_sent_at`, `last_error` |
| `PUT /api/repos/{owner}/{repo}/notifications/{name}` | Bir bildirimi oluşturur ya da değiştirir. `204`, ya da nedeniyle birlikte `400`. |
| `DELETE /api/repos/{owner}/{repo}/notifications/{name}` | `204`, ya da `404` |
| `POST /api/repos/{owner}/{repo}/notifications/{name}/test` | Deneme mesajı gönderir: gittiyse `204`, gitmediyse nedeniyle birlikte `502` |

Bütün endpoint'ler `maintainer` ister. `CISID_SECRET_KEY` yoksa `403` döner.

```bash
curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"kind":"slack","url":"https://hooks.slack.com/services/T000/B000/XXXX","on":["broken","fixed"],"branches":["main"]}' \
  http://localhost:8080/api/repos/sidumandr/cisid/notifications/team-chat

curl -X PUT -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"kind":"email","to":["dev@example.com","ops@example.com"]}' \
  http://localhost:8080/api/repos/sidumandr/cisid/notifications/team-mail
```

`PUT` gövdesi: `kind`, `url` ya da `to`, isteğe bağlı olarak `secret`, `on`, `branches`, `pull_requests`.

### Mesajlar

Mesaj repoyu, branch'i (ya da pull request'i) ve ne olduğunu söyler; kırılmanın nedenini, build numarasını, commit'i ve build'i neyin başlattığını ekler. `CISID_PUBLIC_URL` tanımlıysa job sayfasına bağlantı verir.

```
sidumandr/cisid: main is broken
Step "test" failed with exit code 1 after 1m48s.
Build #42 · commit 6113728 · push
```

Başlık en çok şey anlatan sonuçtur: bir önceki build biliniyorsa `is broken` ve `is fixed`, kırılmadan sonra gelen kırılmada `failed again`, aksi hâlde `failed` ve `passed`. Matrix kombinasyonları aynı kombinasyonun bir önceki build'iyle karşılaştırılır.

### Webhook'lar

`webhook` bildirimi JSON gönderir:

```json
{
  "result": "broken",
  "repo": "sidumandr/cisid",
  "title": "sidumandr/cisid: main is broken",
  "detail": "Step \"test\" failed with exit code 1 after 1m48s.",
  "url": "https://ci.example.com/jobs/42",
  "job": { "id": 42, "repo": "sidumandr/cisid", "branch": "main", "status": "failed", "...": "..." }
}
```

`job`, job API'sinin döndürdüğü job'dur ([api.md](api.md#job)); deneme mesajında `"result": "test"` olur ve `job` bulunmaz. Başlıklarda `X-Cisid-Result` (sonuç) ve imza secret'ı varsa `X-Cisid-Signature: sha256=<hex>` gelir: gövdenin secret ile HMAC-SHA256'sı, GitHub'ın webhook'larını imzaladığı gibi. Gövdeye güvenmeden önce imzayı doğrulayın.

### E-posta

E-posta, server'a tanımlanan bir SMTP sunucusu üzerinden gider:

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_SMTP_HOST` | yok | SMTP sunucusu. Tanımlı değilse e-posta bildirimi kaydedilemez. |
| `CISID_SMTP_PORT` | `587`, `tls` ile `465` | |
| `CISID_SMTP_TLS` | `starttls` | `starttls`: STARTTLS ile şifreli bağlantıya geçer, sunmayan sunucuyu reddeder. `tls`: baştan TLS (port 465). `none`: şifresiz, yalnızca aynı makinedeki ya da güvenilir bir ağdaki sunucu için. |
| `CISID_SMTP_USERNAME`, `CISID_SMTP_PASSWORD` | yok | Birlikte tanımlanır. Şifre, `localhost` dışında hiçbir zaman şifresiz bağlantıdan gönderilmez. |
| `CISID_SMTP_FROM` | yok, host ile zorunlu | Gönderen, ör. `cisid <ci@example.com>` |

Mesajlar UTF-8 düz metindir ve otomatik yanıtlar karışmasın diye `Auto-Submitted: auto-generated` ile işaretlenir.

### Gönderim

- Bir job bittiğinde sonucunu tek bir server alır ve bir kez bildirir; birden fazla server aynı veritabanını paylaşsa da. İptal edilen build'ler ve bir günden eski sonuçlar bildirilmez (kapalı kalmış bir server açıldığında dünü bildirmez).
- Sonra geçebilecek bir hata (ağ hatası, `429`, `5xx`, geçici SMTP hatası) olursa gönderim 2 ve 10 saniye sonra iki kez daha denenir. Sonra hata bildirime yazılır ve arayüzde görünür; bir sonraki sonuç her zamanki gibi gönderilir. Server yeniden başlarsa gönderim tekrarlanmaz: bir sonuç en fazla bir kez bildirilir.
- `cisid_notifications_total` metriği gönderimleri tür ve sonuca göre sayar ([metrics.md](metrics.md)).
- Sohbet ve webhook istekleri link-local adreslere (ör. bulut metadata servisi `169.254.169.254`), multicast adreslere ve bilinen metadata adreslerine gönderilmez, yönlendirmeler izlenmez. Sohbetler çoğunlukla kurum içinde barındırıldığı için özel ağlara izin verilir. Hatalar adresin yalnızca host'unu söyler; adresin geri kalanı hiçbir zaman loglanmaz.

## Rozetler

```markdown
[![cisid](https://ci.example.com/api/repos/sidumandr/cisid/badge.svg)](https://ci.example.com/repos/sidumandr/cisid)
```

`GET /api/repos/{owner}/{repo}/badge.svg`, reponun varsayılan branch'inin son sonucunu SVG olarak çizer: **passing**, **failing**, ya da branch'in henüz biten bir build'i yoksa **unknown**. `?branch=release/1.0` başka bir branch'i gösterir. Repo sayfasındaki **Badge** düğmesi kopyalanacak Markdown'ı verir.

- Geçen ya da kırılan build'ler sayılır; iptal edilen ve çalışan build'ler ile pull request'ler sayılmaz. Matrix build'inde kombinasyonlardan biri kırıldıysa build kırılmış sayılır; bir kombinasyonun yeniden çalıştırılması onun önceki sonucunun yerine geçer.
- Varsayılan branch sağlayıcıdan öğrenilir ve 10 dakika hatırlanır. cisid'in hiç derlemediği repolar, sağlayıcıya sorulmadan `unknown` gösterir.
- Rozetler job'lar gibi okunur: okumak giriş istiyorsa (`CISID_READ_TOKEN` ya da `CISID_READ_ACCESS=signed-in`), GitHub'daki bir README onları gösteremez. `CISID_PUBLIC_BADGES=true`, geri kalan her şey giriş istemeye devam ederken rozetleri herkese açar. Rozet yalnızca bir branch'in son build'inin geçip geçmediğini gösterir.
- Yanıtlar `Cache-Control: no-cache` ve `ETag` taşır; böylece GitHub'ınki gibi görsel proxy'leri son sonucu gösterir.
