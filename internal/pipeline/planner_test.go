package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/sidumandr/cisid/internal/secrets"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

func testJob() models.Job {
	return models.Job{
		ID:        42,
		Repo:      "sidumandr/cisid",
		CloneURL:  "https://github.com/sidumandr/cisid.git",
		Branch:    "feature/webhook",
		CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
	}
}

func TestPlanClonesTheCommit(t *testing.T) {
	steps, err := stepsOf(Planner{}.Plan(t.Context(), testJob()))

	require.NoError(t, err)
	require.Len(t, steps, 1)
	assert.Equal(t, models.StepSpec{
		Name:  "clone",
		Image: DefaultCloneImage,
		Commands: []string{
			"git init -q .",
			`git remote add origin "$CISID_CLONE_URL"`,
			`git fetch -q --depth=1 origin "$CISID_COMMIT_SHA"`,
			`git checkout -q -B "$CISID_BRANCH" FETCH_HEAD`,
			"git log -1 --format='%H %s'",
		},
		Env: map[string]string{
			"CISID_REPO":          "sidumandr/cisid",
			"CISID_CLONE_URL":     "https://github.com/sidumandr/cisid.git",
			"CISID_BRANCH":        "feature/webhook",
			"CISID_COMMIT_SHA":    "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
			"GIT_TERMINAL_PROMPT": "0",
		},
		TimeoutSeconds: 600,
	}, steps[0])
}

// fakeConfigs serves a fixed .cisid.yml or error.
type fakeConfigs struct {
	data []byte
	err  error
	got  []string
}

func (f *fakeConfigs) FileContent(_ context.Context, repo, ref, path string) ([]byte, error) {
	f.got = append(f.got, repo, ref, path)
	return f.data, f.err
}

func TestPlanAppendsConfigSteps(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - name: test
    image: golang:1.27
    commands: [go vet ./..., go test ./...]
    environment:
      CGO_ENABLED: 0
    timeout: 15m
`)}
	job := testJob()

	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), job))

	require.NoError(t, err)
	assert.Equal(t, []string{"sidumandr/cisid", job.CommitSHA, ".cisid.yml"}, configs.got,
		"the config is read at the pushed commit")
	require.Len(t, steps, 2)
	assert.Equal(t, "clone", steps[0].Name)
	assert.Equal(t, models.StepSpec{
		Name:     "test",
		Image:    "golang:1.27",
		Commands: []string{"go vet ./...", "go test ./..."},
		Env: map[string]string{
			"CGO_ENABLED":      "0",
			"CISID_REPO":       "sidumandr/cisid",
			"CISID_BRANCH":     "feature/webhook",
			"CISID_COMMIT_SHA": job.CommitSHA,
		},
		TimeoutSeconds: 900,
	}, steps[1])
}

func TestPlanConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		configs *fakeConfigs
		want    error
		notWant error
	}{
		{name: "no config file", configs: &fakeConfigs{err: fmt.Errorf("get: %w", github.ErrNotFound)}, want: ErrNoConfig, notWant: ErrTemporary},
		{name: "GitHub down", configs: &fakeConfigs{err: fmt.Errorf("get: %w", github.ErrTemporary)}, want: ErrTemporary},
		{name: "permanent GitHub error", configs: &fakeConfigs{err: errors.New("unexpected response 401")}, notWant: ErrTemporary},
		{name: "invalid config", configs: &fakeConfigs{data: []byte("steps: []")}, want: ErrInvalidConfig, notWant: ErrTemporary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Planner{Configs: tt.configs}.Plan(t.Context(), testJob())

			require.Error(t, err)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
			}
			if tt.notWant != nil {
				assert.NotErrorIs(t, err, tt.notWant)
			}
		})
	}
}

func TestPlanCanceledFetchIsTemporary(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Planner{Configs: &fakeConfigs{err: context.Canceled}}.Plan(ctx, testJob())

	assert.ErrorIs(t, err, ErrTemporary, "shutting down must not fail the job")
}

func TestPlanUsesConfiguredCloneImage(t *testing.T) {
	steps, err := stepsOf(Planner{CloneImage: "registry.local/git:2"}.Plan(t.Context(), testJob()))

	require.NoError(t, err)
	assert.Equal(t, "registry.local/git:2", steps[0].Image)
}

func TestPlanKeepsPayloadOutOfTheScript(t *testing.T) {
	job := testJob()
	job.CloneURL = `https://example.com/x.git"; rm -rf / #`
	job.Branch = "$(reboot)"

	steps, err := stepsOf(Planner{}.Plan(t.Context(), job))

	require.NoError(t, err)
	script := strings.Join(steps[0].Commands, "\n")
	assert.NotContains(t, script, "rm -rf")
	assert.NotContains(t, script, "reboot")
	assert.Equal(t, job.CloneURL, steps[0].Env["CISID_CLONE_URL"])
}

