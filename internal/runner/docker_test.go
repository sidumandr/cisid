package runner

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testImage       = "alpine:3.22"
	testContainerID = "c0ffee"
)

// mockDocker is a testify mock of DockerClient.
type mockDocker struct {
	mock.Mock
}

func (m *mockDocker) ImagePull(ctx context.Context, ref string, opts client.ImagePullOptions) (client.ImagePullResponse, error) {
	args := m.Called(ctx, ref, opts)
	resp, _ := args.Get(0).(client.ImagePullResponse)
	return resp, args.Error(1)
}

func (m *mockDocker) ContainerCreate(ctx context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(client.ContainerCreateResult), args.Error(1)
}

func (m *mockDocker) ContainerStart(ctx context.Context, id string, opts client.ContainerStartOptions) (client.ContainerStartResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(client.ContainerStartResult), args.Error(1)
}

func (m *mockDocker) ContainerLogs(ctx context.Context, id string, opts client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	args := m.Called(ctx, id, opts)
	logs, _ := args.Get(0).(client.ContainerLogsResult)
	return logs, args.Error(1)
}

func (m *mockDocker) ContainerWait(ctx context.Context, id string, opts client.ContainerWaitOptions) client.ContainerWaitResult {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(client.ContainerWaitResult)
}

func (m *mockDocker) ContainerStop(ctx context.Context, id string, opts client.ContainerStopOptions) (client.ContainerStopResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(client.ContainerStopResult), args.Error(1)
}

func (m *mockDocker) ContainerRemove(ctx context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(client.ContainerRemoveResult), args.Error(1)
}

func (m *mockDocker) VolumeRemove(ctx context.Context, id string, opts client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	args := m.Called(ctx, id, opts)
	return args.Get(0).(client.VolumeRemoveResult), args.Error(1)
}

// fakePull is an ImagePullResponse that replays msgs and then fails with err.
type fakePull struct {
	msgs []jsonstream.Message
	err  error
}

func (f *fakePull) Read([]byte) (int, error) { return 0, io.EOF }
func (f *fakePull) Close() error             { return nil }

func (f *fakePull) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(yield func(jsonstream.Message, error) bool) {
		for _, m := range f.msgs {
			if !yield(m, nil) {
				return
			}
		}
		if f.err != nil {
			yield(jsonstream.Message{}, f.err)
		}
	}
}

func (f *fakePull) Wait(ctx context.Context) error {
	for _, err := range f.JSONMessages(ctx) {
		if err != nil {
			return err
		}
	}
	return nil
}

// muxLogs encodes frames in Docker's multiplexed log format
// (8-byte header: stream, 3 zero bytes, big-endian payload size).
func muxLogs(frames ...[2]string) []byte {
	var buf bytes.Buffer
	for _, f := range frames {
		stream := byte(1)
		if f[0] == "stderr" {
			stream = 2
		}
		hdr := [8]byte{stream}
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(f[1])))
		buf.Write(hdr[:])
		buf.WriteString(f[1])
	}
	return buf.Bytes()
}

func logsOf(frames ...[2]string) client.ContainerLogsResult {
	return io.NopCloser(bytes.NewReader(muxLogs(frames...)))
}

func exited(code int64) client.ContainerWaitResult {
	res := make(chan container.WaitResponse, 1)
	res <- container.WaitResponse{StatusCode: code}
	return client.ContainerWaitResult{Result: res, Error: make(chan error)}
}

// neverExits simulates a container that keeps running.
func neverExits() client.ContainerWaitResult {
	return client.ContainerWaitResult{
		Result: make(chan container.WaitResponse),
		Error:  make(chan error),
	}
}

func notCanceled(ctx context.Context) bool { return ctx.Err() == nil }

var (
	logsOpts   = client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Follow: true}
	waitOpts   = client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning}
	removeOpts = client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}
)

func stopOpts(seconds int) client.ContainerStopOptions {
	return client.ContainerStopOptions{Timeout: &seconds}
}

func newTestRunner(m *mockDocker) *DockerRunner {
	return NewDockerRunner(m, WithLogger(slog.New(slog.DiscardHandler)))
}

func simpleStep() Step {
	return Step{Image: testImage, Cmd: []string{"sh", "-c", "true"}}
}

// expectPullCreateStart sets up a successful pull, create and start.
func expectPullCreateStart(m *mockDocker) {
	m.On("ImagePull", mock.Anything, testImage, client.ImagePullOptions{}).
		Return(&fakePull{}, nil).Once()
	m.On("ContainerCreate", mock.Anything, mock.AnythingOfType("client.ContainerCreateOptions")).
		Return(client.ContainerCreateResult{ID: testContainerID}, nil).Once()
	m.On("ContainerStart", mock.Anything, testContainerID, client.ContainerStartOptions{}).
		Return(client.ContainerStartResult{}, nil).Once()
}

