package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

// TestDockerIntegrationJob runs a real two-step job through the agent. It is
// skipped unless CISID_DOCKER_TESTS=1, since it pulls images.
func TestDockerIntegrationJob(t *testing.T) {
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}
	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	fs := newFakeServer(t)
	startAgent(t, fs, runner.NewDockerRunner(cli))
	sc := fs.accept(t)

	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 100,
		Steps: []models.StepSpec{
			{Name: "write", Image: "alpine:3.22", Commands: []string{"echo built-by-$CISID_STEP > artifact.txt"}},
			{Name: "read", Image: "alpine:3.22", Commands: []string{"pwd", "cat artifact.txt", "exit 4"}},
		},
	})
	logs, res := sc.collectJob(t, 100)

	var stdout strings.Builder
	for _, l := range logs {
		if l.Step == 1 && l.Stream == models.StreamStdout {
			stdout.WriteString(l.Data)
		}
	}
	assert.Equal(t, "/workspace\nbuilt-by-write\n", stdout.String(), "steps share the workspace volume")
	assert.Equal(t, models.JobResult{
		JobID: 100, Status: models.JobStatusFailed, ExitCode: 4, FailedStep: intPtr(1),
	}, res)
}

func TestDockerIntegrationJobWithService(t *testing.T) {
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}
	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	fs := newFakeServer(t)
	startAgent(t, fs, runner.NewDockerRunner(cli))
	sc := fs.accept(t)

	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 101,
		Services: []models.ServiceSpec{{
			Name: "db", Image: "postgres:18-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test"},
		}},
		Steps: []models.StepSpec{{
			Name: "query", Image: "postgres:18-alpine",
			Env: map[string]string{"PGPASSWORD": "test"},
			Commands: []string{
				"for i in $(seq 60); do pg_isready -q -h db && break; sleep 0.5; done",
				"psql -h db -U postgres -tAc 'select 6 * 7'",
			},
		}},
	})
	logs, res := sc.collectJob(t, 101)

	var stdout strings.Builder
	for _, l := range logs {
		if l.Stream == models.StreamStdout {
			stdout.WriteString(l.Data)
		}
	}
	assert.Equal(t, models.JobStatusSuccess, res.Status, "error: %s", res.Error)
	assert.Equal(t, "42\n", stdout.String(), "the step reaches the service by name")
}
