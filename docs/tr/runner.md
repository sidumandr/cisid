# Docker Runner

[English](../runner.md) · **Türkçe**

`internal/runner/docker.go` bir pipeline adımını (`Step`) Docker container'ında çalıştırır. SDK olarak `github.com/moby/moby/client` kullanılır (bkz. [CLAUDE.md](../../CLAUDE.md)).

## Kullanım

```go
cli, err := client.New(client.FromEnv)
if err != nil {
	return fmt.Errorf("create docker client: %w", err)
}
defer cli.Close()

r := runner.NewDockerRunner(cli)

code, err := r.Run(ctx, runner.Step{
	Image:      "golang:1.25",
	Cmd:        []string{"go", "test", "./..."},
	Env:        map[string]string{"CGO_ENABLED": "0"},
	WorkingDir: "/workspace",
	Mounts:     []runner.Mount{{Source: "/srv/cisid/jobs/42", Target: "/workspace"}},
	Timeout:    10 * time.Minute,
	OnPullProgress: func(p runner.PullProgress) {
		log.Printf("%s %s %d/%d", p.Layer, p.Status, p.Current, p.Total)
	},
}, stdoutWriter, stderrWriter)
```

Logları kendin okumak istersen (ör. WebSocket'e aktarmak için) `Start` kullan:

```go
exec, err := r.Start(ctx, step)
if err != nil {
	return err
}
go stream(exec.Stdout) // Stdout ve Stderr aynı anda okunmalı
go stream(exec.Stderr)
code, err := exec.Wait(ctx)
```

## Yaşam döngüsü

```
Start ─► ImagePull ─► ContainerCreate ─► ContainerStart ─► ContainerLogs (follow)
                                                             │
                      supervise goroutine ◄──────────────────┘
                        │
                        ├─ ContainerWait (not-running)
                        │     ├─ çıktı             → exit code
                        │     ├─ Step.Timeout doldu → ContainerStop → ErrTimeout
                        │     └─ ctx iptal edildi   → ContainerStop → context.Canceled
                        │
                        ├─ logların bitmesini bekle (ctx iptal edildiyse kes)
                        └─ ContainerRemove (force, volumes)  → Wait() döner
```

- **Pull:** Her pull'da registry'ye gidilir. Her ilerleme mesajı `OnPullProgress`'e iletilir.
- **Loglar:** Docker'ın multiplexed stream'i `stdcopy` ile ayrılır, iki `io.Pipe` üzerinden `Stdout` ve `Stderr` olarak verilir.
- **Exit code:** Sıfırdan farklı bir exit code hata değildir; `(code, nil)` döner. Hata sadece runner'ın kendisi başarısız olduğunda döner. Bu durumda exit code `-1` olur.
- **Timeout:** Container'a `SIGTERM` gönderilir, grace period (varsayılan 10 sn, `WithStopGracePeriod`) dolunca `SIGKILL` gönderilir. Kapanış sırasında yazılan loglar korunur.
- **İptal:** Stop ve remove çağrıları `context.WithoutCancel` ile türetilmiş bir context kullanır. Böylece iptalden sonra da temizlik yapılır. Okunmayan loglar kesilir, okuyucular hata alır ve takılı kalmaz.
- **Temizlik:** Her container `io.cisid.managed=true` label'ı taşır ve iş bitince silinir.

## Hatalar

| Hata | Ne zaman |
|---|---|
| `ErrInvalidStep` | `Step.Image` boş |
| `ErrImagePull` | Pull isteği ya da pull stream'i hata verdi |
| `ErrTimeout` | `Step.Timeout` doldu |
| `context.Canceled` | `Start`'a verilen context iptal edildi |
| `ErrContainerWait` | Docker exit durumunu raporlayamadı |

Hepsi `errors.Is` ile kontrol edilir. Alttaki Docker hatası da zincirde korunur.

## Testler

`docker_test.go`, `DockerClient` arayüzünü `testify/mock` ile mock'lar:

| Test | Senaryo |
|---|---|
| `TestRunHappyPath` | Pull ilerlemesi, create ayarları (env, workdir, mount), stdout/stderr, exit 0, temizlik |
| `TestRunStartReturnsReadersAndExitCode` | `Start` + `io.Reader` + `Wait` |
| `TestRunNonZeroExitCode` | Exit 2, hata dönmez |
| `TestRunImagePullFailure` | İstek hatası, stream'de `errorDetail`, kopan stream |
| `TestRunTimeout` | Graceful stop, kapanış logları korunur |
| `TestRunContextCancellation` | İptal → stop + remove, okuyucular kapanır |
| `TestWaitHonorsCallerContext` | `Wait`'ten vazgeçmek container'ı durdurmaz |
| `TestStartFailures` | Boş image, create/start/logs hataları ve temizlik |
| `TestRunWaitError` | Daemon'un wait hatası |

Gerçek Docker'a karşı entegrasyon testleri (alpine image'ını çeker):

```bash
CISID_DOCKER_TESTS=1 go test ./internal/runner/ -run Integration -v
```