func TestPlanRejectsIncompleteJob(t *testing.T) {
	for name, mutate := range map[string]func(*models.Job){
		"no clone URL":  func(j *models.Job) { j.CloneURL = "" },
		"no commit SHA": func(j *models.Job) { j.CommitSHA = "" },
		"no branch":     func(j *models.Job) { j.Branch = "" },
	} {
		t.Run(name, func(t *testing.T) {
			job := testJob()
			mutate(&job)
			_, err := Planner{}.Plan(t.Context(), job)
			assert.ErrorIs(t, err, ErrInvalidJob)
		})
	}
}

// TestDockerIntegrationClone runs the clone step against GitHub. Skipped
// unless CISID_DOCKER_TESTS=1. CISID_TEST_CLONE_IMAGE overrides the image.
func TestDockerIntegrationClone(t *testing.T) {
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}
	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	// The repository's first commit, which only contains go.mod and .gitignore.
	const sha = "015857938dbfb83c919c7c6603363e29bb61df3e"
	steps, err := stepsOf(Planner{CloneImage: os.Getenv("CISID_TEST_CLONE_IMAGE")}.Plan(t.Context(), models.Job{
		Repo:      "sidumandr/cisid",
		CloneURL:  "https://github.com/sidumandr/cisid.git",
		Branch:    "main",
		CommitSHA: sha,
	}))
	require.NoError(t, err)
	step := steps[0]

	// Same shape as the agent runs it.
	var stdout, stderr bytes.Buffer
	code, err := runner.NewDockerRunner(cli).Run(t.Context(), runner.Step{
		Image:      step.Image,
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{"set -ex\n" + strings.Join(step.Commands, "\n") + "\ntest -f go.mod"},
		Env:        step.Env,
		WorkingDir: "/workspace",
		Timeout:    2 * time.Minute,
	}, &stdout, &stderr)

	require.NoError(t, err)
	require.Equal(t, 0, code, "stderr:\n%s", stderr.String())
	assert.Equal(t, sha+" Initialize Go module\n", stdout.String())
}

