package status

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestDescribePullRequestHasItsOwnContext(t *testing.T) {
	pr := job(1, models.JobStatusSuccess)
	pr.Event, pr.PullRequest = models.EventPullRequest, 7
	assert.Equal(t, "cisid/pr", Describe(pr).Context, "apart from a push build of the same commit")

	pr.Matrix = map[string]string{"GO": "1.27"}
	assert.Equal(t, "cisid/pr (GO=1.27)", Describe(pr).Context)

	assert.Equal(t, "cisid", Describe(job(2, models.JobStatusSuccess)).Context)
}
