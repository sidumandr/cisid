# cisid'i bir ekip için çalıştırmak

[English](../team-server.md) · **Türkçe**

Şirket ağındaki tek bir Linux server her şeyi çalıştırır: PostgreSQL, cisid server'ı, build'leri Docker'da çalıştıran bir agent ve önlerinde HTTPS için Caddy. Ekip arkadaşları kendi makinalarından ofis ağı ya da VPN üzerinden açar, kaydolur, GitHub ya da GitLab hesabını, ardından repolarını bağlar. Bundan sonra cisid yeni commit'leri sağlayıcılara sorar ve her birini server'da build'ler.

İnternetten erişilebilir olması gereken hiçbir şey yok: server yalnızca dışarıya, sağlayıcılara bağlanır. Webhook beklemek yerine yoklama yapmasının nedeni de bu.

```
ekip arkadaşının tarayıcısı ──https──▶ Caddy ──▶ cisid-server ──▶ PostgreSQL
                                                   ▲     │
                                            agent ─┘     └──▶ GitHub, GitLab (yoklar, klonlar, commit status yazar)
                                              │
                                        Makinadaki Docker (build adımları)
```

Dosyalar [`deployments/team/`](../../deployments/team) içinde: `compose.yaml`, `Caddyfile` ve `env.example`.

## Gerekenler

