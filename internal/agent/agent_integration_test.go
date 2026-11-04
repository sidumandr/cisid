package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

// TestDockerIntegrationJob runs a real two-step job through the agent. It is
// skipped unless CISID_DOCKER_TESTS=1, since it pulls images.

// dockerTimeout is how long a job on a real Docker daemon may take
// between two messages: creating containers on a busy host is slow.
const dockerTimeout = time.Minute

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
	sc.wait = dockerTimeout

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
	sc.wait = dockerTimeout

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

// configSource serves a fixed .cisid.yml to the planner.
type configSource string

func (c configSource) FileContent(context.Context, string, string, string) ([]byte, error) {
	return []byte(c), nil
}

// TestDockerIntegrationArtifacts collects artifacts with the step the
// planner makes, in the real clone image, and uploads them.
func TestDockerIntegrationArtifacts(t *testing.T) {
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}
	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	planner := pipeline.Planner{Configs: configSource(`
steps:
  - {name: build, image: alpine:3.22, commands: [true]}
artifacts: [dist, "*.txt", missing.log]
`)}
	plan, err := planner.Plan(t.Context(), models.Job{ID: 102, Repo: "sidumandr/cisid", CloneURL: "https://example.com/x.git",
		Branch: "main", CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246"})
	require.NoError(t, err)
	collect := plan.Assignment.Steps[len(plan.Assignment.Steps)-1]
	require.True(t, collect.Artifacts)

	fs := newFakeServer(t)
	startAgent(t, fs, runner.NewDockerRunner(cli))
	sc := fs.accept(t)
	sc.wait = dockerTimeout
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 102,
		Steps: []models.StepSpec{
			{Name: "build", Image: "alpine:3.22", Commands: []string{
				"mkdir -p dist/sub", "echo app > dist/app", "echo deep > 'dist/sub/a b.bin'", "echo note > notes.txt",
			}},
			collect,
		},
	})
	logs, res := sc.collectJob(t, 102)
	assert.Equal(t, models.JobStatusSuccess, res.Status, "error: %s", res.Error)

	got := map[string]string{}
	for _, u := range fs.uploaded() {
		got[strings.TrimPrefix(u.path, "/agent/jobs/102/artifacts/")] = u.body
	}
	assert.Equal(t, map[string]string{"dist/app": "app\n", "dist/sub/a b.bin": "deep\n", "notes.txt": "note\n"}, got)

	var log strings.Builder
	for _, l := range logs {
		if l.Step == 1 {
			log.WriteString(l.Data)
		}
	}
	assert.Contains(t, log.String(), "no file matches missing.log")
	assert.Contains(t, log.String(), "uploaded notes.txt (5 B)")
}
