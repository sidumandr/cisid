package status

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestDescribeManualAndScheduledBuilds(t *testing.T) {
	for event, want := range map[string]string{
		models.EventManual:   "cisid/manual",
		models.EventSchedule: "cisid/schedule",
	} {
		j := job(1, models.JobStatusSuccess)
		j.Event = event
		assert.Equal(t, want, Describe(j).Context, "apart from a push build of the same commit")
	}
}