func TestRunHappyPath(t *testing.T) {
	m := &mockDocker{}
	step := Step{
		Image:      testImage,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"sh", "-c", "echo hello; echo warn >&2"},
		Env:        map[string]string{"CI": "true", "BRANCH": "main"},
		WorkingDir: "/workspace",
		Mounts: []Mount{
			{Source: "/srv/cisid/src", Target: "/workspace"},
			{Type: MountVolume, Source: "cisid-cache", Target: "/cache", ReadOnly: true},
		},
	}

	var progress []PullProgress
	step.OnPullProgress = func(p PullProgress) { progress = append(progress, p) }

	m.On("ImagePull", mock.Anything, testImage, client.ImagePullOptions{}).Return(&fakePull{
		msgs: []jsonstream.Message{
			{Status: "Pulling from library/alpine", ID: "3.22"},
			{Status: "Downloading", ID: "f18232174bc9", Progress: &jsonstream.Progress{Current: 512, Total: 3624}},
			{Status: "Pull complete", ID: "f18232174bc9"},
		},
	}, nil).Once()
	m.On("ContainerCreate", mock.Anything, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      testImage,
			Entrypoint: []string{"/bin/sh", "-c"},
			Cmd:        []string{"sh", "-c", "echo hello; echo warn >&2"},
			Env:        []string{"BRANCH=main", "CI=true"},
			WorkingDir: "/workspace",
			Labels:     map[string]string{LabelManaged: "true"},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{Type: mount.TypeBind, Source: "/srv/cisid/src", Target: "/workspace"},
				{Type: mount.TypeVolume, Source: "cisid-cache", Target: "/cache", ReadOnly: true},
			},
		},
	}).Return(client.ContainerCreateResult{ID: testContainerID}, nil).Once()
	m.On("ContainerStart", mock.Anything, testContainerID, client.ContainerStartOptions{}).
		Return(client.ContainerStartResult{}, nil).Once()
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).
		Return(logsOf([2]string{"stdout", "hello\n"}, [2]string{"stderr", "warn\n"}), nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(exited(0)).Once()
	m.On("ContainerRemove", mock.MatchedBy(notCanceled), testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	var stdout, stderr bytes.Buffer
	code, err := newTestRunner(m).Run(t.Context(), step, &stdout, &stderr)

	require.NoError(t, err)
	assert.Equal(t, 0, code)
	assert.Equal(t, "hello\n", stdout.String())
	assert.Equal(t, "warn\n", stderr.String())
	assert.Equal(t, []PullProgress{
		{Layer: "3.22", Status: "Pulling from library/alpine"},
		{Layer: "f18232174bc9", Status: "Downloading", Current: 512, Total: 3624},
		{Layer: "f18232174bc9", Status: "Pull complete"},
	}, progress)
	m.AssertExpectations(t)
	m.AssertNotCalled(t, "ContainerStop", mock.Anything, mock.Anything, mock.Anything)
}

func TestRunStartReturnsReadersAndExitCode(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).
		Return(logsOf([2]string{"stdout", "line 1\n"}, [2]string{"stdout", "line 2\n"}), nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(exited(0)).Once()
	m.On("ContainerRemove", mock.Anything, testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	exec, err := newTestRunner(m).Start(t.Context(), simpleStep())
	require.NoError(t, err)
	assert.Equal(t, testContainerID, exec.ContainerID)

	errc := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, exec.Stderr)
		errc <- err
	}()
	out, err := io.ReadAll(exec.Stdout)
	require.NoError(t, err)
	require.NoError(t, <-errc)
	assert.Equal(t, "line 1\nline 2\n", string(out))

	code, err := exec.Wait(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 0, code)
	m.AssertExpectations(t)
}

