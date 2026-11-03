package db

import (
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/pkg/models"
)

// newStore returns a Store on a fresh, migrated database.
func newStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	pool := dbtest.New(t)
	_, err := Migrate(t.Context(), pool)
	require.NoError(t, err)
	return New(pool), pool
}

func pendingJob(sha string) models.Job {
	return models.Job{
		Repo:      "sidumandr/cisid",
		CloneURL:  "https://github.com/sidumandr/cisid.git",
		Branch:    "main",
		CommitSHA: sha,
		Status:    models.JobStatusPending,
	}
}

func createJobs(t *testing.T, s *Store, n int) []int64 {
	t.Helper()
	ids := make([]int64, n)
	for i := range n {
		id, err := s.CreateJob(t.Context(), pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246"))
		require.NoError(t, err)
		ids[i] = id
	}
	return ids
}

func intPtr(i int) *int { return &i }

func TestStoreCreateAndGetJob(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	job := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")

	id, err := s.CreateJob(ctx, job)
	require.NoError(t, err)
	assert.Positive(t, id)

	got, err := s.GetJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, id, got.ID)
	assert.Equal(t, job.Repo, got.Repo)
	assert.Equal(t, job.CloneURL, got.CloneURL)
	assert.Equal(t, job.Branch, got.Branch)
	assert.Equal(t, job.CommitSHA, got.CommitSHA)
	assert.Equal(t, models.JobStatusPending, got.Status)
	assert.False(t, got.CreatedAt.IsZero())
	assert.Empty(t, got.Agent)
	assert.Nil(t, got.StartedAt)

	_, err = s.GetJob(ctx, id+1000)
	assert.ErrorIs(t, err, ErrJobNotFound)
}

func TestStorePing(t *testing.T) {
	s, _ := newStore(t)
	require.NoError(t, s.Ping(t.Context()))
}

func TestStoreClaimJobOldestFirst(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 2)

	first, err := s.ClaimJob(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, ids[0], first.ID)
	assert.Equal(t, models.JobStatusRunning, first.Status)
	assert.Equal(t, "agent-1", first.Agent)
	assert.NotNil(t, first.StartedAt)

	second, err := s.ClaimJob(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, ids[1], second.ID)

	_, err = s.ClaimJob(ctx, "agent-3")
	assert.ErrorIs(t, err, ErrNoPendingJob)
}

