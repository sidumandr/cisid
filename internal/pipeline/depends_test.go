package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestParseDependsOn(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
steps:
  - {name: a, image: alpine, commands: [true]}
  - {name: b, image: alpine, commands: [true]}
`))
	require.NoError(t, err)
	assert.False(t, cfg.Parallel, "without depends_on steps run one after another")
	assert.Nil(t, cfg.Steps[1].DependsOn)

	cfg, err = ParseConfig([]byte(`
steps:
  - {name: lint, image: alpine, commands: [true], depends_on: []}
  - {name: build, image: alpine, commands: [true], depends_on: clone}
  - {name: test, image: alpine, commands: [true], depends_on: [build, lint]}
`))
	require.NoError(t, err)
	assert.True(t, cfg.Parallel)
	assert.Equal(t, []string{}, cfg.Steps[0].DependsOn)
	assert.Equal(t, []string{"clone"}, cfg.Steps[1].DependsOn, "a single name is a list of one")
	assert.Equal(t, []string{"build", "lint"}, cfg.Steps[2].DependsOn)

	_, err = ParseConfig([]byte(`
steps:
  - {name: a, image: alpine, commands: [true], depends_on: [a]}
  - {name: b, image: alpine, commands: [true], depends_on: [c]}
  - {name: c, image: alpine, commands: [true], depends_on: [nope, a, a]}
`))
	require.ErrorIs(t, err, ErrInvalidConfig)
	for _, want := range []string{
		`steps[0]: depends_on: a step cannot depend on itself`,
		`steps[1]: depends_on: "c" must be written before this step`,
		`steps[2]: depends_on: there is no step "nope"`,
		`steps[2]: depends_on: "a" is listed twice`,
	} {
		assert.Contains(t, err.Error(), want)
	}
}

func TestPlanDependencies(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - {name: lint, image: alpine, commands: [true]}
  - {name: build, image: alpine, commands: [true], depends_on: []}
  - {name: unit, image: alpine, commands: [true], depends_on: [build]}
  - {name: e2e, image: alpine, commands: [true], depends_on: [build], when: {branch: main}}
  - {name: report, image: alpine, commands: [true], depends_on: [lint, unit, e2e]}
  - {name: publish, image: alpine, commands: [true], depends_on: [e2e], when: {event: push}}
`)}
	job := testJob() // on feature/webhook: e2e does not run

	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), job))
	require.NoError(t, err)
	names := make([]string, len(steps))
	deps := map[string][]int{}
	for i, s := range steps {
		names[i] = s.Name
		deps[s.Name] = s.DependsOn
	}
	assert.Equal(t, []string{"clone", "lint", "build", "unit", "report", "publish"}, names)
	assert.Nil(t, deps["clone"])
	assert.Equal(t, []int{0}, deps["lint"], "a step without depends_on waits for the clone only")
	assert.Equal(t, []int{0}, deps["build"])
	assert.Equal(t, []int{2}, deps["unit"])
	assert.Equal(t, []int{1, 2, 3}, deps["report"], "the skipped e2e passes on its dependency, build")
	assert.Equal(t, []int{2}, deps["publish"])

	for i := range steps {
		for _, d := range models.Needs(steps, i) {
			assert.Less(t, d, i, "steps wait only for earlier steps")
		}
	}
}

func TestPlanWithoutDependenciesRunsInOrder(t *testing.T) {
	configs := &fakeConfigs{data: []byte(`
steps:
  - {name: a, image: alpine, commands: [true]}
  - {name: b, image: alpine, commands: [true]}
`)}
	steps, err := stepsOf(Planner{Configs: configs}.Plan(t.Context(), testJob()))
	require.NoError(t, err)
	for i, s := range steps {
		assert.Nil(t, s.DependsOn, s.Name)
		if i > 0 {
			assert.Equal(t, []int{i - 1}, models.Needs(steps, i))
		}
	}
	assert.Nil(t, models.Needs(steps, 0))
}