- Docker Engine ve Compose eklentisi kurulu bir Linux makina; mümkünse yalnızca CI için: agent build container'larını bu makinanın Docker'ında başlatır, bu da makinada root'a denk bir erişimdir ([deployment.md](deployment.md#agent-ve-docker-socket)). 4 CPU ve 8 GB bellek rahat bir başlangıç.
- Şirket DNS'inde ona bir ad, örneğin `ci.company.internal`. Aşağıdaki örnekler bu adı kullanır.
- Sağlayıcılara (`github.com`, `api.github.com`, `gitlab.com` ya da şirketin GitLab'ı) ve pipeline'ların image çektiği registry'lere dışarıya HTTPS erişimi.

## 1. Kodu al

```bash
git clone https://github.com/sidumandr/cisid.git
```

```bash
cd cisid
```

Image'lar bu checkout'tan build'lenir. Bir sürümde kalmak için `git checkout v0.x.y`.

## 2. OAuth uygulamalarını kaydet

Ekip arkadaşları bunlarla giriş yapar ve hesaplarını bağlar. Callback URL'inin yalnızca ekip arkadaşlarının tarayıcısında açılması yeterli; ağ içindeki bir ad da olur.

| Sağlayıcı | Nerede | Ayarlar |
|---|---|---|
| GitHub | Settings → Developer settings → **OAuth Apps** → New OAuth App; ya da uygulama şirketin olsun diye organizasyonun ayarları | Homepage URL `https://ci.company.internal`, callback URL `https://ci.company.internal/auth/github/callback` |
| GitLab | User settings → **Applications**; ya da grubun ayarları, ya da şirketin kendi GitLab'ında admin alanı | Redirect URI `https://ci.company.internal/auth/gitlab/callback`, *Confidential* işaretli, scope'lar `api` ve `read_user` |

Kullanmadığın sağlayıcıyı atla. OAuth uygulaması olmayan bir sağlayıcıyı ekip arkadaşları yine kendi token'larıyla bağlayabilir; Gitea ve Forgejo yalnızca böyle bağlanır (`CISID_GITEA_URL` ya da `CISID_FORGEJO_URL` ayarlıyken, [forges.md](forges.md)).

Repolar OAuth uygulamalarını kısıtlayan bir GitHub organizasyonundaysa, organizasyonun bir sahibi uygulamayı bir kez onaylar (Organization settings → Third-party access) ya da uygulama organizasyonun kendisinin olur.

## 3. Ayarları doldur

```bash
cp deployments/team/env.example deployments/team/.env
```

```bash
chmod 600 deployments/team/.env
```

Sonra `deployments/team/.env` dosyasını doldur. Önemli olanlar:

| Ayar | |
|---|---|
| `CISID_HOST`, `CISID_PUBLIC_URL` | Ekip arkadaşlarının açtığı ad ve URL olarak aynısı. Sağlayıcılar tarayıcıyı `<CISID_PUBLIC_URL>/auth/<forge>/callback` adresine geri gönderir. |
| `CISID_DB_PASSWORD`, `CISID_AGENT_TOKEN`, `CISID_API_TOKEN` | Uzun, rastgele değerler: `openssl rand -hex 32` |
| `CISID_SECRET_KEY` | `openssl rand -base64 32`. Repoların secret'larını ve ekip arkadaşlarının sağlayıcı token'larını şifreler. Bir kopyasını veritabanı yedeklerinden ayrı (bir parola yöneticisinde) sakla: o olmadan ikisi de bir daha okunamaz. |
| `CISID_SIGNUP_ROLE=maintainer` | Server'a ulaşan herkes maintainer olarak hesap açabilir; maintainer repo bağlar ve build çalıştırır. |
| `CISID_OAUTH_SIGNUP_ROLE=maintainer` | **GitHub ile devam et** ve **GitLab ile devam et** için de aynısı. |
| `CISID_POLL_INTERVAL=1m` | cisid'in yeni commit'leri sağlayıcılara ne sıklıkla sorduğu ([polling.md](polling.md)) |
| `CISID_*_OAUTH_CLIENT_ID`, `..._SECRET` | 2. adımdaki OAuth uygulamaları |
| `CISID_STEP_MEMORY`, `CISID_STEP_CPUS` | Her build container'ının sınırları |

Açık kayıt yalnızca şirket ağının dışından kimse server'a ulaşamadığı için güvenli. Bir gün dışarıdan erişilebilir olacaksa, önce iki kayıt rolünü de boşalt.

Server'ın diğer her ayarı ([server.md](server.md)) aynı dosyaya yazılabilir: server hepsini okur.

## 4. Başlat

```bash
docker compose -f deployments/team/compose.yaml up -d --build --wait
```

Server açılırken veritabanı migration'larını uygular. Caddy 80 ve 443 portlarını dinler; server, agent ve PostgreSQL dışarıya açılmaz.

## 5. Sertifikaya güven

Let's Encrypt ağ içindeki bir server'a ulaşamaz, bu yüzden Caddy sertifikayı kendi CA'sıyla imzalar. Kök sertifikasını container'dan dışarı kopyala:

```bash
docker compose -f deployments/team/compose.yaml cp caddy:/data/caddy/pki/authorities/local/root.crt cisid-root.crt
```

Her ekip arkadaşı ona bir kez güvenir ya da IT bunu diğer şirket sertifikalarıyla birlikte dağıtır:

- Windows: dosyayı aç → **Sertifika Yükle** → Yerel Makine → *Güvenilen Kök Sertifika Yetkilileri*.
- macOS: Anahtar Zinciri Erişimi → Sistem → dosyayı sürükle → aç → Güven → *Her Zaman Güven*.
- Linux: `/usr/local/share/ca-certificates/cisid-root.crt` olarak kopyala ve `sudo update-ca-certificates` çalıştır. Firefox kendi listesini tutar: Ayarlar → Sertifikalar → İçe aktar.

Şirketin kendi CA'sı varsa, sertifikasını ve anahtarını `deployments/team/certs/` içine `cert.pem` ve `key.pem` olarak koy, `Caddyfile` içindeki `tls internal` satırını `tls /certs/cert.pem /certs/key.pem` yap ve Caddy'yi yeniden başlat. Ekip arkadaşlarının yeni bir şeye güvenmesi gerekmez.

## 6. İlk hesap

`https://ci.company.internal` adresini aç ve **Hesap oluştur**: yeni bir server'daki ilk hesap onun admin'i olur ([accounts.md](accounts.md#ilk-admin)). Bunu ekibe haber vermeden önce yap.

## 7. Her ekip arkadaşının yaptığı

1. `https://ci.company.internal` adresini açar ve hesap oluşturur ya da GitHub veya GitLab ile devam eder.
2. **Hesap** → *Sağlayıcı hesapları* → GitHub ya da GitLab'ın yanında **Bağla**, sağlayıcıda cisid'e izin verir. Ya da **Token kullan** ile kendi token'ını verir; örneğin yalnızca bazı repolara ulaşan fine-grained bir GitHub token'ı.
3. **Repolar** → **Repo bağla**: repolarından birini seçer (özel olanlar da olur) ve *Varsayılan branch'i yokla* seçeneğini bırakır. cisid varsayılan branch'in son commit'ini, sonra oraya gelen her yeni commit'i build'ler.
4. Reponun varsayılan branch'inde bir `.cisid.yml` olmalı ([pipeline.md](pipeline.md)); yoksa pencere bunu söyler. Başka branch'ler reponun *Yoklanan branch'ler* sekmesinden eklenir.

Her repo, onu bağlayan ekip arkadaşının erişimiyle build'lenir: cisid onu klonlar, `.cisid.yml` dosyasını okur ve sonucu commit status olarak onun token'ıyla sağlayıcıya yazar. Repo listesi bunun kimin erişimi olduğunu gösterir. O ekip arkadaşı sağlayıcı hesabının bağlantısını keserse repo, server'ın o sağlayıcı için token'ına döner; ayarlı bir token yoksa özel repolar biri yeniden bağlayana kadar build'lenmez.

## İşletmek

| İş | Komut (checkout içinden) |
|---|---|
| Loglar | `docker compose -f deployments/team/compose.yaml logs -f server agent` |
| Aynı anda daha çok build | `docker compose -f deployments/team/compose.yaml up -d --scale agent=3`: bir agent aynı anda bir iş çalıştırır |
| Güncelleme | `git pull`, sonra 4. adımdaki başlatma komutu yeniden |
| Veritabanı yedeği | `docker compose -f deployments/team/compose.yaml exec -T postgres pg_dump -U cisid cisid \| gzip > cisid-$(date +%F).sql.gz` |
| Durdurma (veri kalır) | `docker compose -f deployments/team/compose.yaml down` |

Yedeği her gün cron'dan al ve makinanın dışına kopyala. Caddy volume'unu (`caddy-data`) da sakla: o olmadan Caddy yeni bir kök üretir ve herkesin ona yeniden güvenmesi gerekir.

Başka makinalardaki agent'lar aynı `CISID_AGENT_TOKEN` ile `wss://ci.company.internal/agent/ws` adresine bağlanır; `CISID_SERVER_CA_FILE` de `cisid-root.crt` dosyasını gösterir ([agent.md](agent.md)).

## Kısaca güvenlik

- Server'ı internete açma. Açık kayıt, sağlayıcıyla kaydolma ve ağ üzerinden giden agent token'ı yalnızca ağın içinde sorun değil.
- Bağlı bir repoya `.cisid.yml` push'layabilen herkes, agent'ın Docker erişimiyle server'da kod çalıştırır. Yalnızca ekibin kendi repolarını bağla ve CI makinasını diğer servislerden ayrı tut.
- Sağlayıcı token'ları `CISID_SECRET_KEY` ile şifrelenir ve bir daha gösterilmez. GitHub'ı OAuth ile bağlamak cisid'e `repo` scope'unu verir; bu, ekip arkadaşının ulaşabildiği her repoya ulaşır. Daha azını isteyen fine-grained bir token bağlar.
- Bir OAuth client secret'ı bir sohbette ya da bir kayıtta paylaşıldıysa, sağlayıcıda yenisini oluştur ve `.env` dosyasına yaz.

Daha fazlası: [security.md](security.md).
