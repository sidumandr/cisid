# Publishing a release

**English** · [Türkçe](tr/release.md)

When a `vX.Y.Z` tag is pushed, [`.github/workflows/release.yml`](../.github/workflows/release.yml) runs in GitHub Actions. The steps run in order; if one fails, the later ones do not run:

1. **test:** `go vet`, all tests with `-race` against a PostgreSQL service, integration tests against real Docker (`CISID_DOCKER_TESTS=1`) and the chart tests. If the tests fail, nothing is published.
2. **images:** The server and agent images are built for `linux/amd64` and `linux/arm64`. They are pushed to `ghcr.io/sidumandr/cisid-server` and `ghcr.io/sidumandr/cisid-agent` with the `X.Y.Z` and `X.Y` tags.
3. **chart:** The Helm chart is packaged with its version and `appVersion` taken from the tag, and pushed to `oci://ghcr.io/sidumandr/charts/cisid`. This makes the chart's default image tag point at that release's images.
4. **release:** The GitHub release is created with notes generated from the commits and with the chart package.

## Cutting a release

```bash
git tag -a v0.1.0 -m "cisid 0.1.0"
```

```bash
git push origin v0.1.0
```

Progress shows in the repository's **Actions** tab. New packages on GitHub can be private by default. After the first release, set the visibility of `cisid-server`, `cisid-agent` and `charts/cisid` to **Public** under **Packages**; otherwise only your account can pull them.

## Installing the published chart

```bash
helm install ci oci://ghcr.io/sidumandr/charts/cisid --version 0.1.0 --namespace ci \
  --set-string secrets.webhookSecret=$(openssl rand -hex 32) \
  --set-string secrets.agentToken=$(openssl rand -hex 24) \
  --set-string postgresql.auth.password=$(openssl rand -hex 16)
```

All values and the installation notes are in the [chart README](../deployments/helm/cisid/README.md).
