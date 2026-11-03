package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const (
	// LabelManaged marks containers created by cisid so orphans can be found.
	LabelManaged = "io.cisid.managed"

	// DefaultStopGracePeriod is how long a container gets between SIGTERM and
	// SIGKILL when a step is canceled or times out.
	DefaultStopGracePeriod = 10 * time.Second

	// cleanupSlack is added to the stop grace period to bound stop + remove.
	cleanupSlack = 30 * time.Second
)

var (
	// ErrInvalidStep is returned for a step that cannot be run.
	ErrInvalidStep = errors.New("invalid step")
	// ErrImagePull is returned when the step image cannot be pulled.
	ErrImagePull = errors.New("image pull failed")
	// ErrTimeout is returned when a step runs longer than Step.Timeout.
	ErrTimeout = errors.New("step timed out")
	// ErrContainerWait is returned when the daemon cannot report the exit status.
	ErrContainerWait = errors.New("container wait failed")
)

// DockerClient is the subset of the Docker Engine API the runner uses.
// *client.Client satisfies it.
type DockerClient interface {
	ImagePull(ctx context.Context, ref string, opts client.ImagePullOptions) (client.ImagePullResponse, error)
	ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error)
}

var _ DockerClient = (*client.Client)(nil)

// MountType selects how Mount.Source is interpreted.
type MountType string

const (
	// MountBind mounts a host path. It is the default.
	MountBind MountType = "bind"
	// MountVolume mounts a named Docker volume.
	MountVolume MountType = "volume"
)

// Mount attaches a host path or volume to the step container.
type Mount struct {
	Type     MountType
	Source   string
	Target   string
	ReadOnly bool
}

// PullProgress is one progress update while pulling an image.
type PullProgress struct {
	Layer   string
	Status  string
	Current int64
	Total   int64
}

// Step describes a single container to run.
type Step struct {
	Image string
	// Entrypoint overrides the image entrypoint when set.
	Entrypoint []string
	Cmd        []string
	Env        map[string]string
	WorkingDir string
	Mounts     []Mount
	// Timeout limits the container run time; zero means no limit.
	Timeout time.Duration
	// OnPullProgress, if set, is called for every image pull update.
	OnPullProgress func(PullProgress)
}

// Option configures a DockerRunner.
type Option func(*DockerRunner)

// WithStopGracePeriod sets the SIGTERM-to-SIGKILL delay used on cancel/timeout.
func WithStopGracePeriod(d time.Duration) Option {
	return func(r *DockerRunner) { r.stopGrace = d }
}

// WithLogger sets the logger used for cleanup failures.
func WithLogger(log *slog.Logger) Option {
	return func(r *DockerRunner) { r.log = log }
}

// DockerRunner runs steps in Docker containers.
type DockerRunner struct {
	cli       DockerClient
	stopGrace time.Duration
	log       *slog.Logger
}

