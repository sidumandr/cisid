package db

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

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

	first, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ids[0], first.ID)
	assert.Equal(t, models.JobStatusRunning, first.Status)
	assert.Equal(t, "agent-1", first.Agent)
	assert.NotNil(t, first.StartedAt)

	second, err := s.ClaimJob(ctx, "agent-2", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ids[1], second.ID)

	_, err = s.ClaimJob(ctx, "agent-3", time.Minute)
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
				job, err := s.ClaimJob(t.Context(), name, time.Minute)
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
	_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)

	assert.ErrorIs(t, s.RequeueJob(ctx, ids[0], "agent-2"), ErrJobConflict, "only the owner may requeue")
	require.NoError(t, s.RequeueJob(ctx, ids[0], "agent-1"))

	got, err := s.GetJob(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusPending, got.Status)
	assert.Empty(t, got.Agent)
	assert.Nil(t, got.StartedAt)

	again, err := s.ClaimJob(ctx, "agent-2", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, ids[0], again.ID)
}

func TestStoreRequeueAgentJobs(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 3)
	for range 2 {
		_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
		require.NoError(t, err)
	}
	_, err := s.ClaimJob(ctx, "agent-2", time.Minute)
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
		_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
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
		_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
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
		_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
		require.NoError(t, err)

		res := failed
		res.JobID = ids[0]
		assert.ErrorIs(t, s.FinishJob(ctx, "agent-2", res), ErrJobConflict)
	})

	t.Run("pending job cannot be finished", func(t *testing.T) {
		s, _ := newStore(t)
		ctx := t.Context()
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
		require.NoError(t, err)
		require.NoError(t, s.RequeueJob(ctx, ids[0], "agent-1"))

		err = s.FinishJob(ctx, "agent-1", models.JobResult{JobID: ids[0], Status: models.JobStatusSuccess})
		assert.ErrorIs(t, err, ErrJobConflict, "a requeued job belongs to nobody")

		got, err := s.GetJob(ctx, ids[0])
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusPending, got.Status)
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

func TestStoreExpireLogs(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	// Two finished long ago, one recently, one still pending.
	ids := createJobs(t, s, 4)
	for _, id := range ids {
		require.NoError(t, s.AppendJobLog(ctx, models.JobLog{JobID: id, Seq: 1, Stream: models.StreamStdout, Data: "line\n"}))
	}
	finish := func(id int64, ago string) {
		_, err := pool.Exec(ctx, "UPDATE jobs SET status = 'success', finished_at = now() - $2::interval WHERE id = $1", id, ago)
		require.NoError(t, err)
	}
	finish(ids[0], "100 days")
	finish(ids[1], "95 days")
	finish(ids[2], "1 day")

	const retention = 90 * 24 * time.Hour
	n, err := s.ExpireLogs(ctx, retention, 1)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "at most a batch")
	n, err = s.ExpireLogs(ctx, retention, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the rest")
	n, err = s.ExpireLogs(ctx, retention, 10)
	require.NoError(t, err)
	assert.Zero(t, n, "nothing left")

	for i, id := range ids {
		logs, err := s.JobLogs(ctx, id)
		require.NoError(t, err)
		job, err := s.GetJob(ctx, id)
		require.NoError(t, err)
		if i < 2 {
			assert.Empty(t, logs, "job %d", i)
			assert.NotNil(t, job.LogsExpiredAt, "job %d is marked", i)
		} else {
			assert.Len(t, logs, 1, "job %d keeps its logs", i)
			assert.Nil(t, job.LogsExpiredAt, "job %d", i)
		}
	}
}

func TestStoreClaimJobSetsLease(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)

	job, err := s.ClaimJob(ctx, "agent-1", 5*time.Minute)

	require.NoError(t, err)
	require.NotNil(t, job.LeaseExpiresAt)
	assert.WithinDuration(t, job.StartedAt.Add(5*time.Minute), *job.LeaseExpiresAt, time.Second)
}