func TestPlanSkipsStepsForOtherBranches(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - {name: test, image: golang:1.27, commands: [go test ./...]}
  - {name: deploy, image: alpine:3.22, commands: [./deploy.sh], when: {branch: main}}
`)}
	job := testJob() // pushed to feature/webhook

	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), job))
	require.NoError(t, err)
	assert.Equal(t, []string{"clone", "test"}, []string{steps[0].Name, steps[1].Name})
	assert.Len(t, steps, 2)

	job.Branch = "main"
	steps, err = stepsOf(Planner{Configs: configs}.Plan(t.Context(), job))
	require.NoError(t, err)
	assert.Len(t, steps, 3)
}

// fakeSecrets serves fixed secret values.
type fakeSecrets struct {
	values map[string]string
	err    error
	asked  []string
}

func (f *fakeSecrets) Secrets(_ context.Context, repo string, names []string) (map[string]string, error) {
	f.asked = names
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]string{}
	for _, n := range names {
		v, ok := f.values[n]
		if !ok {
			return nil, fmt.Errorf("%w: %s is not set for %s", secrets.ErrNotFound, n, repo)
		}
		out[n] = v
	}
	return out, nil
}

const secretsConfig = `
steps:
  - {name: test, image: golang:1.27, commands: [go test ./...]}
  - {name: publish, image: alpine:3.22, commands: [./publish.sh], secrets: [NPM_TOKEN]}
  - {name: deploy, image: alpine:3.22, commands: [./deploy.sh], secrets: [DEPLOY_TOKEN, NPM_TOKEN], when: {branch: main}}
`

func TestPlanGivesSecretsToTheStepsThatAsk(t *testing.T) {
	src := &fakeSecrets{values: map[string]string{"NPM_TOKEN": "npm_abc", "DEPLOY_TOKEN": "dep_xyz"}}
	job := testJob()
	job.Branch = "main"

	steps, err := stepsOf(Planner{Configs: &fakeConfigs{data: []byte(secretsConfig)}, Secrets: src}.Plan(t.Context(), job))

	require.NoError(t, err)
	require.Len(t, steps, 4)
	assert.Equal(t, []string{"DEPLOY_TOKEN", "NPM_TOKEN"}, src.asked, "each secret is read once")
	assert.Nil(t, steps[1].Secrets, "steps get only what they ask for")
	assert.Equal(t, map[string]string{"NPM_TOKEN": "npm_abc"}, steps[2].Secrets)
	assert.Equal(t, map[string]string{"DEPLOY_TOKEN": "dep_xyz", "NPM_TOKEN": "npm_abc"}, steps[3].Secrets)
	assert.NotContains(t, steps[3].Env, "DEPLOY_TOKEN", "secrets stay out of the plain environment")
}

func TestPlanSecretsOnlyForStepsThatRun(t *testing.T) {
	src := &fakeSecrets{values: map[string]string{"NPM_TOKEN": "npm_abc"}} // no DEPLOY_TOKEN

	_, err := Planner{Configs: &fakeConfigs{data: []byte(secretsConfig)}, Secrets: src}.Plan(t.Context(), testJob())

	require.NoError(t, err, "deploy is skipped on this branch, so its secret is not needed")
	assert.Equal(t, []string{"NPM_TOKEN"}, src.asked)
}

func TestPlanSecretErrors(t *testing.T) {
	configs := &fakeConfigs{data: []byte(secretsConfig)}

	_, err := Planner{Configs: configs, Secrets: &fakeSecrets{}}.Plan(t.Context(), testJob())
	assert.ErrorIs(t, err, secrets.ErrNotFound)
	assert.NotErrorIs(t, err, ErrTemporary)

	_, err = Planner{Configs: configs}.Plan(t.Context(), testJob())
	assert.ErrorIs(t, err, ErrInvalidConfig, "secrets requested but none configured")

	_, err = Planner{Configs: configs, Secrets: &fakeSecrets{err: errors.New("connection refused")}}.Plan(t.Context(), testJob())
	assert.ErrorIs(t, err, ErrTemporary)
}

// stepsOf returns the steps of a plan.
func stepsOf(a models.JobAssignment, err error) ([]models.StepSpec, error) {
	return a.Steps, err
}

func TestPlanIncludesServices(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
services:
  - name: postgres
    image: postgres:18-alpine
    environment:
      POSTGRES_PASSWORD: test
  - name: cache
    image: redis:8-alpine
    command: [redis-server, --save, ""]
steps:
  - {name: test, image: golang:1.27, commands: [go test ./...]}
`)}

	plan, err := Planner{Configs: configs}.Plan(t.Context(), testJob())

	require.NoError(t, err)
	assert.Len(t, plan.Steps, 2)
	assert.Equal(t, []models.ServiceSpec{
		{Name: "postgres", Image: "postgres:18-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test"}},
		{Name: "cache", Image: "redis:8-alpine", Command: []string{"redis-server", "--save", ""}},
	}, plan.Services)
}
