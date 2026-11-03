# Web arayüzü

Server, job'ları izlemek ve yönetmek için bir web arayüzü sunar (`internal/web`). Arayüz binary'nin içine gömülüdür. Dışarıdan hiçbir CDN, kütüphane ya da font yüklemez; bütün verisini [Job API](api.md)'sinden alır.

## Ekranlar

| Sayfa | İçerik |
|---|---|
| `/` | Son 200 job. Üstte durumu özetleyen tek cümle: kaç job çalışıyor, son 24 saatte kaçı kırıldı, en sonuncusu hangi adımda. Sekmeler (Tümü, Çalışanlar, Kırılanlar, Pull request'ler) sayılarıyla birlikte; repo, branch ya da commit ile filtre. 5 saniyede bir yenilenir. |
| `/jobs/{id}` | Job'un durumu tek cümlede ("Failed at test with exit code 1 after 1m 48s."), tetikleyen olay, commit, agent, başlama zamanı ve süre. Altında adım şeridi ve seçilen adımın canlı logu. |
| `/repos` | Son job'larda görünen repolar: son build'leri, durumları, build sayıları. |
| `/repos/{owner}/{repo}` | Bir reponun build'leri, zamanlamaları, yoklanan branch'leri ([polling](polling.md)) ve secret'ları, sekmelerde. Diğer sağlayıcıların repoları önekiyle açılır, ör. `/repos/gitea:office/app` ya da iç içe GitLab grupları için `/repos/gitlab:group%2Fsub/project`. |

Diyaloglar: **Run build** (repo ve branch), API token'ı, okuma için oturum açma, yeni zamanlama, yeni secret ve silme onayları.

## Adım şeridi

Her job'un adımları (clone, test, chart…) yan yana segmentler olarak çizilir. Segmentin rengi adımın durumunu söyler: geçti (yeşil), kırıldı (kırmızı), çalışıyor (turuncu, yavaşça yanıp söner), iptal edildi (gri), çalışmadı ya da sırası gelmedi (koyu gri).

- Listede her satırda küçük bir şerit vardır; hangi job'un nerede kırıldığı tek bakışta görülür.
- Job sayfasında şerit büyüktür ve tıklanır. Tıklanan adımın logu gösterilir. Sayfa açılınca çalışan adım, yoksa kırılan adım, o da yoksa son adım seçilir. Job çalışırken seçim, siz bir adıma tıklayana kadar o an çalışan adımı izler.
- Çalışan bir job'un hangi adımda olduğunu API, logu en son akan adımdan bilir (`current_step`, [api.md](api.md)).

## Loglar

- Loglar SSE akışıyla gelir ([api.md](api.md#canlı-akış)). ANSI renk kodları temizlenir, stderr satırları daha soluk gösterilir.
- Log paneli çıktıyla büyür, ekranı doldurunca kendi içinde kayar. En alttayken yeni satırlar geldikçe otomatik kayar; yukarı kaydırmışsanız yerinde kalır.
- Saklama süresi dolan logların yerinde ne zaman silindikleri yazar.

## Görünüm

- **Renkler:** Koyu temada siyah zemin üzerinde turuncu, açık temada beyaz zemin. Tema işletim sisteminin ayarına uyar; kenar çubuğunun altındaki düğmeyle değiştirilir ve tarayıcıda hatırlanır.
- **Renk yalnızca veride:** Turuncu; birincil düğmede, çalışan adımda ve seçili olanı gösteren 1px çizgide (sekme, kenar çubuğu, adım) kullanılır. Durumlar soluk zemin üzerinde aynı tonda yazıyla gösterilir.
- **Yazı:** Metinler [Authentic Sans](https://authentic.website/sans), rakamlar, commit SHA'ları, süreler ve loglar sistemin monospace fontuyla yazılır; böylece sütunlardaki rakamlar hizalı durur. Font dosyaları `internal/web/static/fonts/` altındadır; lisans notu [LICENSE.md](../internal/web/static/fonts/LICENSE.md) dosyasında.
- **Telefon:** Kenar çubuğu alta, etiketli bir çubuğa dönüşür. Satırlar iki ya da üç satırlık kartlara, job sayfasındaki bilgiler bir listeye dönüşür.
- Arayüz metinlerinde uzun tire (em dash, U+2014) kullanılmaz; `internal/web` testi bunu denetler.

Tasarım, `design-taste-frontend` skill'inin uygulama ekranı kurallarıyla yapıldı (data terminal stili, seed 1222882761): ölçümleri 1440, 768 ve 390 piksel genişlikte bir tarayıcıda alındı.

## Eylemler ve token'lar

- **Run build**, **Cancel**, **Rerun**, zamanlama, polling ve secret işlemleri `maintainer` ister ([accounts.md](accounts.md)). Giriş yapılmamışsa ilk kullanımda bir giriş diyaloğu açılır: kullanıcı adı ve şifre, ya da bir token. Server'ın API token'ı yalnızca o sekmenin oturumunda (`sessionStorage`) tutulur; kişisel token ve şifre tarayıcıda bir oturum açar.
- Kenar çubuğunun altındaki kişi simgesi **Hesap** sayfasını açar: rol, şifre değiştirme, kişisel token'lar, çıkış. Admin'ler **Kullanıcılar** sayfasında kullanıcı ekler, rollerini ve sağlayıcı hesaplarını değiştirir, devre dışı bırakır ve siler.
- Çalışan bir job'da yalnızca **Cancel**, biten bir job'da yalnızca **Rerun** gösterilir.
- `CISID_PUBLIC_URL` tanımlıysa GitHub commit status bağlantıları job sayfasına (`/jobs/{id}`) gider.

## Güvenlik

- Sayfalar `Content-Security-Policy: default-src 'self'` ile sunulur: inline script yoktur, fontlar dahil başka origin'den hiçbir şey yüklenmez. Ayrıca `frame-ancestors 'none'` ve `X-Content-Type-Options: nosniff` gönderilir.
- API'den gelen her değer (log satırları dahil) sayfaya `textContent` ile eklenir, hiçbir zaman HTML olarak yorumlanmaz. Bir adımın çıktısındaki `<script>` metin olarak görünür.
- Okuma giriş istiyorsa (`CISID_READ_ACCESS=signed-in`) arayüz ilk okumada giriş ister. Kullanıcı oturumu `CISID_SESSION_TTL` kadar, okuma token'ıyla açılan oturum tarayıcı kapanana kadar sürer ([api.md](api.md#okuma-erişimi)). Değilse sayfalar herkese açıktır.
- Cookie ile yapılan her değişiklik `X-Cisid-Request` başlığını taşır; başka bir sitenin formu bunu ekleyemez (CSRF koruması).
- Secret değerleri arayüzde hiçbir zaman gösterilmez; yalnızca adları, hangi build'lere verildikleri ve ne zaman güncellendikleri listelenir.
