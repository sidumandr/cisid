package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArtifacts(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
steps:
  - {name: build, image: alpine, commands: [true]}
artifacts: [dist/*.tar.gz, coverage.out, reports]
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"dist/*.tar.gz", "coverage.out", "reports"}, cfg.Artifacts)

	_, err = ParseConfig([]byte(`
steps:
  - {name: artifacts, image: alpine, commands: [true]}
artifacts: [/etc/passwd, ../secret, ./dist, dist/, a, a, "x\\y"]
`))
	require.ErrorIs(t, err, ErrInvalidConfig)
	for _, want := range []string{
		`steps[0]: name "artifacts" is reserved`,
		`artifacts[0]: "/etc/passwd" must be a clean path inside the workspace`,
		`artifacts[1]: "../secret" must be`,
		`artifacts[2]: "./dist" must be`,
		`artifacts[3]: "dist/" must be`,
		`artifacts[5]: "a" is listed twice`,
		`artifacts[6]: "x\\y" must be`,
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestPlanCollectsArtifactsLast(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - {name: build, image: golang:1.27, commands: [make]}
  - {name: test, image: golang:1.27, commands: [make test]}
artifacts: [dist/*, coverage.out]
`)}
	steps, err := stepsOf(Planner{Configs: configs, CloneImage: "alpine/git:test"}.Plan(t.Context(), testJob()))
	require.NoError(t, err)
	require.Len(t, steps, 4)
	last := steps[3]
	assert.Equal(t, "artifacts", last.Name)
	assert.True(t, last.Artifacts)
	assert.False(t, steps[2].Artifacts)
	assert.Equal(t, "alpine/git:test", last.Image, "the clone image has sh, find and tar")
	assert.Equal(t, "dist/*\ncoverage.out", last.Env["CISID_ARTIFACT_PATHS"])
	assert.Nil(t, last.DependsOn, "in order: after the last step")
	script := strings.Join(last.Commands, "\n")
	assert.NotContains(t, script, "dist/", "paths reach the script only through the environment")
	assert.Contains(t, script, `"$CISID_ARTIFACT_UPLOAD" != 1`)
}

func TestPlanCollectsArtifactsAfterParallelSteps(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - {name: lint, image: alpine, commands: [true]}
  - {name: build, image: alpine, commands: [true], depends_on: []}
artifacts: [dist]
`)}
	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), testJob()))
	require.NoError(t, err)
	require.Len(t, steps, 4)
	assert.Equal(t, []int{0, 1, 2}, steps[3].DependsOn, "it waits for every step")
}

func TestPlanWithoutArtifactsHasNoCollectStep(t *testing.T) {
	configs := &fakeConfigs{data: []byte("steps:\n  - {name: a, image: alpine, commands: [true]}\n")}
	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), testJob()))
	require.NoError(t, err)
	for _, s := range steps {
		assert.False(t, s.Artifacts)
		assert.NotEqual(t, "artifacts", s.Name)
	}
}