func TestRunNonZeroExitCode(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).
		Return(logsOf([2]string{"stderr", "go: build failed\n"}), nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(exited(2)).Once()
	m.On("ContainerRemove", mock.Anything, testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	var stderr bytes.Buffer
	code, err := newTestRunner(m).Run(t.Context(), simpleStep(), io.Discard, &stderr)

	require.NoError(t, err, "a failing command is a result, not a runner error")
	assert.Equal(t, 2, code)
	assert.Equal(t, "go: build failed\n", stderr.String())
	m.AssertExpectations(t)
	m.AssertNotCalled(t, "ContainerStop", mock.Anything, mock.Anything, mock.Anything)
}

func TestRunImagePullFailure(t *testing.T) {
	registryErr := errors.New("dial tcp: lookup registry-1.docker.io: no such host")

	tests := []struct {
		name    string
		resp    client.ImagePullResponse
		callErr error
		wantErr error
	}{
		{
			name:    "pull request fails",
			callErr: registryErr,
			wantErr: registryErr,
		},
		{
			name: "pull stream reports error",
			resp: &fakePull{msgs: []jsonstream.Message{
				{Status: "Pulling from library/alpine", ID: "3.22"},
				{Error: &jsonstream.Error{Code: 404, Message: "manifest for alpine:3.22 not found"}},
			}},
		},
		{
			name:    "pull stream breaks",
			resp:    &fakePull{err: io.ErrUnexpectedEOF},
			wantErr: io.ErrUnexpectedEOF,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mockDocker{}
			m.On("ImagePull", mock.Anything, testImage, client.ImagePullOptions{}).
				Return(tt.resp, tt.callErr).Once()

			code, err := newTestRunner(m).Run(t.Context(), simpleStep(), io.Discard, io.Discard)

			assert.Equal(t, -1, code)
			require.ErrorIs(t, err, ErrImagePull)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
			m.AssertExpectations(t)
			m.AssertNotCalled(t, "ContainerCreate", mock.Anything, mock.Anything)
		})
	}
}

func TestRunTimeout(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)

	// The log stream stays open until the container is stopped.
	logsR, logsW := io.Pipe()
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).Return(logsR, nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(neverExits()).Once()
	m.On("ContainerStop", mock.MatchedBy(notCanceled), testContainerID, stopOpts(5)).
		Run(func(mock.Arguments) {
			_, _ = logsW.Write(muxLogs([2]string{"stdout", "terminated\n"}))
			_ = logsW.Close()
		}).
		Return(client.ContainerStopResult{}, nil).Once()
	m.On("ContainerRemove", mock.MatchedBy(notCanceled), testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	step := simpleStep()
	step.Timeout = 50 * time.Millisecond
	r := NewDockerRunner(m, WithStopGracePeriod(5*time.Second), WithLogger(slog.New(slog.DiscardHandler)))

	var stdout bytes.Buffer
	start := time.Now()
	code, err := r.Run(t.Context(), step, &stdout, io.Discard)

	assert.Equal(t, -1, code)
	require.ErrorIs(t, err, ErrTimeout)
	assert.NotErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second)
	assert.Equal(t, "terminated\n", stdout.String(), "logs written during shutdown must not be lost")
	m.AssertExpectations(t)
}

