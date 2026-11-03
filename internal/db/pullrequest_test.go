package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func prJob(number int, sha string) models.Job {
	j := pendingJob(sha)
	j.Branch = "feature/x"
	j.Event, j.PullRequest, j.TargetBranch, j.Fork = models.EventPullRequest, number, "main", true
	return j
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
)

// TestStorePullRequestJobs checks that a pull request's fields are stored,
// that a new commit supersedes its unfinished jobs and that closing it
// cancels the rest, without touching other jobs.
func TestStorePullRequestJobs(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	first, err := s.QueueJob(ctx, prJob(7, shaA), "hash-1", "")
	require.NoError(t, err)
	running, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)
	require.Equal(t, first, running.ID)
	assert.Equal(t, models.EventPullRequest, running.Event)
	assert.Equal(t, 7, running.PullRequest)
	assert.Equal(t, "main", running.TargetBranch)
	assert.True(t, running.Fork)

	second, err := s.QueueJob(ctx, prJob(7, shaB), "hash-2", "")
	require.NoError(t, err)
	stop, err := s.CancelRequestedJobs(ctx, []string{"agent-1"})
	require.NoError(t, err)
	require.Len(t, stop, 1, "the running job is asked to stop")
	assert.Equal(t, first, stop[0].ID)

	otherPR, err := s.QueueJob(ctx, prJob(8, shaA), "hash-3", "")
	require.NoError(t, err)
	push, err := s.QueueJob(ctx, pendingJob(shaA), "hash-4", "")
	require.NoError(t, err)
	third, err := s.QueueJob(ctx, prJob(7, shaC), "hash-5", "")
	require.NoError(t, err)

	superseded, err := s.GetJob(ctx, second)
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusCanceled, superseded.Status, "a pending job is canceled at once")
	for _, id := range []int64{otherPR, push, third} {
		job, err := s.GetJob(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusPending, job.Status, "job %d is untouched", id)
	}
	pushJob, err := s.GetJob(ctx, push)
	require.NoError(t, err)
	assert.Equal(t, models.EventPush, pushJob.Event, "jobs default to push")
	assert.Zero(t, pushJob.PullRequest)

	ids, err := s.CancelPullRequest(ctx, "sidumandr/cisid", 7)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{first, third}, ids, "closing cancels what is left")
	again, err := s.CancelPullRequest(ctx, "sidumandr/cisid", 7)
	require.NoError(t, err)
	assert.Equal(t, []int64{first}, again, "only the running job, until its agent stops it")

	rerun, err := s.RerunJob(ctx, third, "")
	require.NoError(t, err)
	assert.Equal(t, models.EventPullRequest, rerun.Event, "a rerun keeps the pull request")
	assert.Equal(t, 7, rerun.PullRequest)
	assert.Equal(t, "main", rerun.TargetBranch)
	assert.True(t, rerun.Fork)

	sibs, err := s.ExpandMatrix(ctx, otherPR, map[string]string{"GO": "1"}, []map[string]string{{"GO": "2"}})
	require.NoError(t, err)
	sib, err := s.GetJob(ctx, sibs[0])
	require.NoError(t, err)
	assert.Equal(t, 8, sib.PullRequest, "matrix jobs belong to the same pull request")
	assert.True(t, sib.Fork)
}
