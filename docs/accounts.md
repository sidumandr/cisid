# Hesaplar ve roller

cisid'de kullanıcı hesapları vardır. Her kullanıcının bir **rolü** olur ve kullanıcılar şifreyle ya da kişisel API token'larıyla giriş yapar. Server'ın kendi token'ları (`CISID_API_TOKEN`, `CISID_READ_TOKEN`) eskisi gibi çalışmaya devam eder; hesaplar onların yanına eklenir.

## Roller

| Rol | Yapabildikleri |
|---|---|
| `viewer` | Job'ları, logları ve metrikleri okur |
| `maintainer` | Ayrıca build başlatır, job iptal eder ve yeniden çalıştırır; zamanlamaları, yoklanan branch'leri ve secret'ları yönetir |
| `admin` | Ayrıca kullanıcıları yönetir |

Her rol, kendinden öncekilerin yapabildiği her şeyi yapar.

## Kim, nasıl tanınır

| Yol | Kim | Not |
|---|---|---|
| `Authorization: Bearer <CISID_API_TOKEN>` | admin | Server'ın token'ı; bir kullanıcıya ait değildir |
| `Authorization: Bearer <CISID_READ_TOKEN>` | viewer | |
| `Authorization: Bearer cisid_...` | Token'ın sahibi, kendi rolüyle | Kişisel token ([aşağıda](#kişisel-tokenlar)) |
| Oturum cookie'si (`cisid_session`) | Giriş yapan kullanıcı | `POST /api/login` ile alınır |
| Okuma cookie'si (`cisid_token`) | viewer | Arayüzde okuma ya da API token'ıyla giriş yapılınca alınır, yalnızca okur |

Bir istek kabul edilmeyen bir Bearer token taşıyorsa cookie'lere bakılmaz. Kimliği olmayan bir istek `401`, rolü yetmeyen bir istek `403` alır.

## İlk admin

Yeni bir server'da ilk hesabı açmanın iki yolu var:

- `CISID_ADMIN_PASSWORD` verin. Server açılışta `CISID_ADMIN_USER` (varsayılan `admin`) adlı bir admin oluşturur; kullanıcı zaten varsa dokunmaz, şifresini değiştirmez. Şifreyi sonra arayüzden değiştirin.
- Ya da `CISID_API_TOKEN` ile kullanıcı API'sini çağırın; API token'ı admin'dir:

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"username":"ahmet","role":"admin","password":"en-az-10-karakter"}' \
  http://localhost:8080/api/users
```

## Okuma erişimi

| `CISID_READ_ACCESS` | Anlamı |
|---|---|
| `public` | Herkes job'ları, logları ve metrikleri okur. `CISID_READ_TOKEN` yoksa varsayılandır. |
| `signed-in` | Okumak giriş ya da token ister. `CISID_READ_TOKEN` varsa varsayılandır. |

Değişiklikler her zaman maintainer ister. Hiç kullanıcı ve `CISID_API_TOKEN` yoksa değişiklik yapan uç noktalar `403` döner, çünkü kimse yetkili olamaz.

## Giriş ve oturumlar

- `POST /api/login` gövdesi `{"username":"...","password":"..."}` ya da `{"token":"..."}`. Başarılıysa `204` ve bir oturum cookie'si döner (HttpOnly, HTTPS'te `Secure`, `SameSite=Lax`). Kişisel token'la giriş de bir oturum açar; server'ın token'larıyla giriş yalnızca okuma cookie'si verir.
- Yanlış şifre ile olmayan kullanıcı aynı yanıtı alır (`401 wrong username or password`) ve aynı sürede döner.
- Oturum `CISID_SESSION_TTL` kadar sürer (varsayılan `7d`). Server yalnızca oturumun SHA-256 özetini saklar. `POST /api/logout` oturumu siler. Süresi dolan oturumlar saatlik temizlikte silinir.
- **CSRF:** Cookie ile tanınan bir istek bir şey değiştiriyorsa `X-Cisid-Request` başlığını göndermelidir, yoksa `403` alır. Başka bir siteden gönderilen form bu başlığı ekleyemez. Web arayüzü bunu her istekte gönderir; Bearer token'la gelen isteklerin göndermesi gerekmez.
- Şifreler en az 10 karakterdir ve PBKDF2-HMAC-SHA256 (600.000 tur, tuzlu) ile saklanır. Hash'ler API'den hiçbir zaman dönmez.
- `GET /api/me`: kimin giriş yaptığı (`user`, `role`, `via`), okumanın açık olup olmadığı (`public_read`), hesapların açık olup olmadığı (`accounts`).

## Kişisel token'lar

Kullanıcılar betikler ve CLI için token oluşturur. Token sahibinin rolüyle çalışır; rol değişirse token'ın yetkisi de değişir. Kullanıcı devre dışı bırakılır ya da silinirse token'ları çalışmaz.

| Yol | Açıklama |
|---|---|
| `GET /api/me/tokens` | Token'lar: ad, oluşturulma ve son kullanım zamanı. Token'ın kendisi listelenmez. |
| `POST /api/me/tokens` | `{"name":"deploy script"}` ile yeni token. Token yalnızca bu yanıtta görünür: `{"id":3,"name":"...","token":"cisid_..."}` |
| `DELETE /api/me/tokens/{id}` | Token'ı iptal eder |
| `PUT /api/me/password` | `{"current":"...","new":"..."}` ile şifre değiştirir. Şifresi olmayan (yalnızca sağlayıcıyla giren) kullanıcı `current` boş bırakarak şifre koyar. |

Token'lar `cisid_` ile başlar; sızan token'ları kod taramalarında bulmayı kolaylaştırır. Server yalnızca özetlerini saklar.

## Kullanıcı yönetimi (admin)

| Yol | Açıklama |
|---|---|
| `GET /api/users` | Kullanıcılar: ad, rol, şifresi var mı, devre dışı mı, sağlayıcı hesapları |
| `POST /api/users` | `{"username":"ahmet","role":"maintainer","password":"...","identities":{"gitea":"ahmet"}}`; `password` ve `identities` isteğe bağlı. Ad alınmışsa `409`. |
| `PATCH /api/users/{username}` | `role`, `password` (`""` şifreyi kaldırır), `disabled`, `identities` alanlarından istenenler |
| `DELETE /api/users/{username}` | Kullanıcıyı, oturumlarını ve token'larını siler |

- Kullanıcı adı küçük harf, rakam, `.`, `_` ve `-` içerir, en fazla 39 karakterdir.
- Bir admin kendi rolünü düşüremez, kendini devre dışı bırakamaz ya da silemez; bunu başka bir admin ya da API token'ı yapar. Böylece kimse yanlışlıkla kendini dışarıda bırakmaz.
- Devre dışı bırakılan kullanıcının oturumları hemen kapanır ve token'ları çalışmaz.
- `identities`, kullanıcının sağlayıcılardaki kullanıcı adlarıdır.

Arayüzde: kenar çubuğunun altındaki kişi simgesi **Hesap** sayfasını (şifre, token'lar, çıkış) açar; admin'ler **Kullanıcılar** sayfasını da görür.
