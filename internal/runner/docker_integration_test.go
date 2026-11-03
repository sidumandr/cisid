package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDockerRunner connects to the local Docker daemon. The test is skipped
// unless CISID_DOCKER_TESTS=1, since it pulls images and starts containers.
func newDockerRunner(t *testing.T) *DockerRunner {
	t.Helper()
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}

	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	return NewDockerRunner(cli, WithStopGracePeriod(time.Second))
}

func TestDockerIntegrationRun(t *testing.T) {
	r := newDockerRunner(t)

	var stdout, stderr bytes.Buffer
	code, err := r.Run(t.Context(), Step{
		Image:      "alpine:3.22",
		Cmd:        []string{"sh", "-c", `echo "hello $NAME"; pwd; echo oops >&2; exit 3`},
		Env:        map[string]string{"NAME": "cisid"},
		WorkingDir: "/tmp",
	}, &stdout, &stderr)

	require.NoError(t, err)
	assert.Equal(t, 3, code)
	assert.Equal(t, "hello cisid\n/tmp\n", stdout.String())
	assert.Equal(t, "oops\n", stderr.String())
}

func TestDockerIntegrationTimeout(t *testing.T) {
	r := newDockerRunner(t)

	code, err := r.Run(t.Context(), Step{
		Image:   "alpine:3.22",
		Cmd:     []string{"sleep", "60"},
		Timeout: 2 * time.Second,
	}, &bytes.Buffer{}, &bytes.Buffer{})

	assert.Equal(t, -1, code)
	require.ErrorIs(t, err, ErrTimeout)
}

func TestDockerIntegrationService(t *testing.T) {
	r := newDockerRunner(t)
	ctx := t.Context()
	net := "cisid-it-" + strings.ToLower(rand.Text()[:8])

	require.NoError(t, r.CreateNetwork(ctx, net))
	t.Cleanup(func() { assert.NoError(t, r.RemoveNetwork(context.Background(), net)) })

	id, err := r.StartService(ctx, Service{
		Name: "db", Image: "postgres:18-alpine", Network: net,
		Env: map[string]string{"POSTGRES_PASSWORD": "test"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, r.StopService(context.Background(), id)) })

	var stdout, stderr bytes.Buffer
	code, err := r.Run(ctx, Step{
		Image:      "postgres:18-alpine",
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd: []string{`for i in $(seq 60); do pg_isready -q -h db && break; sleep 0.5; done
PGPASSWORD=test psql -h db -U postgres -tAc 'select 40 + 2'`},
		Network: net,
		Timeout: time.Minute,
	}, &stdout, &stderr)

	require.NoError(t, err)
	require.Equal(t, 0, code, "stderr: %s", stderr.String())
	assert.Equal(t, "42\n", stdout.String(), "the step reaches the service by name")
}

func TestDockerIntegrationMemoryLimit(t *testing.T) {
	r := newDockerRunner(t)
	limited := NewDockerRunner(r.cli, WithStopGracePeriod(time.Second), WithLimits(Limits{MemoryBytes: 32 << 20, Pids: 64}))

	code, err := limited.Run(t.Context(), Step{
		Image:   "alpine:3.22",
		Cmd:     []string{"tail", "/dev/zero"}, // reads forever, buffering the "line"
		Timeout: time.Minute,
	}, &bytes.Buffer{}, &bytes.Buffer{})

	assert.Equal(t, 137, code)
	require.ErrorIs(t, err, ErrOutOfMemory)
}