func TestStoreLeases(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 3)
	// agent-1 holds an expired lease, agent-2 and agent-3 valid ones.
	_, err := s.ClaimJob(ctx, "agent-1", -time.Second)
	require.NoError(t, err)
	_, err = s.ClaimJob(ctx, "agent-2", time.Minute)
	require.NoError(t, err)
	_, err = s.ClaimJob(ctx, "agent-3", -time.Second)
	require.NoError(t, err)

	// agent-3 is still connected somewhere, so its lease is renewed in time.
	n, err := s.RenewLeases(ctx, []string{"agent-3", "unknown"}, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	requeued, err := s.RequeueExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{ids[0]}, requeued)

	got, err := s.GetJob(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusPending, got.Status)
	assert.Empty(t, got.Agent)
	assert.Nil(t, got.LeaseExpiresAt)

	for _, id := range ids[1:] {
		got, err := s.GetJob(ctx, id)
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusRunning, got.Status)
	}

	requeued, err = s.RequeueExpired(ctx)
	require.NoError(t, err)
	assert.Empty(t, requeued)
}

func TestStoreAdoptJob(t *testing.T) {
	t.Run("job still running on the agent", func(t *testing.T) {
		s, _ := newStore(t)
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(t.Context(), "agent-1", -time.Second)
		require.NoError(t, err)

		require.NoError(t, s.AdoptJob(t.Context(), "agent-1", ids[0], time.Minute))

		got, err := s.GetJob(t.Context(), ids[0])
		require.NoError(t, err)
		assert.True(t, got.LeaseExpiresAt.After(time.Now().Add(30*time.Second)), "lease renewed")
	})

	t.Run("requeued job not yet claimed by anyone", func(t *testing.T) {
		s, _ := newStore(t)
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(t.Context(), "agent-1", -time.Second)
		require.NoError(t, err)
		_, err = s.RequeueExpired(t.Context())
		require.NoError(t, err)

		require.NoError(t, s.AdoptJob(t.Context(), "agent-1", ids[0], time.Minute))

		got, err := s.GetJob(t.Context(), ids[0])
		require.NoError(t, err)
		assert.Equal(t, models.JobStatusRunning, got.Status)
		assert.Equal(t, "agent-1", got.Agent)
	})

	t.Run("job taken over by another agent", func(t *testing.T) {
		s, _ := newStore(t)
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(t.Context(), "agent-2", time.Minute)
		require.NoError(t, err)

		err = s.AdoptJob(t.Context(), "agent-1", ids[0], time.Minute)
		assert.ErrorIs(t, err, ErrJobConflict)
	})

	t.Run("finished job", func(t *testing.T) {
		s, _ := newStore(t)
		ids := createJobs(t, s, 1)
		_, err := s.ClaimJob(t.Context(), "agent-1", time.Minute)
		require.NoError(t, err)
		require.NoError(t, s.FinishJob(t.Context(), "agent-1",
			models.JobResult{JobID: ids[0], Status: models.JobStatusSuccess}))

		err = s.AdoptJob(t.Context(), "agent-1", ids[0], time.Minute)
		assert.ErrorIs(t, err, ErrJobConflict)
	})
}

func TestStoreListJobs(t *testing.T) {
	s, _ := newStore(t)
	ids := createJobs(t, s, 3)

	jobs, err := s.ListJobs(t.Context(), 2)

	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, []int64{ids[2], ids[1]}, []int64{jobs[0].ID, jobs[1].ID}, "newest first")
}

func TestStoreJobLogsAfter(t *testing.T) {
	s, _ := newStore(t)
	id := createJobs(t, s, 1)[0]
	for seq := int64(1); seq <= 5; seq++ {
		require.NoError(t, s.AppendJobLog(t.Context(), models.JobLog{JobID: id, Seq: seq, Stream: models.StreamStdout, Data: "x"}))
	}

	logs, err := s.JobLogsAfter(t.Context(), id, 2, 2)

	require.NoError(t, err)
	require.Len(t, logs, 2)
	assert.Equal(t, []int64{3, 4}, []int64{logs[0].Seq, logs[1].Seq})
}

func TestStoreNotifiesJobEvents(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	id := createJobs(t, s, 1)[0]
	_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)

	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "LISTEN "+JobEventsChannel)
	require.NoError(t, err)

	next := func() (string, bool) {
		wctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		n, err := conn.Conn().WaitForNotification(wctx)
		if err != nil {
			return "", false
		}
		return n.Payload, true
	}
	want := strconv.FormatInt(id, 10)

	l := models.JobLog{JobID: id, Seq: 1, Stream: models.StreamStdout, Data: "hello\n"}
	require.NoError(t, s.AppendJobLog(ctx, l))
	payload, ok := next()
	require.True(t, ok, "a new log chunk is announced")
	assert.Equal(t, want, payload)

	require.NoError(t, s.AppendJobLog(ctx, l))
	_, ok = next()
	assert.False(t, ok, "a duplicate chunk is not announced")

	require.NoError(t, s.FinishJob(ctx, "agent-1", models.JobResult{JobID: id, Status: models.JobStatusSuccess}))
	payload, ok = next()
	require.True(t, ok, "a finished job is announced")
	assert.Equal(t, want, payload)
}

