package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreApproveJob(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	fork := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")
	fork.Fork, fork.NeedsApproval = true, true
	held, err := s.CreateJob(ctx, fork)
	require.NoError(t, err)
	own := createJobs(t, s, 1)[0]

	job, err := s.GetJob(ctx, held)
	require.NoError(t, err)
	assert.True(t, job.NeedsApproval)

	got, err := s.ClaimJob(ctx, "worker-1", nil, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, own, got.ID, "a job waiting for approval is passed over")
	_, err = s.ClaimJob(ctx, "worker-1", nil, time.Minute)
	require.ErrorIs(t, err, ErrNoPendingJob)

	job, err = s.ApproveJob(ctx, held, "alice")
	require.NoError(t, err)
	assert.False(t, job.NeedsApproval)
	assert.Equal(t, "alice", job.ApprovedBy)
	_, err = s.ApproveJob(ctx, held, "bob")
	require.ErrorIs(t, err, ErrNoApprovalNeeded, "once is enough")
	_, err = s.ApproveJob(ctx, own, "bob")
	require.ErrorIs(t, err, ErrNoApprovalNeeded)
	_, err = s.ApproveJob(ctx, 999, "bob")
	require.ErrorIs(t, err, ErrJobNotFound)

	got, err = s.ClaimJob(ctx, "worker-1", nil, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, held, got.ID, "once approved, an agent takes it")
	assert.Equal(t, "alice", got.ApprovedBy)
}
