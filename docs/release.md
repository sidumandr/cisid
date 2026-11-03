# Sürüm yayımlama

Bir `vX.Y.Z` etiketi push'landığında [`.github/workflows/release.yml`](../.github/workflows/release.yml) GitHub Actions'ta çalışır. Adımlar sırayla ilerler; biri başarısız olursa sonrakiler çalışmaz:

1. **test:** `go vet`, PostgreSQL servisiyle bütün testler `-race` ile, gerçek Docker'a karşı entegrasyon testleri (`CISID_DOCKER_TESTS=1`) ve chart testleri. Testler geçmezse hiçbir şey yayımlanmaz.
2. **images:** Server ve agent image'ları `linux/amd64` ve `linux/arm64` için derlenir. `ghcr.io/sidumandr/cisid-server` ve `ghcr.io/sidumandr/cisid-agent` adresine `X.Y.Z` ve `X.Y` etiketleriyle gönderilir.
3. **chart:** Helm chart'ı, sürümü ve `appVersion`'ı etiketten alınarak paketlenir ve `oci://ghcr.io/sidumandr/charts/cisid` adresine gönderilir. Chart'ın varsayılan image etiketi böylece o sürümün image'ları olur.
4. **release:** GitHub release'i, commit'lerden üretilen notlarla ve chart paketiyle oluşturulur.

## Sürüm çıkarmak

```bash
git tag -a v0.1.0 -m "cisid 0.1.0"
```

```bash
git push origin v0.1.0
```

İlerleme repo'nun **Actions** sekmesinde görünür. Yeni paketler GitHub'da varsayılan olarak özel olabilir. İlk yayımdan sonra **Packages** altında `cisid-server`, `cisid-agent` ve `charts/cisid` için görünürlüğü **Public** yapın; yoksa yalnızca sizin hesabınız çekebilir.

## Yayımlanan chart'ı kurmak

```bash
helm install ci oci://ghcr.io/sidumandr/charts/cisid --version 0.1.0 --namespace ci \
  --set-string secrets.webhookSecret=$(openssl rand -hex 32) \
  --set-string secrets.agentToken=$(openssl rand -hex 24) \
  --set-string postgresql.auth.password=$(openssl rand -hex 16)
```

Değerlerin tamamı ve kurulum notları [chart README'sinde](../deployments/helm/cisid/README.md).