func TestStoreUnreportedJobs(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 2)

	jobs, err := s.UnreportedJobs(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, jobs, 2, "new jobs are unreported")

	require.NoError(t, s.MarkReported(ctx, ids[0], models.JobStatusPending))
	jobs, err = s.UnreportedJobs(ctx, 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, ids[1], jobs[0].ID)

	// A status change makes the job unreported again.
	_, err = s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)
	jobs, err = s.UnreportedJobs(ctx, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, ids[0], jobs[0].ID)
	assert.Equal(t, models.JobStatusRunning, jobs[0].Status)
}

func TestStoreCancelJob(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 3)
	running, err := s.ClaimJob(ctx, "agent-1", time.Minute) // ids[0]
	require.NoError(t, err)

	// A pending job is canceled at once.
	job, err := s.CancelJob(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusCanceled, job.Status)
	assert.Equal(t, CancelReason, job.Error)
	assert.NotNil(t, job.FinishedAt)
	_, err = s.ClaimJob(ctx, "agent-2", time.Minute)
	require.NoError(t, err) // ids[2]: the canceled job is skipped
	_, err = s.ClaimJob(ctx, "agent-3", time.Minute)
	assert.ErrorIs(t, err, ErrNoPendingJob)

	// A running job keeps running until its agent stops it.
	job, err = s.CancelJob(ctx, running.ID)
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusRunning, job.Status)
	toCancel, err := s.CancelRequestedJobs(ctx, []string{"agent-1", "agent-2"})
	require.NoError(t, err)
	require.Len(t, toCancel, 1)
	assert.Equal(t, running.ID, toCancel[0].ID)

	_, err = s.CancelJob(ctx, ids[1])
	assert.ErrorIs(t, err, ErrJobConflict, "already finished")
	_, err = s.CancelJob(ctx, ids[2]+100)
	assert.ErrorIs(t, err, ErrJobNotFound)
}

func TestStoreCanceledJobIsNotRequeued(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "agent-1", -time.Second)
	require.NoError(t, err)
	_, err = s.CancelJob(ctx, job.ID)
	require.NoError(t, err)

	// The agent never comes back to stop it.
	_, err = s.RequeueExpired(ctx)
	require.NoError(t, err)

	got, err := s.GetJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, models.JobStatusCanceled, got.Status)
	assert.Equal(t, CancelReason, got.Error)
	_, err = s.ClaimJob(ctx, "agent-2", time.Minute)
	assert.ErrorIs(t, err, ErrNoPendingJob)
}

func TestStoreRerunJob(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	id := createJobs(t, s, 1)[0]

	job, err := s.RerunJob(ctx, id, "")

	require.NoError(t, err)
	assert.Greater(t, job.ID, id)
	assert.Equal(t, models.JobStatusPending, job.Status)
	assert.Equal(t, "6113728f27ae82c7b1a177c8d03f9e96e0adf246", job.CommitSHA)

	_, err = s.RerunJob(ctx, id+100, "")
	assert.ErrorIs(t, err, ErrJobNotFound)
}

func TestStoreSetJobSteps(t *testing.T) {
	s, _ := newStore(t)
	id := createJobs(t, s, 1)[0]

	require.NoError(t, s.SetJobSteps(t.Context(), id, []string{"clone", "test"}))

	got, err := s.GetJob(t.Context(), id)
	require.NoError(t, err)
	assert.Equal(t, []string{"clone", "test"}, got.StepNames)
}

