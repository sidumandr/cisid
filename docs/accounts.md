# Hesaplar ve roller

cisid'de kullanıcı hesapları vardır. Her kullanıcının bir **rolü** olur ve kullanıcılar şifreyle, git sağlayıcılarıyla (GitHub, Gitea, Forgejo, GitLab, Bitbucket; [aşağıda](#sağlayıcıyla-giriş)) ya da kişisel API token'larıyla giriş yapar. Server'ın kendi token'ları (`CISID_API_TOKEN`, `CISID_READ_TOKEN`) eskisi gibi çalışmaya devam eder; hesaplar onların yanına eklenir.

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

## Sağlayıcıyla giriş

Kullanıcılar sağlayıcıdaki hesaplarıyla giriş yapabilir (OAuth 2.0, authorization code akışı). Her sağlayıcıda bir OAuth uygulaması kaydedilir ve client id ile secret'ı server'a verilir. Giriş diyaloğunda her sağlayıcı için bir **Sign in with ...** düğmesi çıkar.

| Değişken | Açıklama |
|---|---|
| `CISID_GITHUB_OAUTH_CLIENT_ID`, `CISID_GITHUB_OAUTH_CLIENT_SECRET` | GitHub (ya da GitHub Enterprise Server, `CISID_GITHUB_API_URL`'den) |
| `CISID_GITEA_OAUTH_CLIENT_ID`, `..._SECRET` | Gitea (`CISID_GITEA_URL` gerekir) |
| `CISID_FORGEJO_OAUTH_CLIENT_ID`, `..._SECRET` | Forgejo (`CISID_FORGEJO_URL` gerekir) |
| `CISID_GITLAB_OAUTH_CLIENT_ID`, `..._SECRET` | GitLab (`CISID_GITLAB_URL`, varsayılan GitLab.com) |
| `CISID_BITBUCKET_OAUTH_CLIENT_ID`, `..._SECRET` | Bitbucket Cloud (OAuth consumer'ın key ve secret'ı) |
| `CISID_OAUTH_SIGNUP_ROLE` | `viewer` ya da `maintainer`: hesabı olmayan sağlayıcı kullanıcılarına ilk girişte bu rolle hesap açılır. Boşsa yalnızca admin'in bağladığı kullanıcılar girer. |
| `CISID_PUBLIC_URL` | Zorunlu: sağlayıcı tarayıcıyı `<CISID_PUBLIC_URL>/auth/<sağlayıcı>/callback` adresine geri gönderir |

**Uygulamayı kaydetmek.** Her sağlayıcıda *callback* (redirect) adresi `https://<cisid>/auth/<sağlayıcı>/callback` olur.

| Sağlayıcı | Nerede | İzin |
|---|---|---|
| GitHub | Settings → Developer settings → **OAuth Apps** → New OAuth App | İzin gerekmez (herkese açık profil) |
| Gitea, Forgejo | Settings → **Applications** → OAuth2 Applications (ya da site yönetiminden) | `read:user` |
| GitLab | User settings → **Applications** (ya da grup/admin uygulaması) | `read_user` |
| Bitbucket | Workspace settings → **OAuth consumers** → Add consumer; *This is a private consumer* işaretli | *Account: Read* |

**Hesaplar nasıl eşleşir.**

- Bir admin kullanıcıya sağlayıcıdaki kullanıcı adını yazar (`identities`, ör. `{"gitea":"ahmet"}`; arayüzde *Forge logins*). O kullanıcı sağlayıcıyla ilk kez girdiğinde sağlayıcının **değişmeyen kullanıcı numarası** hesaba bağlanır. Sonraki girişler bu numarayla eşleşir: kullanıcı sağlayıcıda adını değiştirse de girer, eski adını alan başka biri giremez.
- `CISID_OAUTH_SIGNUP_ROLE` tanımlıysa eşleşmeyen sağlayıcı kullanıcısı için hesap açılır. Kullanıcı adı sağlayıcıdaki adından türetilir; alınmışsa sonuna sağlayıcının adı eklenir (`ahmet-gitea`). Kendi kurduğunuz bir Gitea ya da GitLab'da herkes çalışansa kullanışlıdır. **GitHub.com, GitLab.com ya da Bitbucket'ta açmayın:** o sağlayıcıda hesabı olan herkes girebilir.
- Devre dışı bırakılan kullanıcı sağlayıcıyla da giremez.

**Güvenlik.** Giriş `state` parametresiyle bağlanır (kısa ömürlü, HttpOnly bir cookie'de, `SameSite=Lax`). Dönüş adresi yalnızca bu sitedeki bir yol olabilir (açık yönlendirme yok). Sağlayıcının verdiği erişim token'ı yalnızca "kim giriş yaptı" sorusu için kullanılır, saklanmaz. Başarısız bir giriş **Hesap** sayfasına nedeniyle döner (`/account?signin=unknown` gibi).

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
