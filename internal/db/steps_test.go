package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreStepResults(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	ids := createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)
	require.Equal(t, ids[0], job.ID)
	require.NoError(t, s.SetJobSteps(ctx, job.ID, []string{"clone", "lint", "test"}))

	job, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Nil(t, job.StepResults, "nothing reported yet")

	t0 := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(3 * time.Second)
	zero := 0
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 0,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &t0}}))
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 0,
		StepResult: models.StepResult{State: models.StepSuccess, ExitCode: &zero, StartedAt: &t0, FinishedAt: &t1}}))
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 2,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &t1}}))
	// A start resent after the end does not undo it.
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 0,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &t0}}))

	job, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, job.StepResults, 3, "one entry per planned step")
	assert.Equal(t, models.StepSuccess, job.StepResults[0].State)
	require.NotNil(t, job.StepResults[0].FinishedAt)
	assert.True(t, t1.Equal(*job.StepResults[0].FinishedAt))
	assert.Equal(t, 0, *job.StepResults[0].ExitCode)
	assert.Equal(t, models.StepPending, job.StepResults[1].State, "not reported yet")
	assert.Equal(t, models.StepRunning, job.StepResults[2].State, "steps run at the same time")

	list, err := s.ListJobs(ctx, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Len(t, list[0].StepResults, 3)

	assert.Error(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: -1}))

	one, two := 1, 2
	failed := 1
	require.NoError(t, s.FinishJob(ctx, "worker-1", models.JobResult{
		JobID: job.ID, Status: models.JobStatusFailed, ExitCode: 1, FailedStep: &two,
		Steps: []models.StepResult{
			{State: models.StepSuccess, ExitCode: &zero, StartedAt: &t0, FinishedAt: &t1},
			{State: models.StepCanceled, ExitCode: &one, StartedAt: &t1, FinishedAt: &t1},
			{State: models.StepFailed, ExitCode: &failed, StartedAt: &t1, FinishedAt: &t1},
		},
	}))
	job, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, job.StepResults, 3)
	assert.Equal(t, []models.StepState{models.StepSuccess, models.StepCanceled, models.StepFailed},
		[]models.StepState{job.StepResults[0].State, job.StepResults[1].State, job.StepResults[2].State},
		"the result replaces what was reported while it ran")

	// A finished job takes no more step reports.
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 1,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &t1}}))
	job, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StepCanceled, job.StepResults[1].State)
}

func TestStoreStepResultsOfFinishedJobWithoutReport(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)
	require.NoError(t, s.SetJobSteps(ctx, job.ID, []string{"clone", "test"}))
	now := time.Now()
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 0,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &now}}))

	// An agent that reports steps while running but sends no steps with
	// the result leaves the rest unstarted, which reads as skipped.
	require.NoError(t, s.FinishJob(ctx, "worker-1", models.JobResult{JobID: job.ID, Status: models.JobStatusCanceled, ExitCode: -1}))
	job, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, job.StepResults, 2)
	assert.Equal(t, models.StepRunning, job.StepResults[0].State)
	assert.Equal(t, models.StepSkipped, job.StepResults[1].State)
}

func TestStoreClaimForgetsEarlierSteps(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, s.RecordStep(ctx, models.JobStep{JobID: job.ID, Step: 0,
		StepResult: models.StepResult{State: models.StepRunning, StartedAt: &now}}))

	require.NoError(t, s.RequeueJob(ctx, job.ID, "worker-1"))
	again, err := s.ClaimJob(ctx, "worker-2", time.Minute)
	require.NoError(t, err)
	require.Equal(t, job.ID, again.ID)
	again, err = s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Nil(t, again.StepResults, "the new attempt starts afresh")
}
