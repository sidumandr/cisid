package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

const scopedConfig = `
steps:
  - {name: deploy, image: alpine:3.22, commands: [./deploy.sh], secrets: [DEPLOY_TOKEN, NPM_TOKEN]}
`

// TestPlanLimitsSecretsToBranches checks that a secret limited to branches
// only reaches jobs on them, never pull requests, whatever .cisid.yml says.
func TestPlanLimitsSecretsToBranches(t *testing.T) {
	src := &fakeSecrets{
		values:   map[string]string{"DEPLOY_TOKEN": "dep_xyz", "NPM_TOKEN": "npm_abc"},
		branches: map[string][]string{"DEPLOY_TOKEN": {"main", "release/*"}},
	}
	push := func(branch string) models.Job {
		job := testJob()
		job.Branch, job.Event = branch, models.EventPush
		return job
	}
	pr := prTestJob()
	pr.Branch = "main" // even a pull request from a branch named main

	for name, tc := range map[string]struct {
		job  models.Job
		want map[string]string
	}{
		"push to main":        {push("main"), map[string]string{"DEPLOY_TOKEN": "dep_xyz", "NPM_TOKEN": "npm_abc"}},
		"push to release/1.0": {push("release/1.0"), map[string]string{"DEPLOY_TOKEN": "dep_xyz", "NPM_TOKEN": "npm_abc"}},
		"push to feature/x":   {push("feature/x"), map[string]string{"NPM_TOKEN": "npm_abc"}},
		"pull request":        {pr, map[string]string{"NPM_TOKEN": "npm_abc"}},
		"job before events":   {models.Job{ID: 1, Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git", Branch: "main", CommitSHA: testJob().CommitSHA}, map[string]string{"DEPLOY_TOKEN": "dep_xyz", "NPM_TOKEN": "npm_abc"}},
	} {
		t.Run(name, func(t *testing.T) {
			steps, err := stepsOf(Planner{Configs: &fakeConfigs{data: []byte(scopedConfig)}, Secrets: src}.Plan(t.Context(), tc.job))
			require.NoError(t, err)
			require.Len(t, steps, 2, "the step runs either way")
			assert.Equal(t, tc.want, steps[1].Secrets)
		})
	}
}
