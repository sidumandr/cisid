package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

const eventsConfig = `
steps:
  - {name: test, image: golang:1.27, commands: [go test ./...], secrets: [API_KEY]}
  - name: deploy
    image: alpine:3.22
    commands: [./deploy.sh]
    secrets: [DEPLOY_TOKEN]
    when:
      event: push
      branch: main
  - name: preview
    image: alpine:3.22
    commands: [./preview.sh]
    when:
      event: pull_request
`

func prTestJob() models.Job {
	job := testJob()
	job.Event, job.PullRequest, job.TargetBranch = models.EventPullRequest, 7, "main"
	return job
}

func names(steps []models.StepSpec) []string {
	var out []string
	for _, s := range steps {
		out = append(out, s.Name)
	}
	return out
}

func TestPlanPullRequest(t *testing.T) {
	src := &fakeSecrets{values: map[string]string{"API_KEY": "key_123", "DEPLOY_TOKEN": "dep_xyz"}}
	steps, err := stepsOf(Planner{Configs: &fakeConfigs{data: []byte(eventsConfig)}, Secrets: src}.Plan(t.Context(), prTestJob()))
	require.NoError(t, err)

	assert.Equal(t, []string{"clone", "test", "preview"}, names(steps), "when.event picks the steps")
	for _, s := range steps {
		assert.Equal(t, "pull_request", s.Env["CISID_EVENT"], s.Name)
		assert.Equal(t, "7", s.Env["CISID_PULL_REQUEST"], s.Name)
		assert.Equal(t, "main", s.Env["CISID_TARGET_BRANCH"], s.Name)
		assert.Equal(t, "feature/webhook", s.Env["CISID_BRANCH"], "the pull request's own branch")
	}
	assert.Equal(t, map[string]string{"API_KEY": "key_123"}, steps[1].Secrets, "a pull request from the repository gets secrets")
}

func TestPlanPushRunsPushSteps(t *testing.T) {
	src := &fakeSecrets{values: map[string]string{"API_KEY": "key_123", "DEPLOY_TOKEN": "dep_xyz"}}
	job := testJob()
	job.Branch, job.Event = "main", models.EventPush

	steps, err := stepsOf(Planner{Configs: &fakeConfigs{data: []byte(eventsConfig)}, Secrets: src}.Plan(t.Context(), job))

	require.NoError(t, err)
	assert.Equal(t, []string{"clone", "test", "deploy"}, names(steps))
	assert.NotContains(t, steps[0].Env, "CISID_PULL_REQUEST")
	assert.Equal(t, "push", steps[0].Env["CISID_EVENT"])
}

func TestPlanForkGetsNoSecrets(t *testing.T) {
	src := &fakeSecrets{values: map[string]string{"API_KEY": "key_123"}}
	job := prTestJob()
	job.Fork = true

	steps, err := stepsOf(Planner{Configs: &fakeConfigs{data: []byte(eventsConfig)}, Secrets: src}.Plan(t.Context(), job))

	require.NoError(t, err)
	assert.Equal(t, []string{"clone", "test", "preview"}, names(steps), "the step still runs")
	assert.Nil(t, steps[1].Secrets, "but without the secret")
	assert.Nil(t, src.asked, "secrets are not even read")
}

func TestStepEvents(t *testing.T) {
	cfg, err := ParseConfig([]byte(eventsConfig))
	require.NoError(t, err)

	push := models.Job{Branch: "main"} // jobs from before events were recorded are pushes
	pr := models.Job{Branch: "main", Event: models.EventPullRequest}
	assert.True(t, cfg.Steps[0].RunsFor(push) && cfg.Steps[0].RunsFor(pr), "no when: every event")
	assert.True(t, cfg.Steps[1].RunsFor(push))
	assert.False(t, cfg.Steps[1].RunsFor(pr), "a push-only step skips pull requests, even from a branch called main")
	assert.False(t, cfg.Steps[2].RunsFor(push))
	assert.True(t, cfg.Steps[2].RunsFor(pr))
}