func TestStoreSecrets(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	const repo = "sidumandr/cisid"

	require.NoError(t, s.PutSecret(ctx, repo, "B_TOKEN", []byte("sealed-b")))
	require.NoError(t, s.PutSecret(ctx, repo, "A_TOKEN", []byte("sealed-a1")))
	require.NoError(t, s.PutSecret(ctx, repo, "A_TOKEN", []byte("sealed-a2")))
	require.NoError(t, s.PutSecret(ctx, "other/repo", "A_TOKEN", []byte("other")))

	infos, err := s.SecretNames(ctx, repo)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	assert.Equal(t, []string{"A_TOKEN", "B_TOKEN"}, []string{infos[0].Name, infos[1].Name})

	sealed, err := s.SealedSecrets(ctx, repo, []string{"A_TOKEN", "MISSING"})
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"A_TOKEN": []byte("sealed-a2")}, sealed)

	found, err := s.DeleteSecret(ctx, repo, "A_TOKEN")
	require.NoError(t, err)
	assert.True(t, found)
	found, err = s.DeleteSecret(ctx, repo, "A_TOKEN")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestStoreExpandMatrix(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	id := createJobs(t, s, 1)[0]
	_, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)

	ids, err := s.ExpandMatrix(ctx, id, map[string]string{"GO": "1.26"},
		[]map[string]string{{"GO": "1.27"}, {"GO": "1.28"}})
	require.NoError(t, err)
	require.Len(t, ids, 2)

	parent, err := s.GetJob(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"GO": "1.26"}, parent.Matrix)
	assert.Nil(t, parent.ParentID)

	sib, err := s.GetJob(ctx, ids[1])
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"GO": "1.28"}, sib.Matrix)
	assert.Equal(t, &id, sib.ParentID)
	assert.Equal(t, models.JobStatusPending, sib.Status)
	assert.Equal(t, parent.CommitSHA, sib.CommitSHA)

	again, err := s.ExpandMatrix(ctx, id, map[string]string{"GO": "1.26"}, []map[string]string{{"GO": "9"}})
	require.NoError(t, err)
	assert.Empty(t, again, "an expanded job is not expanded twice")

	rerun, err := s.RerunJob(ctx, ids[1], "")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"GO": "1.28"}, rerun.Matrix, "a rerun keeps its combination")

	plain, err := s.GetJob(ctx, createJobs(t, s, 1)[0])
	require.NoError(t, err)
	assert.Nil(t, plain.Matrix)
}

// TestStoreKeepsTraceParent checks where a job's trace context comes from:
// the queuing request, the first job of a matrix, or the rerun request.
func TestStoreKeepsTraceParent(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	const (
		pushTrace  = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
		rerunTrace = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	)
	job := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")
	job.TraceParent = pushTrace
	id, err := s.QueueJob(ctx, job, "hash-trace", "delivery-trace")
	require.NoError(t, err)

	claimed, err := s.ClaimJob(ctx, "agent-1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, pushTrace, claimed.TraceParent)

	ids, err := s.ExpandMatrix(ctx, id, map[string]string{"GO": "1"}, []map[string]string{{"GO": "2"}})
	require.NoError(t, err)
	sib, err := s.GetJob(ctx, ids[0])
	require.NoError(t, err)
	assert.Equal(t, pushTrace, sib.TraceParent, "matrix jobs share the push's trace")

	rerun, err := s.RerunJob(ctx, id, rerunTrace)
	require.NoError(t, err)
	assert.Equal(t, rerunTrace, rerun.TraceParent, "a rerun belongs to the rerun request's trace")

	plain, err := s.GetJob(ctx, createJobs(t, s, 1)[0])
	require.NoError(t, err)
	assert.Empty(t, plain.TraceParent)
}

func TestStoreQueueJobOncePerPayload(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	job := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")

	id, err := s.QueueJob(ctx, job, "hash-a", "delivery-1")
	require.NoError(t, err)
	assert.Positive(t, id)

	_, err = s.QueueJob(ctx, job, "hash-a", "delivery-2")
	assert.ErrorIs(t, err, ErrDuplicateDelivery, "same body, new delivery header: still a duplicate")

	_, err = s.QueueJob(ctx, job, "hash-b", "")
	require.NoError(t, err, "another body")

	jobs, err := s.ListJobs(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, jobs, 2)

	_, err = pool.Exec(ctx, "UPDATE webhook_deliveries SET received_at = now() - interval '9 days' WHERE payload_sha256 = 'hash-a'")
	require.NoError(t, err)
	n, err := s.PruneDeliveries(ctx, 8*24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestStoreCountJobsByStatus(t *testing.T) {
	s, _ := newStore(t)
	createJobs(t, s, 3)
	_, err := s.ClaimJob(t.Context(), "agent-1", time.Minute)
	require.NoError(t, err)

	counts, err := s.CountJobsByStatus(t.Context())
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"pending": 2, "running": 1}, counts)
}
