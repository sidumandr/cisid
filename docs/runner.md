# Docker Runner

**English** · [Türkçe](tr/runner.md)

`internal/runner/docker.go` runs a pipeline step (`Step`) in a Docker container. It uses `github.com/moby/moby/client` as the SDK (see [CLAUDE.md](../CLAUDE.md)).

## Usage

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

To read the logs yourself (e.g. to forward them over WebSocket), use `Start`:

```go
exec, err := r.Start(ctx, step)
if err != nil {
	return err
}
go stream(exec.Stdout) // Stdout and Stderr must be read concurrently
go stream(exec.Stderr)
code, err := exec.Wait(ctx)
```

## Lifecycle

```
Start ─► ImagePull ─► ContainerCreate ─► ContainerStart ─► ContainerLogs (follow)
                                                             │
                      supervise goroutine ◄──────────────────┘
                        │
                        ├─ ContainerWait (not-running)
                        │     ├─ exited               → exit code
                        │     ├─ Step.Timeout expired → ContainerStop → ErrTimeout
                        │     └─ ctx canceled         → ContainerStop → context.Canceled
                        │
                        ├─ wait for the logs to end (cut them off if ctx is canceled)
                        └─ ContainerRemove (force, volumes)  → Wait() returns
```

- **Pull:** Every pull goes to the registry. Each progress message is passed to `OnPullProgress`.
- **Logs:** Docker's multiplexed stream is split with `stdcopy` and exposed as `Stdout` and `Stderr` through two `io.Pipe`s.
- **Exit code:** A non-zero exit code is not an error: the call returns `(code, nil)`. An error is returned only when the runner itself fails. In that case the exit code is `-1`.
- **Timeout:** The container gets `SIGTERM`, then `SIGKILL` when the grace period (10 s by default, `WithStopGracePeriod`) runs out. Logs written during shutdown are kept.
- **Cancellation:** The stop and remove calls use a context derived with `context.WithoutCancel`. So cleanup still happens after cancellation. Unread logs are cut off, and readers get an error instead of hanging.
- **Cleanup:** Every container carries the `io.cisid.managed=true` label and is removed when the work is done.

## Errors

| Error | When |
|---|---|
| `ErrInvalidStep` | `Step.Image` is empty |
| `ErrImagePull` | The pull request or the pull stream returned an error |
| `ErrTimeout` | `Step.Timeout` expired |
| `context.Canceled` | The context passed to `Start` was canceled |
| `ErrContainerWait` | Docker could not report the exit status |

Check all of them with `errors.Is`. The underlying Docker error stays in the chain too.

## Tests

`docker_test.go` mocks the `DockerClient` interface with `testify/mock`:

| Test | Scenario |
|---|---|
| `TestRunHappyPath` | Pull progress, create settings (env, workdir, mount), stdout/stderr, exit 0, cleanup |
| `TestRunStartReturnsReadersAndExitCode` | `Start` + `io.Reader` + `Wait` |
| `TestRunNonZeroExitCode` | Exit 2, no error returned |
| `TestRunImagePullFailure` | Request error, `errorDetail` in the stream, broken stream |
| `TestRunTimeout` | Graceful stop, shutdown logs are kept |
| `TestRunContextCancellation` | Cancellation → stop + remove, readers close |
| `TestWaitHonorsCallerContext` | Giving up on `Wait` does not stop the container |
| `TestStartFailures` | Empty image, create/start/logs errors and cleanup |
| `TestRunWaitError` | The daemon's wait error |

Integration tests against real Docker (they pull the alpine image):

```bash
CISID_DOCKER_TESTS=1 go test ./internal/runner/ -run Integration -v
```
