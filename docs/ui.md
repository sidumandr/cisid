# Web arayüzü

Server, job'ları izlemek için küçük bir web arayüzü sunar (`internal/web`). Arayüz binary'nin içine gömülüdür. Dışarıdan hiçbir CDN ya da kütüphane yüklemez, bütün verisini [Job API](api.md)'sinden alır.

| Sayfa | İçerik |
|---|---|
| `/` | Son 50 job: durum, repo, branch, commit, agent, süre. 5 sn'de bir yenilenir. |
| `/jobs/{id}` | Job özeti ve adım adım canlı loglar. Loglar SSE akışıyla gelir ([api.md](api.md#canlı-akış)). |

- Loglar adım adlarıyla gruplanır. Başarısız ya da iptal edilen adım işaretlenir. stderr satırları farklı renkte gösterilir, ANSI renk kodları temizlenir.
- Log en alttayken yeni satırlar geldikçe sayfa otomatik kayar. Kullanıcı yukarı kaydırmışsa yerinde kalır.
- **Cancel** ve **Rerun** butonları API token'ı ister. Token ilk kullanımda sorulur ve sadece o sekmenin oturumunda (`sessionStorage`) tutulur.
- Açık ve koyu tema, işletim sisteminin ayarına göre seçilir.
- `CISID_PUBLIC_URL` tanımlıysa GitHub commit status bağlantıları job sayfasına (`/jobs/{id}`) gider.

## Güvenlik

- Sayfalar `Content-Security-Policy: default-src 'self'` ile sunulur: inline script yoktur, başka origin'den hiçbir şey yüklenmez. Ayrıca `frame-ancestors 'none'` ve `X-Content-Type-Options: nosniff` gönderilir.
- API'den gelen her değer (log satırları dahil) sayfaya `textContent` ile eklenir, hiçbir zaman HTML olarak yorumlanmaz. Bir adımın çıktısındaki `<script>` metin olarak görünür.
- Okuma sayfaları kimlik doğrulama istemez (Faz 4). Server'ı güvenilmeyen bir ağa açmadan önce önüne erişim kontrolü koyun.
