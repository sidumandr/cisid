package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStepEventsManualAndSchedule(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
steps:
  - {name: nightly, image: x, commands: [true], when: {event: [schedule, manual]}}
`))
	require.NoError(t, err)

	step := cfg.Steps[0]
	assert.True(t, step.RunsFor(models.Job{Event: models.EventSchedule}))
	assert.True(t, step.RunsFor(models.Job{Event: models.EventManual}))
	assert.False(t, step.RunsFor(models.Job{Event: models.EventPush}))
}
