package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLabels(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
labels: [gpu, arm64, gpu]
steps:
  - {name: a, image: alpine, commands: [true]}
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"arm64", "gpu"}, cfg.Labels, "sorted, once each")

	cfg, err = ParseConfig([]byte("labels: region=eu\nsteps:\n  - {name: a, image: alpine, commands: [true]}\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"region=eu"}, cfg.Labels, "a single label")

	_, err = ParseConfig([]byte("labels: [\"has space\", \"-x\"]\nsteps:\n  - {name: a, image: alpine, commands: [true]}\n"))
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), `labels[0]: "has space"`)
	assert.Contains(t, err.Error(), `labels[1]: "-x"`)
}

func TestPlanCarriesLabels(t *testing.T) {
	configs := &fakeConfigs{data: []byte("labels: [arm64]\nsteps:\n  - {name: a, image: alpine, commands: [true]}\n")}
	plan, err := Planner{Configs: configs}.Plan(t.Context(), testJob())
	require.NoError(t, err)
	assert.Equal(t, []string{"arm64"}, plan.Labels)

	configs = &fakeConfigs{data: []byte("steps:\n  - {name: a, image: alpine, commands: [true]}\n")}
	plan, err = Planner{Configs: configs}.Plan(t.Context(), testJob())
	require.NoError(t, err)
	assert.Empty(t, plan.Labels, "any agent runs it")
}