func TestRunContextCancellation(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)

	logsR, _ := io.Pipe() // never closed by the "daemon"
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).Return(logsR, nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(neverExits()).Once()
	m.On("ContainerStop", mock.MatchedBy(notCanceled), testContainerID, stopOpts(10)).
		Return(client.ContainerStopResult{}, nil).Once()
	m.On("ContainerRemove", mock.MatchedBy(notCanceled), testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	ctx, cancel := context.WithCancel(t.Context())
	exec, err := newTestRunner(m).Start(ctx, simpleStep())
	require.NoError(t, err)

	cancel()

	done := make(chan struct{})
	var code int
	go func() {
		defer close(done)
		code, err = exec.Wait(t.Context())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after context cancellation")
	}

	assert.Equal(t, -1, code)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotErrorIs(t, err, ErrTimeout)

	// Readers are closed so consumers do not hang.
	_, readErr := io.ReadAll(exec.Stdout)
	assert.Error(t, readErr)
	m.AssertExpectations(t)
}

func TestWaitHonorsCallerContext(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)
	logsR, logsW := io.Pipe()
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).Return(logsR, nil).Once()
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).Return(neverExits()).Once()
	m.On("ContainerStop", mock.Anything, testContainerID, stopOpts(10)).
		Run(func(mock.Arguments) { _ = logsW.Close() }).
		Return(client.ContainerStopResult{}, nil).Once()
	m.On("ContainerRemove", mock.Anything, testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	runCtx, stopRun := context.WithCancel(t.Context())
	exec, err := newTestRunner(m).Start(runCtx, simpleStep())
	require.NoError(t, err)

	waitCtx, cancelWait := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelWait()
	_, err = exec.Wait(waitCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	// Giving up on Wait does not stop the container; canceling the run does.
	stopRun()
	_, err = exec.Wait(t.Context())
	require.ErrorIs(t, err, context.Canceled)
	m.AssertExpectations(t)
}

func TestStartFailures(t *testing.T) {
	daemonErr := errors.New("Error response from daemon: conflict")

	t.Run("empty image", func(t *testing.T) {
		m := &mockDocker{}
		_, err := newTestRunner(m).Start(t.Context(), Step{})
		require.ErrorIs(t, err, ErrInvalidStep)
		m.AssertNotCalled(t, "ImagePull", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("create fails", func(t *testing.T) {
		m := &mockDocker{}
		m.On("ImagePull", mock.Anything, testImage, client.ImagePullOptions{}).Return(&fakePull{}, nil).Once()
		m.On("ContainerCreate", mock.Anything, mock.AnythingOfType("client.ContainerCreateOptions")).
			Return(client.ContainerCreateResult{}, daemonErr).Once()

		_, err := newTestRunner(m).Start(t.Context(), simpleStep())
		require.ErrorIs(t, err, daemonErr)
		m.AssertExpectations(t)
		m.AssertNotCalled(t, "ContainerStart", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("start fails removes container", func(t *testing.T) {
		m := &mockDocker{}
		m.On("ImagePull", mock.Anything, testImage, client.ImagePullOptions{}).Return(&fakePull{}, nil).Once()
		m.On("ContainerCreate", mock.Anything, mock.AnythingOfType("client.ContainerCreateOptions")).
			Return(client.ContainerCreateResult{ID: testContainerID}, nil).Once()
		m.On("ContainerStart", mock.Anything, testContainerID, client.ContainerStartOptions{}).
			Return(client.ContainerStartResult{}, daemonErr).Once()
		m.On("ContainerRemove", mock.MatchedBy(notCanceled), testContainerID, removeOpts).
			Return(client.ContainerRemoveResult{}, nil).Once()

		_, err := newTestRunner(m).Start(t.Context(), simpleStep())
		require.ErrorIs(t, err, daemonErr)
		m.AssertExpectations(t)
	})

	t.Run("logs fail stops and removes container", func(t *testing.T) {
		m := &mockDocker{}
		expectPullCreateStart(m)
		m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).Return(nil, daemonErr).Once()
		m.On("ContainerStop", mock.MatchedBy(notCanceled), testContainerID, stopOpts(10)).
			Return(client.ContainerStopResult{}, nil).Once()
		m.On("ContainerRemove", mock.MatchedBy(notCanceled), testContainerID, removeOpts).
			Return(client.ContainerRemoveResult{}, nil).Once()

		_, err := newTestRunner(m).Start(t.Context(), simpleStep())
		require.ErrorIs(t, err, daemonErr)
		m.AssertExpectations(t)
	})
}

func TestRemoveVolume(t *testing.T) {
	daemonErr := errors.New("volume is in use")
	opts := client.VolumeRemoveOptions{Force: true}

	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{name: "removed"},
		{name: "missing volume is not an error", err: fmt.Errorf("get cisid-ws: %w", cerrdefs.ErrNotFound)},
		{name: "daemon error is returned", err: daemonErr, wantErr: daemonErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mockDocker{}
			m.On("VolumeRemove", mock.Anything, "cisid-ws", opts).
				Return(client.VolumeRemoveResult{}, tt.err).Once()

			err := newTestRunner(m).RemoveVolume(t.Context(), "cisid-ws")

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			m.AssertExpectations(t)
		})
	}
}

func TestRunWaitError(t *testing.T) {
	m := &mockDocker{}
	expectPullCreateStart(m)
	m.On("ContainerLogs", mock.Anything, testContainerID, logsOpts).Return(logsOf(), nil).Once()

	res := make(chan container.WaitResponse, 1)
	res <- container.WaitResponse{StatusCode: 137, Error: &container.WaitExitError{Message: "container killed"}}
	m.On("ContainerWait", mock.Anything, testContainerID, waitOpts).
		Return(client.ContainerWaitResult{Result: res, Error: make(chan error)}).Once()
	m.On("ContainerStop", mock.Anything, testContainerID, stopOpts(10)).
		Return(client.ContainerStopResult{}, nil).Once()
	m.On("ContainerRemove", mock.Anything, testContainerID, removeOpts).
		Return(client.ContainerRemoveResult{}, nil).Once()

	code, err := newTestRunner(m).Run(t.Context(), simpleStep(), io.Discard, io.Discard)

	assert.Equal(t, -1, code)
	require.ErrorIs(t, err, ErrContainerWait)
	assert.True(t, strings.Contains(err.Error(), "container killed"))
	m.AssertExpectations(t)
}
