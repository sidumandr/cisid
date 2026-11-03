package runner

import (
	"bytes"
	"os"
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