// NewDockerRunner returns a runner that talks to the daemon through cli.
func NewDockerRunner(cli DockerClient, opts ...Option) *DockerRunner {
	r := &DockerRunner{cli: cli, stopGrace: DefaultStopGracePeriod, log: slog.Default()}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Execution is a started step container.
//
// Stdout and Stderr must be read concurrently until EOF (or the run context
// canceled); otherwise the container's log stream blocks. Both readers are
// closed once the container has stopped.
type Execution struct {
	ContainerID string
	Stdout      io.Reader
	Stderr      io.Reader

	done     chan struct{}
	exitCode int
	err      error
}

// Wait blocks until the container has stopped and been removed, and returns
// its exit code. A non-zero exit code is not an error. On timeout,
// cancellation or daemon failure the exit code is -1 and err is set.
//
// ctx only bounds how long Wait blocks; to stop the container, cancel the
// context passed to Start.
func (e *Execution) Wait(ctx context.Context) (int, error) {
	select {
	case <-e.done:
		return e.exitCode, e.err
	case <-ctx.Done():
		return -1, fmt.Errorf("wait for container %s: %w", e.ContainerID, ctx.Err())
	}
}

// Run starts step, copies its output to stdout and stderr, and returns the
// exit code once the container has stopped.
func (r *DockerRunner) Run(ctx context.Context, step Step, stdout, stderr io.Writer) (int, error) {
	exec, err := r.Start(ctx, step)
	if err != nil {
		return -1, err
	}

	var wg sync.WaitGroup
	for _, c := range []struct {
		dst io.Writer
		src io.Reader
	}{{stdout, exec.Stdout}, {stderr, exec.Stderr}} {
		wg.Go(func() {
			if _, err := io.Copy(c.dst, c.src); err != nil {
				// Keep draining so a failing writer cannot block the container.
				_, _ = io.Copy(io.Discard, c.src)
			}
		})
	}

	<-exec.done
	wg.Wait()
	return exec.exitCode, exec.err
}

// Start pulls the step image, creates and starts the container, and returns
// immediately. The container is stopped gracefully when ctx is canceled or
// step.Timeout elapses, and removed once it has stopped.
func (r *DockerRunner) Start(ctx context.Context, step Step) (*Execution, error) {
	if step.Image == "" {
		return nil, fmt.Errorf("%w: image is empty", ErrInvalidStep)
	}

	if err := r.pull(ctx, step); err != nil {
		return nil, err
	}

	created, err := r.cli.ContainerCreate(ctx, createOptions(step))
	if err != nil {
		return nil, fmt.Errorf("create container for %s: %w", step.Image, err)
	}
	id := created.ID

	if _, err := r.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		r.cleanup(ctx, id, false)
		return nil, fmt.Errorf("start container %s: %w", id, err)
	}

	logs, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		r.cleanup(ctx, id, true)
		return nil, fmt.Errorf("attach logs of container %s: %w", id, err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	exec := &Execution{
		ContainerID: id,
		Stdout:      stdoutR,
		Stderr:      stderrR,
		done:        make(chan struct{}),
	}

	logsDone := make(chan struct{})
	go func() {
		defer close(logsDone)
		defer logs.Close()
		_, err := stdcopy.StdCopy(stdoutW, stderrW, logs)
		stdoutW.CloseWithError(err)
		stderrW.CloseWithError(err)
	}()

	abortLogs := func(cause error) {
		_ = logs.Close()
		stdoutR.CloseWithError(cause)
		stderrR.CloseWithError(cause)
	}

	go r.supervise(ctx, exec, step.Timeout, logsDone, abortLogs)
	return exec, nil
}

// supervise waits for the container to exit, enforces the timeout, stops the
// container on cancellation and removes it. It closes exec.done when finished.
func (r *DockerRunner) supervise(ctx context.Context, exec *Execution, timeout time.Duration,
	logsDone <-chan struct{}, abortLogs func(error)) {
	defer close(exec.done)

	runCtx, cancelRun := ctx, context.CancelFunc(func() {})
	if timeout > 0 {
		runCtx, cancelRun = context.WithTimeout(ctx, timeout)
	}
	defer cancelRun()

	code, err := r.waitExit(runCtx, exec.ContainerID)
	if err != nil {
		code = -1
		switch {
		case ctx.Err() != nil:
			err = fmt.Errorf("run container %s: %w", exec.ContainerID, ctx.Err())
		case runCtx.Err() != nil:
			err = fmt.Errorf("%w after %s", ErrTimeout, timeout)
		}
	}

	if err != nil {
		r.stop(ctx, exec.ContainerID)
	}
	// Keep the logs of a timed-out step; drop them only if the caller is gone.
	select {
	case <-logsDone:
	case <-ctx.Done():
		abortLogs(ctx.Err())
		<-logsDone
	}

	r.cleanup(ctx, exec.ContainerID, false)
	exec.exitCode, exec.err = code, err
}

func (r *DockerRunner) waitExit(ctx context.Context, id string) (int, error) {
	res := r.cli.ContainerWait(ctx, id, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})
	select {
	case resp := <-res.Result:
		if resp.Error != nil && resp.Error.Message != "" {
			return -1, fmt.Errorf("%w: container %s: %s", ErrContainerWait, id, resp.Error.Message)
		}
		return int(resp.StatusCode), nil
	case err := <-res.Error:
		return -1, fmt.Errorf("%w: container %s: %w", ErrContainerWait, id, err)
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (r *DockerRunner) pull(ctx context.Context, step Step) error {
	resp, err := r.cli.ImagePull(ctx, step.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrImagePull, step.Image, err)
	}
	defer resp.Close()

	for msg, err := range resp.JSONMessages(ctx) {
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrImagePull, step.Image, err)
		}
		if msg.Error != nil {
			return fmt.Errorf("%w: %s: %w", ErrImagePull, step.Image, msg.Error)
		}
		if step.OnPullProgress != nil {
			p := PullProgress{Layer: msg.ID, Status: msg.Status}
			if msg.Progress != nil {
				p.Current, p.Total = msg.Progress.Current, msg.Progress.Total
			}
			step.OnPullProgress(p)
		}
	}
	return nil
}

// stop sends SIGTERM and, after the grace period, SIGKILL. It uses a context
// detached from ctx's cancellation so shutdown still happens after a cancel.
func (r *DockerRunner) stop(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.stopGrace+cleanupSlack)
	defer cancel()

	secs := int(r.stopGrace.Seconds())
	if _, err := r.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs}); err != nil {
		r.log.WarnContext(ctx, "stop container", "container", id, "error", err)
	}
}

// cleanup force-removes the container, stopping it first if requested.
func (r *DockerRunner) cleanup(ctx context.Context, id string, stopFirst bool) {
	if stopFirst {
		r.stop(ctx, id)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupSlack)
	defer cancel()

	opts := client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}
	if _, err := r.cli.ContainerRemove(ctx, id, opts); err != nil {
		r.log.WarnContext(ctx, "remove container", "container", id, "error", err)
	}
}

// RemoveVolume force-removes a named volume, e.g. a job workspace. Removing a
// volume that does not exist is not an error.
func (r *DockerRunner) RemoveVolume(ctx context.Context, name string) error {
	_, err := r.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

func createOptions(step Step) client.ContainerCreateOptions {
	var env []string
	for _, k := range slices.Sorted(maps.Keys(step.Env)) {
		env = append(env, k+"="+step.Env[k])
	}

	var mounts []mount.Mount
	for _, m := range step.Mounts {
		typ := mount.TypeBind
		if m.Type == MountVolume {
			typ = mount.TypeVolume
		}
		mounts = append(mounts, mount.Mount{
			Type:     typ,
			Source:   m.Source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly,
		})
	}

	return client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      step.Image,
			Entrypoint: step.Entrypoint,
			Cmd:        step.Cmd,
			Env:        env,
			WorkingDir: step.WorkingDir,
			Labels:     map[string]string{LabelManaged: "true"},
		},
		HostConfig: &container.HostConfig{Mounts: mounts},
	}
}
