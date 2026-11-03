# Polling: webhook'suz push build'leri

Bir sağlayıcının webhook'u server'a ulaşamıyorsa (ör. ofis ağındaki, NAT ya da firewall arkasındaki bir sunucu), cisid branch'leri kendisi yoklayabilir. Server izlenen her branch'in son commit'ine belli aralıklarla bakar; commit değiştiyse bir **push** build'i açar. Hiçbir port açmak, tünel kurmak ya da sağlayıcıda webhook tanımlamak gerekmez: bağlantıyı her zaman server başlatır.

Polling bütün sağlayıcılarda çalışır ([forges.md](forges.md)). Webhook'larla birlikte de kullanılabilir.

## Kurulum

| Değişken | Varsayılan | Açıklama |
|---|---|---|
| `CISID_POLL_INTERVAL` | `1m` | Her branch'in yoklanma aralığı. En az `10s`. `0` polling'i kapatır. |

Bir branch'i izlemek için arayüzde repo sayfasındaki **Polling** sekmesinden **Poll a branch** düğmesini kullanın ya da API'yi çağırın (API token'ı gerekir):

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" -d '{"branch":"main"}' \
  http://localhost:8080/api/repos/gitea:office/app/watches
```

Repo henüz cisid'de hiç derlenmediyse arayüzde görünmez; ilk branch'i API ile ekleyin ya da adresini doğrudan açın: `/repos/gitea:office/app`.

| Yol | Açıklama |
|---|---|
| `GET /api/repos/{owner}/{repo}/watches` | İzlenen branch'ler: `branch`, `last_sha`, `last_poll_at`, `last_job_id`, `last_error` (okuma erişimi ister) |
| `POST /api/repos/{owner}/{repo}/watches` | `{"branch":"main"}` ile branch'i izlemeye başlar; `branch` verilmezse varsayılan branch. Yeniyse `201`, zaten izleniyorsa `200`. Repo ya da branch yoksa `404`, polling kapalıysa `409`. |
| `DELETE /api/repos/{owner}/{repo}/watches/{branch}` | İzlemeyi bırakır: `204`, izlenmiyorsa `404`. `/` içeren branch adları olduğu gibi yazılır: `.../watches/release/1.0`. |

## Çalışma şekli

- **İlk yoklama** branch'in o anki son commit'ini derler; böylece izlemenin çalıştığı hemen görülür. Sonraki yoklamalar yalnızca yeni bir commit gelince build açar.
- Build'ler **push** build'leridir: `CISID_EVENT=push`, commit status bağlamı `cisid`, `when.event: [push]` adımları çalışır. Yoklama arasında birden fazla commit push edildiyse yalnızca sonuncusu derlenir.
- Her yoklama sağlayıcıya tek bir istek atar (branch'in son commit'i). Server her 5 saniyede vakti gelen branch'leri alır.
- **Birden fazla server kopyası** aynı veritabanını paylaşıyorsa her yoklamayı yalnızca bir kopya yapar ve aynı commit hiçbir zaman iki kez derlenmez: branch'in son görülen commit'i, build'i açan transaction'da güncellenir.
- Yoklama başarısız olursa (sağlayıcıya ulaşılamadı, token geçersiz, branch silindi) hata `last_error`'a yazılır, arayüzde görünür ve sonraki yoklamada tekrar denenir. Başarılı bir yoklama hatayı temizler.
- Polling yalnızca branch'leri izler. **Pull request'ler** için webhook gerekir.

## Rate limit

Her izlenen branch, aralık başına bir istek demektir. 1 dakikalık aralıkla bir branch saatte 60 istek yapar.

| Sağlayıcı | Token'sız | Token'la |
|---|---|---|
| GitHub | IP başına saatte 60: tek bir branch'i ancak karşılar | Saatte 5000: 1 dakikalık aralıkla yaklaşık 80 branch |
| GitLab.com, Bitbucket Cloud | Düşük sınırlar | Bol |
| Gitea, Forgejo, kendi GitLab'ınız | Sunucunun ayarına bağlı | Sunucunun ayarına bağlı |

Çok sayıda branch izliyorsanız sağlayıcının token'ını verin ya da `CISID_POLL_INTERVAL`'ı uzatın.
