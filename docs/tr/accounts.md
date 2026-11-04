# Hesaplar ve roller

[English](../accounts.md) · **Türkçe**

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

Yeni bir server'da ilk hesabı açmanın üç yolu var:

- **Kaydolmak.** Server'da hiç kullanıcı yokken **Register** sayfasından (ya da `POST /api/register` ile) ilk kaydolan admin olur. O ana kadar server'a ulaşabilen herkes bu kişi olabilir; bu yüzden ilk hesabı server dışarıdan erişilebilir olmadan önce açın ya da aşağıdaki yollardan birini kullanın.
- `CISID_ADMIN_PASSWORD` verin. Server açılışta `CISID_ADMIN_USER` (varsayılan `admin`) adlı bir admin oluşturur; kullanıcı zaten varsa dokunmaz, şifresini değiştirmez. Şifreyi sonra arayüzden değiştirin.
- Ya da `CISID_API_TOKEN` ile kullanıcı API'sini çağırın; API token'ı admin'dir:

```bash
curl -X POST -H "Authorization: Bearer $CISID_API_TOKEN" \
  -d '{"username":"ahmet","role":"admin","password":"en-az-10-karakter"}' \
  http://localhost:8080/api/users
```

## Kaydolmak

`POST /api/register`, `{"username":"...","password":"..."}` ile bir hesap açar ve tarayıcıda oturumu başlatır (`201`, kullanıcı). Arayüzdeki **Register** sayfası bunu kullanır.

- Hiç kullanıcısı olmayan bir server'da ilk hesap, başka hiçbir ayara bakılmadan admin olur. Aynı veritabanını paylaşan server'lar hangi hesabın ilk olduğunda anlaşır.
- Sonrasında kayıt kapalıdır (`403`); `CISID_SIGNUP_ROLE` `viewer` ya da `maintainer` ise server'a ulaşabilen herkes o rolle hesap açabilir. Kimse kayıtla admin olamaz.
- Kullanıcı adları küçük harfe çevrilir; ad ve şifre kuralları kullanıcı API'sindekilerle aynıdır. Alınmış bir ad `409` alır.
- `GET /api/me` arayüze neyi sunacağını söyler: `signup`, server'da hiç kullanıcı yokken `first`, herkes kaydolabiliyorsa `open`, aksi hâlde yoktur; `provider_signup`, hesabı olmayan sağlayıcı kullanıcılarına ilk girişlerinde hesap açıldığını söyler (`CISID_OAUTH_SIGNUP_ROLE`).

`CISID_READ_ACCESS=signed-in` ile `viewer` olarak açık kayıt, server'a ulaşabilen herkesin job'ları ve logları okuyabilmesi demektir.

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

Kullanıcılar sağlayıcıdaki ya da Google hesaplarıyla giriş yapabilir (OAuth 2.0, authorization code akışı; Google için OpenID Connect). Her sağlayıcıda ya da Google'da bir OAuth uygulaması kaydedilir ve client id ile secret'ı server'a verilir. Yalnızca kurulanlar görünür: **Sign in** sayfasında her biri için logosuyla (GitHub, GitLab, Google) bir **Continue with ...**, giriş diyaloğunda bir **Sign in with ...** düğmesi çıkar.

