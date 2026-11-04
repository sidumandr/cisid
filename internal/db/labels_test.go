package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreClaimJobByLabels(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 2)

	// Labels are unknown until a planning finds them, so anyone claims.
	job, err := s.ClaimJob(ctx, "plain", nil, time.Minute)
	require.NoError(t, err)
	require.Equal(t, ids[0], job.ID)
	require.NoError(t, s.RequeueForLabels(ctx, job.ID, "plain", []string{"arm64"}))
	require.ErrorIs(t, s.RequeueForLabels(ctx, job.ID, "plain", []string{"arm64"}), ErrJobConflict, "it is no longer running")

	job, err = s.GetJob(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, []string{"arm64"}, job.Labels)
	assert.Empty(t, job.Agent)

	next, err := s.ClaimJob(ctx, "plain", nil, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ids[1], next.ID, "an agent without the label passes it over")
	_, err = s.ClaimJob(ctx, "other", []string{"gpu"}, time.Minute)
	require.ErrorIs(t, err, ErrNoPendingJob, "nor does one with other labels")

	got, err := s.ClaimJob(ctx, "arm", []string{"gpu", "arm64"}, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ids[0], got.ID, "an agent with every label claims it")
	assert.Equal(t, "arm", got.Agent)
}