func TestStoreClaimJobConcurrently(t *testing.T) {
	s, _ := newStore(t)
	const jobs, agents = 20, 8
	createJobs(t, s, jobs)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed = map[int64]string{}
		dupes   []int64
	)
	for a := range agents {
		name := "agent-" + string(rune('a'+a))
		wg.Go(func() {
			for {
				job, err := s.ClaimJob(t.Context(), name)
				if err != nil {
					assert.ErrorIs(t, err, ErrNoPendingJob)
					return
				}
				mu.Lock()
				if _, dup := claimed[job.ID]; dup {
					dupes = append(dupes, job.ID)
				}
				claimed[job.ID] = name
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	assert.Empty(t, dupes, "a job must never be claimed twice")
	assert.Len(t, claimed, jobs)
}

func TestStoreRequeueJob(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 1)
	_, err := s.ClaimJob(ctx, "agent-1")
	require.NoError(t, err)

	assert.ErrorIs(t, s.RequeueJob(ctx, ids[0], "agent-2"), ErrJobConflict, "only the owner may requeue")
	require.NoError(t, s.RequeueJob(ctx, ids[0], "agent-1"))

	got, err := s.GetJob(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusPending, got.Status)
	assert.Empty(t, got.Agent)
	assert.Nil(t, got.StartedAt)

	again, err := s.ClaimJob(ctx, "agent-2")
	require.NoError(t, err)
	assert.Equal(t, ids[0], again.ID)
}

func TestStoreRequeueAgentJobs(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 3)
	for range 2 {
		_, err := s.ClaimJob(ctx, "agent-1")
		require.NoError(t, err)
	}
	_, err := s.ClaimJob(ctx, "agent-2")
	require.NoError(t, err)

	n, err := s.RequeueAgentJobs(ctx, "agent-1", ids[1])
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	statuses := map[int64]models.JobStatus{}
	for _, id := range ids {
		j, err := s.GetJob(ctx, id)
		require.NoError(t, err)
		statuses[id] = j.Status
	}
	assert.Equal(t, map[int64]models.JobStatus{
		ids[0]: models.JobStatusPending, // agent-1 no longer runs it
		ids[1]: models.JobStatusRunning, // agent-1 still runs it
		ids[2]: models.JobStatusRunning, // other agent
	}, statuses)

	n, err = s.RequeueAgentJobs(ctx, "agent-1", 0)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestStoreFinishJob(t *testing.T) {
	failed := models.JobResult{
		Status: models.JobStatusFailed, ExitCode: 3, FailedStep: intPtr(1), Error: "tests failed",
	}

	t.Run("owner records the result", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := t.Context()
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(ctx, "agent-1")
		require.NoError(t, err)

		res := failed
		res.JobID = ids[0]
		require.NoError(t, s.FinishJob(ctx, "agent-1", res))

		got, err := s.GetJob(ctx, ids[0])
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusFailed, got.Status)
		assert.Equal(t, intPtr(3), got.ExitCode)
		assert.Equal(t, intPtr(1), got.FailedStep)
		assert.Equal(t, "tests failed", got.Error)
		assert.NotNil(t, got.FinishedAt)

		assert.ErrorIs(t, s.FinishJob(ctx, "agent-1", res), ErrJobConflict, "a finished job cannot finish again")
	})

	t.Run("success has no failed step or error", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := t.Context()
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(ctx, "agent-1")
		require.NoError(t, err)

		require.NoError(t, s.FinishJob(ctx, "agent-1",
			models.JobResult{JobID: ids[0], Status: models.JobStatusSuccess}))

		got, err := s.GetJob(ctx, ids[0])
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusSuccess, got.Status)
		assert.Equal(t, intPtr(0), got.ExitCode)
		assert.Nil(t, got.FailedStep)
		assert.Empty(t, got.Error)
	})

	t.Run("other agent is rejected", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := t.Context()
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(ctx, "agent-1")
		require.NoError(t, err)

		res := failed
		res.JobID = ids[0]
		assert.ErrorIs(t, s.FinishJob(ctx, "agent-2", res), ErrJobConflict)
	})

	t.Run("late result for a requeued job is accepted", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := t.Context()
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(ctx, "agent-1")
		require.NoError(t, err)
		require.NoError(t, s.RequeueJob(ctx, ids[0], "agent-1"))

		require.NoError(t, s.FinishJob(ctx, "agent-1",
			models.JobResult{JobID: ids[0], Status: models.JobStatusSuccess}))

		got, err := s.GetJob(ctx, ids[0])
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusSuccess, got.Status)
		assert.Equal(t, "agent-1", got.Agent)
	})

	t.Run("non-terminal status is invalid", func(t *testing.T) {
		s, _ := newStore(t)
		err := s.FinishJob(t.Context(), "agent-1", models.JobResult{JobID: 1, Status: models.JobStatusRunning})
		assert.ErrorIs(t, err, ErrInvalidJobResult)
	})
}

func TestStoreJobLogs(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 1)
	id := ids[0]

	logs := []models.JobLog{
		{JobID: id, Step: 1, Stream: models.StreamStderr, Seq: 2, Data: "+ go test ./...\n"},
		{JobID: id, Step: 0, Stream: models.StreamStdout, Seq: 1, Data: "Cloning\x00 done\n"},
		{JobID: id, Step: 1, Stream: models.StreamStdout, Seq: 3, Data: "ok\n"},
	}
	for _, l := range logs {
		require.NoError(t, s.AppendJobLog(ctx, l))
	}
	// A resent chunk is ignored.
	require.NoError(t, s.AppendJobLog(ctx, logs[0]))

	got, err := s.JobLogs(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []models.JobLog{
		{JobID: id, Step: 0, Stream: models.StreamStdout, Seq: 1, Data: "Cloning done\n"},
		{JobID: id, Step: 1, Stream: models.StreamStderr, Seq: 2, Data: "+ go test ./...\n"},
		{JobID: id, Step: 1, Stream: models.StreamStdout, Seq: 3, Data: "ok\n"},
	}, got, "ordered by seq, NUL bytes removed")

	err = s.AppendJobLog(ctx, models.JobLog{JobID: id + 1000, Seq: 1, Stream: models.StreamStdout})
	assert.Error(t, err, "logs for unknown jobs are rejected")
}
