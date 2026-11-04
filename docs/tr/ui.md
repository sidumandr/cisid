# Web arayüzü

[English](../ui.md) · **Türkçe**

Server, cisid'i tanıtan ve job'ları izlemek, yönetmek için kullanılan bir web arayüzü sunar. Kaynağı [`web/`](../../web) altında React ve TypeScript ile yazılmış, Vite ile derlenen bir uygulamadır; derleme çıktısı `internal/web/static` altında repoya eklenir ve binary'nin içine gömülür, bu yüzden server'ı çalıştırmak için Node.js gerekmez. Arayüz dışarıdan hiçbir CDN, kütüphane ya da font yüklemez; bütün verisini [Job API](api.md)'sinden alır.

## Ekranlar

| Sayfa | İçerik |
|---|---|
| `/` | Giriş yapmamış ziyaretçiye cisid'in tanıtımı: ne olduğu, bir build'in dört adımı, reponun kendi `.cisid.yml`'si, neler sunduğu ve girişler (**Sign in**, **Create an account**, okuma herkese açıksa **Browse the builds**). Okuma herkese açıksa server'ın son build'lerini canlı gösterir; değilse bir örnek build tekrar tekrar oynar: şeridi dolar, lint ve test yan yana çalışır, logu satır satır gelir ve geçer. Giriş yapmış biri doğrudan `/jobs`'a gider. |
| `/login` | **Sign in**: girişe açılmış her sağlayıcı için logosuyla bir **Continue with ...** düğmesi ([accounts.md](accounts.md#sağlayıcıyla-giriş)), kullanıcı adı ve şifre, ya da onun yerine bir token. `?next=` dönülecek sayfayı söyler (yalnızca bu sitede bir yol); başarısız bir sağlayıcı girişi `?signin=` ile döner ve nedenini yazar. |
| `/register` | **Create an account** ([accounts.md](accounts.md#kaydolmak)). Hiç hesabı olmayan bir server'da sayfa **Set up cisid** olur ve açılan hesap admin'dir. Kayıt kapalıysa bunu söyler; sağlayıcılar ilk girişte hesap açıyorsa onları önerir. |
| `/jobs` | Son 200 job. Üstte durumu özetleyen tek cümle: kaç job çalışıyor, son 24 saatte kaçı kırıldı, en sonuncusu hangi adımda. Sekmeler (Tümü, Çalışanlar, Kırılanlar, Pull request'ler) sayılarıyla birlikte; repo, branch ya da commit ile filtre. 5 saniyede bir yenilenir. |
| `/jobs/{id}` | Job'un durumu tek cümlede ("Failed at test with exit code 1 after 1m 48s."), tetikleyen olay, commit, agent, başlama zamanı ve süre. Altında adım şeridi ve seçilen adımın canlı logu. |
| `/repos` | Son job'larda görünen repolar: son build'leri, durumları, build sayıları. |
| `/repos/{owner}/{repo}` | Bir reponun build'leri, zamanlamaları, yoklanan branch'leri ([polling](polling.md)), bildirimleri ([bildirimler](notifications.md)) ve secret'ları, sekmelerde. **Badge** düğmesi reponun build rozetinin Markdown'ını verir. Diğer sağlayıcıların repoları önekiyle açılır, ör. `/repos/gitea:office/app` ya da iç içe GitLab grupları için `/repos/gitlab:group%2Fsub/project`. |
| `/account`, `/users` | Giriş yapan kullanıcının hesabı, admin'ler için de kullanıcılar ([aşağıda](#eylemler-ve-tokenlar)). |

Diyaloglar: **Run build** (repo ve branch), API token'ı, oturum açma, yeni zamanlama, yeni bildirim, yeni secret, rozet ve silme onayları.

## Adım şeridi

Her job'un adımları (clone, test, chart…) yan yana segmentler olarak çizilir. Segmentin rengi adımın durumunu söyler: geçti (yeşil), kırıldı (kırmızı), çalışıyor (turuncu, yavaşça yanıp söner), iptal edildi (gri), çalışmadı ya da sırası gelmedi (koyu gri).

- Listede her satırda küçük bir şerit vardır; hangi job'un nerede kırıldığı tek bakışta görülür.
- Job sayfasında şerit büyüktür ve tıklanır. Tıklanan adımın logu gösterilir. Sayfa açılınca çalışan adım, yoksa kırılan adım, o da yoksa son adım seçilir. Job çalışırken seçim, siz bir adıma tıklayana kadar o an çalışan adımı izler.
- Agent'lar her adımın ne zaman başlayıp bittiğini bildirir (`step_results`, [api.md](api.md)); böylece şerit aynı anda çalışan adımları ([paralel adımlar](pipeline.md#paralel-adımlar)) ve job sayfasında her adımın ne kadar sürdüğünü gösterir. Eski agent'ların çalıştırdığı job'larda çalışan adım, logu en son akan adımdır (`current_step`).

## Artifact'lar

Job sayfasında logun altında build'in sakladığı dosyalar ([pipeline.md](pipeline.md#artifactlar)) boyutlarıyla listelenir; birine tıklamak onu indirir.

## Loglar

- Loglar SSE akışıyla gelir ([api.md](api.md#canlı-akış)). ANSI renk kodları temizlenir, stderr satırları daha soluk gösterilir.
- Log paneli çıktıyla büyür, ekranı doldurunca kendi içinde kayar. En alttayken yeni satırlar geldikçe otomatik kayar; yukarı kaydırmışsanız yerinde kalır.
- Saklama süresi dolan logların yerinde ne zaman silindikleri yazar.

## Görünüm

- **Renkler:** Turuncunun göründüğü her yerde (düğmeler, çalışan adım, seçim çizgisi, çalışıyor durumu, rozet) logonun turuncusu kullanılır: koyu temanın siyahında `#F26B1D`, açık temanın beyazında `#E0560B`. Beyaz zemindeki turuncu yazı aynı turuncunun koyusudur (`#B84508`), böylece küçük yazı 5.4:1 ile okunur. Tema işletim sisteminin ayarına uyar; kenar çubuğunun altındaki düğmeyle (tanıtım sayfasında **Light**/**Dark**) değiştirilir ve tarayıcıda hatırlanır.
- **Renk yalnızca veride:** Turuncu; birincil düğmede, çalışan adımda ve seçili olanı gösteren 1px çizgide (sekme, kenar çubuğu, adım) kullanılır. Durumlar soluk zemin üzerinde aynı tonda yazıyla gösterilir.
- **Yazı:** Metinler [Authentic Sans](https://authentic.website/sans) ile yazılır. Çalışma ekranlarında rakamlar, commit SHA'ları, süreler ve loglar sistemin monospace fontuyla yazılır, böylece sütunlardaki rakamlar hizalı durur; tanıtım ve giriş sayfaları bir kademe büyük dizilir ve monospace'i yalnızca kod için kullanır. Font dosyaları `web/public/fonts/` altındadır; lisans notu [LICENSE.md](../../web/public/fonts/LICENSE.md) dosyasında.
- **Logo:** Yazısız işaret (çapraz camların ardında iki göz) kenar çubuğunun üstünde; yazılı logo (işaret ve ad) tanıtım sayfasında, giriş sayfalarında, telefonun üst çubuğunda ve footer'da durur. İkisi de temanın zeminine göre logo dosyalarındaki turuncu ve koyu/açık renkle satır içinde çizilir (`web/src/ui/Logo.tsx`); tarayıcı sekmesinde `web/public/` altındaki favicon seti görünür.
- **Baykuş:** Tanıtım sayfasındaki build'lerin ve çağrı bölümünün camı arkasında logo süzülür, ara sıra göz kırpar ve gözleri imleci izler. İmlecin kendisi de baykuştur: sayfanın üzerinde içi boş, tıklanabilir yerlerde dolu (`web/public/cursor-owl*.svg`); yazı alanlarında her zamanki yazı imleci kalır. Azaltılmış hareket açıksa baykuş kıpırdamaz.
- **Cam:** Sayfanın üzerinde duran her şey arkasını bulanık gösterir: sayfa altına kaydıkça tanıtımın üst çubuğu, dar ekranlardaki menüsü, telefonun üst ve alt çubukları ve bütün açılır listeler. Tanıtım sayfasında son build'ler de aynı camın üzerinde, büyük çizilmiş cisid logosunun önünde durur. Diyaloglar arkalarındaki sayfayı bulanıklaştırır.
- **Açılır listeler** (rol, bildirimin türü, Run build'in önerdiği repolar) tarayıcı yerine arayüz tarafından çizilir; böylece listeleri camın üzerinde durabilir. Klavyeyle çalışırlar: oklar gezer, Enter seçer, Escape listeyi kapatır ve diyaloğu açık bırakır.
- **Tanıtım sayfası** adım şeridini tek motif olarak kullanır: bir build'in dört adımı, tarayıcı animasyonu kaydırmaya bağlayabiliyorsa, sayfa kaydırıldıkça dolan bir şerittir. Azaltılmış hareket açıksa hiçbir şey kıpırdamaz.
- **Telefon:** Kenar çubuğu alta, etiketli bir çubuğa dönüşür. Satırlar iki ya da üç satırlık kartlara, job sayfasındaki bilgiler bir listeye dönüşür. Giriş sayfalarının yan paneli kalkar.
- Arayüz metinlerinde uzun tire (em dash, U+2014) kullanılmaz; `internal/web` testi bunu denetler.

Tasarım `design-taste-frontend` skill'iyle yapıldı (data terminal stili, seed 1222882761): çalışma ekranları için uygulama ekranı kuralları, tanıtım ve giriş sayfaları için sayfa kuralları; ölçümleri 1440, 768 ve 390 piksel genişlikte bir tarayıcıda alındı.

## Eylemler ve token'lar

- **Run build**, **Cancel**, **Rerun**, zamanlama, polling, bildirim ve secret işlemleri `maintainer` ister ([accounts.md](accounts.md)). Giriş yapılmamışsa ilk kullanımda bir giriş diyaloğu açılır: kullanıcı adı ve şifre, ya da bir token. Server'ın API token'ı yalnızca o sekmenin oturumunda (`sessionStorage`) tutulur; kişisel token ve şifre tarayıcıda bir oturum açar.
- Kenar çubuğunun altındaki kişi simgesi **Hesap** sayfasını (giriş yapılmamışsa **Sign in** sayfasını) açar: rol, şifre değiştirme, kişisel token'lar, çıkış. Admin'ler **Kullanıcılar** sayfasında kullanıcı ekler, rollerini ve sağlayıcı hesaplarını değiştirir, devre dışı bırakır ve siler.
- Çalışan bir job'da yalnızca **Cancel**, biten bir job'da yalnızca **Rerun** gösterilir. Onay bekleyen fork build'inde ayrıca **Approve** görünür.
- `CISID_PUBLIC_URL` tanımlıysa GitHub commit status bağlantıları job sayfasına (`/jobs/{id}`) gider.

## Güvenlik

- Sayfalar `Content-Security-Policy: default-src 'self'` ile sunulur: inline script ve stil yoktur, fontlar dahil başka origin'den hiçbir şey yüklenmez. Ayrıca `frame-ancestors 'none'` ve `X-Content-Type-Options: nosniff` gönderilir.
- API'den gelen her değer (log satırları dahil) React tarafından metin olarak çizilir, hiçbir zaman HTML olarak yorumlanmaz. Bir adımın çıktısındaki `<script>` metin olarak görünür. `internal/web` testi `dangerouslySetInnerHTML`, `innerHTML` ve benzerlerini kaynaktan uzak tutar.
- Server `CISID_READ_ACCESS=public` demedikçe okumak giriş ister: giriş yapmamış biri tanıtım sayfasını görür, diğer her sayfa giriş ister. Kullanıcı oturumu `CISID_SESSION_TTL` kadar, okuma token'ıyla açılan oturum tarayıcı kapanana kadar sürer ([api.md](api.md#okuma-erişimi)). `public` ile sayfaları herkes okur.
- Cookie ile yapılan her değişiklik `X-Cisid-Request` başlığını taşır; başka bir sitenin formu bunu ekleyemez (CSRF koruması).
- Secret değerleri arayüzde hiçbir zaman gösterilmez; yalnızca adları, hangi build'lere verildikleri ve ne zaman güncellendikleri listelenir.

## Arayüzü değiştirmek

Node.js 20.19+ ya da 22.12+ yalnızca arayüzü değiştirmek için gerekir. `web/` içinde:

```bash
npm ci
```

```bash
npm run dev
```

http://localhost:5173 üzerindeki geliştirme sunucusu sayfaları gerçek adreslerinde sunar, her değişiklikte yeniler ve `/api` ile `/auth` isteklerini bir cisid server'ına iletir; varsayılan `http://localhost:8080`, başkası için `CISID_DEV_SERVER`.

```bash
npm run typecheck
```

```bash
npm test
```

```bash
npm run build
```

`npm run build` tipleri denetler ve derlemeyi `go build`'in gömdüğü `internal/web/static` altına yazar. Derlemeyi kaynakla birlikte commit'leyin; böylece server Node.js olmadan derlenir.

| Yol | İçerik |
|---|---|
| `web/src/pages/` | Her ekran için bir dosya: `Landing`, `Auth` (giriş, kayıt), `Jobs`, `Job`, `Repos`, `Repo`, `Account`, `Users`, `NotFound` |
| `web/src/ui/` | Kenar çubuklu kabuk, diyalog katmanı, simgeler ve ortak parçalar (şerit, durum etiketleri, notlar) |
| `web/src/lib/` | API çağrıları ve giriş, giriş yapan kullanıcı, diyaloglar, metin ve biçimlendirme (testleriyle) |
| `web/src/styles/` | Çalışma ekranları için `app.css`, tanıtım ve giriş sayfaları için `site.css` |