| Değişken | Açıklama |
|---|---|
| `CISID_GITHUB_OAUTH_CLIENT_ID`, `CISID_GITHUB_OAUTH_CLIENT_SECRET` | GitHub (ya da GitHub Enterprise Server, `CISID_GITHUB_API_URL`'den) |
| `CISID_GITEA_OAUTH_CLIENT_ID`, `..._SECRET` | Gitea (`CISID_GITEA_URL` gerekir) |
| `CISID_FORGEJO_OAUTH_CLIENT_ID`, `..._SECRET` | Forgejo (`CISID_FORGEJO_URL` gerekir) |
| `CISID_GITLAB_OAUTH_CLIENT_ID`, `..._SECRET` | GitLab (`CISID_GITLAB_URL`, varsayılan GitLab.com) |
| `CISID_BITBUCKET_OAUTH_CLIENT_ID`, `..._SECRET` | Bitbucket Cloud (OAuth consumer'ın key ve secret'ı) |
| `CISID_GOOGLE_OAUTH_CLIENT_ID`, `..._SECRET` | Google (bir Google Cloud projesinin OAuth istemcisi) |
| `CISID_GOOGLE_DOMAIN` | İsteğe bağlı: Google ile yalnızca bu Google Workspace alan adının (ör. `example.com`) hesapları girer, bağlı olsun olmasın |
| `CISID_OAUTH_SIGNUP_ROLE` | `viewer` ya da `maintainer`: hesabı olmayan sağlayıcı ya da Google kullanıcılarına ilk girişte bu rolle hesap açılır. Boşsa yalnızca admin'in bağladığı kullanıcılar girer. |
| `CISID_PUBLIC_URL` | Zorunlu: sağlayıcı ya da Google tarayıcıyı `<CISID_PUBLIC_URL>/auth/<sağlayıcı>/callback` adresine geri gönderir (Google için `/auth/google/callback`) |

**Uygulamayı kaydetmek.** Her yerde *callback* (redirect) adresi `https://<cisid>/auth/<sağlayıcı>/callback` olur; `<sağlayıcı>` yerine `github`, `gitea`, `forgejo`, `gitlab`, `bitbucket` ya da `google` gelir.

| Sağlayıcı | Nerede | İzin |
|---|---|---|
| GitHub | Settings → Developer settings → **OAuth Apps** → New OAuth App | İzin gerekmez (herkese açık profil) |
| Gitea, Forgejo | Settings → **Applications** → OAuth2 Applications (ya da site yönetiminden) | `read:user` |
| GitLab | User settings → **Applications** (ya da grup/admin uygulaması) | `read_user` |
| Bitbucket | Workspace settings → **OAuth consumers** → Add consumer; *This is a private consumer* işaretli | *Account: Read* |
| Google | [Google Cloud console](https://console.cloud.google.com/) → APIs & Services: önce **OAuth consent screen** kurulur, sonra **Credentials** → Create credentials → OAuth client ID, tür *Web application*, callback *Authorized redirect URIs* altına | `openid`, `email` |

**Hesaplar nasıl eşleşir.**

- Bir admin kullanıcıya sağlayıcıdaki kullanıcı adını yazar (`identities`, ör. `{"gitea":"ahmet"}`; arayüzde *Linked logins*). Google için bu e-posta adresidir (`{"google":"ahmet@example.com"}`) ve yalnızca doğrulanmış adresler girer. O kullanıcı sağlayıcıyla ilk kez girdiğinde sağlayıcının **değişmeyen kullanıcı numarası** hesaba bağlanır. Sonraki girişler bu numarayla eşleşir: kullanıcı sağlayıcıda adını değiştirse de girer, eski adını alan başka biri giremez.
- `CISID_OAUTH_SIGNUP_ROLE` tanımlıysa eşleşmeyen sağlayıcı ya da Google kullanıcısı için hesap açılır. Kullanıcı adı sağlayıcıdaki adından ya da e-posta adresinin `@` öncesinden türetilir; alınmışsa sonuna sağlayıcının adı eklenir (`ahmet-gitea`, `ahmet-google`). Kendi kurduğunuz bir Gitea ya da GitLab'da herkes çalışansa kullanışlıdır. **GitHub.com, GitLab.com ya da Bitbucket'ta, ya da `CISID_GOOGLE_DOMAIN` olmadan Google için açmayın:** oradaki her hesap sahibi girebilir. `CISID_GOOGLE_DOMAIN` ile yalnızca şirketinizin Google Workspace hesaplarına hesap açılır.
- Devre dışı bırakılan kullanıcı sağlayıcıyla da giremez.

**Güvenlik.** Giriş `state` parametresiyle bağlanır (kısa ömürlü, HttpOnly bir cookie'de, `SameSite=Lax`). Dönüş adresi yalnızca bu sitedeki bir yol olabilir (açık yönlendirme yok). Sağlayıcının ya da Google'ın verdiği erişim token'ı yalnızca "kim giriş yaptı" sorusu için kullanılır, saklanmaz. Google bir hesabın hangi Workspace alan adında olduğunu da söyler (`hd`); `CISID_GOOGLE_DOMAIN` tanımlıysa alan adı dışındaki bir hesap, neye bağlı olursa olsun geri çevrilir. Başarısız bir giriş **Sign in** sayfasına nedeniyle döner (`/login?signin=unknown` gibi).

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
