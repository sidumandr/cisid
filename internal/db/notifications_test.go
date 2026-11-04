package db

import (
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreNotifications(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	const repo = "gitea:office/app"

	chat := models.Notification{Repo: repo, Name: "chat", Kind: models.NotifySlack, Target: "hooks.slack.com",
		On: []string{models.ResultFailed, models.ResultFixed}}
	require.NoError(t, s.PutNotification(ctx, chat, []byte("sealed-1")))
	require.NoError(t, s.PutNotification(ctx, models.Notification{Repo: repo, Name: "audit", Kind: models.NotifyWebhook,
		Target: "ci.example.com", On: []string{models.ResultPassed}, Branches: []string{"main", "release/*"}, PullRequests: true},
		[]byte("sealed-2")))
	require.NoError(t, s.PutNotification(ctx, models.Notification{Repo: "other/repo", Name: "chat", Kind: models.NotifyEmail,
		Target: "dev@example.com", On: []string{models.ResultFailed}}, []byte("sealed-3")))

	list, err := s.Notifications(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "audit", list[0].Name, "by name")
	assert.Equal(t, []string{"main", "release/*"}, list[0].Branches)
	assert.True(t, list[0].PullRequests)
	assert.Equal(t, "chat", list[1].Name)
	assert.Equal(t, "hooks.slack.com", list[1].Target)
	assert.Equal(t, []string{"failed", "fixed"}, list[1].On)
	assert.Empty(t, list[1].Branches)
	assert.Nil(t, list[1].LastSentAt)

	sealed, err := s.SealedNotifications(ctx, repo)
	require.NoError(t, err)
	require.Len(t, sealed, 2)
	assert.Equal(t, []byte("sealed-2"), sealed[0].Sealed)
	assert.Equal(t, []byte("sealed-1"), sealed[1].Sealed)

	one, err := s.SealedNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-1"), one.Sealed)
	_, err = s.SealedNotification(ctx, repo, "missing")
	require.ErrorIs(t, err, ErrNotificationNotFound)

	require.NoError(t, s.RecordNotification(ctx, one.ID, "post: 404 Not Found"))
	one, err = s.SealedNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.Equal(t, "post: 404 Not Found", one.LastError)
	assert.Nil(t, one.LastSentAt)
	require.NoError(t, s.RecordNotification(ctx, one.ID, ""))
	one, err = s.SealedNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.Empty(t, one.LastError, "a good send clears the error")
	assert.NotNil(t, one.LastSentAt)

	require.NoError(t, s.RecordNotification(ctx, one.ID, "timeout"))
	chat.Target = "chat.example.com"
	require.NoError(t, s.PutNotification(ctx, chat, []byte("sealed-4")))
	one, err = s.SealedNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-4"), one.Sealed, "saving again replaces it")
	assert.Equal(t, "chat.example.com", one.Target)
	assert.Empty(t, one.LastError)
	assert.Nil(t, one.LastSentAt)

	deleted, err := s.DeleteNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = s.DeleteNotification(ctx, repo, "chat")
	require.NoError(t, err)
	assert.False(t, deleted)
	list, err = s.Notifications(ctx, repo)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

// setJob changes the columns of a job that only agents and planning set.
func setJob(t *testing.T, pool *pgxpool.Pool, id int64, status models.JobStatus, matrix map[string]string, parent *int64) {
	t.Helper()
	_, err := pool.Exec(t.Context(), "UPDATE jobs SET status = $2, matrix = $3, parent_id = $4 WHERE id = $1",
		id, status, matrix, parent)
	require.NoError(t, err)
}

func TestStoreClaimFinishedJobs(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()

	ids := createJobs(t, s, 5)
	setJob(t, pool, ids[0], models.JobStatusSuccess, nil, nil)
	setJob(t, pool, ids[1], models.JobStatusRunning, nil, nil)
	setJob(t, pool, ids[2], models.JobStatusFailed, nil, nil)
	setJob(t, pool, ids[3], models.JobStatusCanceled, nil, nil)

	jobs, err := s.ClaimFinishedJobs(ctx, 2)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, []int64{ids[0], ids[2]}, []int64{jobs[0].ID, jobs[1].ID}, "oldest first, finished only")
	assert.Equal(t, models.JobStatusFailed, jobs[1].Status)

	jobs, err = s.ClaimFinishedJobs(ctx, 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, ids[3], jobs[0].ID)

	jobs, err = s.ClaimFinishedJobs(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, jobs, "each job is claimed once")

	setJob(t, pool, ids[1], models.JobStatusSuccess, nil, nil)
	jobs, err = s.ClaimFinishedJobs(ctx, 10)
	require.NoError(t, err)
	require.Len(t, jobs, 1, "a job is claimed once it finishes")
	assert.Equal(t, ids[1], jobs[0].ID)
}

func TestStoreClaimFinishedJobsConcurrently(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	ids := createJobs(t, s, 40)
	for _, id := range ids {
		setJob(t, pool, id, models.JobStatusSuccess, nil, nil)
	}

	var (
		mu      sync.Mutex
		claimed = map[int64]int{}
		wg      sync.WaitGroup
	)
	for range 4 {
		wg.Go(func() {
			for {
				jobs, err := s.ClaimFinishedJobs(ctx, 3)
				if !assert.NoError(t, err) || len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					claimed[j.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	assert.Len(t, claimed, len(ids))
	for id, n := range claimed {
		assert.Equal(t, 1, n, "job %d claimed %d times", id, n)
	}
}

func TestStorePreviousResult(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()

	create := func(job models.Job, status models.JobStatus, matrix map[string]string) models.Job {
		t.Helper()
		id, err := s.CreateJob(ctx, job)
		require.NoError(t, err)
		setJob(t, pool, id, status, matrix, nil)
		job, err = s.GetJob(ctx, id)
		require.NoError(t, err)
		return job
	}
	push := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")
	pr := push
	pr.Event, pr.PullRequest, pr.Branch, pr.TargetBranch = models.EventPullRequest, 7, "feature", "main"
	dev := push
	dev.Branch = "dev"
	manual := push
	manual.Event = models.EventManual

	first := create(push, models.JobStatusSuccess, nil)
	_, ok, err := s.PreviousResult(ctx, first)
	require.NoError(t, err)
	assert.False(t, ok, "the first build has none")

	create(dev, models.JobStatusFailed, nil)
	create(pr, models.JobStatusFailed, nil)
	create(push, models.JobStatusCanceled, nil)
	create(push, models.JobStatusFailed, map[string]string{"go": "1.26"})
	failed := create(manual, models.JobStatusFailed, nil)

	prev, ok, err := s.PreviousResult(ctx, failed)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, models.JobStatusSuccess, prev,
		"other branches, pull requests, canceled jobs and other combinations do not count; manual builds and pushes do")

	fixed := create(push, models.JobStatusSuccess, nil)
	prev, ok, err = s.PreviousResult(ctx, fixed)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, models.JobStatusFailed, prev)

	combo := create(push, models.JobStatusSuccess, map[string]string{"go": "1.26"})
	prev, ok, err = s.PreviousResult(ctx, combo)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, models.JobStatusFailed, prev, "the same combination's build before")

	prJob := create(pr, models.JobStatusSuccess, nil)
	prev, ok, err = s.PreviousResult(ctx, prJob)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, models.JobStatusFailed, prev, "the same pull request's build before")
	other := pr
	other.PullRequest = 8
	_, ok, err = s.PreviousResult(ctx, create(other, models.JobStatusSuccess, nil))
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestStoreBranchResult(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	const repo = "sidumandr/cisid"

	result := func(branch string) string {
		t.Helper()
		failed, ok, err := s.BranchResult(ctx, repo, branch)
		require.NoError(t, err)
		switch {
		case !ok:
			return "unknown"
		case failed:
			return "failing"
		}
		return "passing"
	}
	create := func(job models.Job, status models.JobStatus, matrix map[string]string, parent *int64) int64 {
		t.Helper()
		id, err := s.CreateJob(ctx, job)
		require.NoError(t, err)
		setJob(t, pool, id, status, matrix, parent)
		return id
	}
	push := pendingJob("6113728f27ae82c7b1a177c8d03f9e96e0adf246")

	assert.Equal(t, "unknown", result("main"))
	has, err := s.RepoHasJobs(ctx, repo)
	require.NoError(t, err)
	assert.False(t, has)

	create(push, models.JobStatusRunning, nil, nil)
	assert.Equal(t, "unknown", result("main"), "no finished build yet")
	has, err = s.RepoHasJobs(ctx, repo)
	require.NoError(t, err)
	assert.True(t, has)

	create(push, models.JobStatusSuccess, nil, nil)
	assert.Equal(t, "passing", result("main"))

	root := create(push, models.JobStatusSuccess, map[string]string{"go": "1.26"}, nil)
	create(push, models.JobStatusFailed, map[string]string{"go": "1.27"}, &root)
	assert.Equal(t, "failing", result("main"), "one failed combination fails the build")
	create(push, models.JobStatusSuccess, map[string]string{"go": "1.27"}, &root)
	assert.Equal(t, "passing", result("main"), "a passing rerun of the combination replaces it")

	pr := push
	pr.Event, pr.PullRequest, pr.Branch, pr.TargetBranch = models.EventPullRequest, 3, "main", "main"
	create(pr, models.JobStatusFailed, nil, nil)
	create(push, models.JobStatusCanceled, nil, nil)
	assert.Equal(t, "passing", result("main"), "pull requests and canceled builds do not count")

	dev := push
	dev.Branch = "dev"
	create(dev, models.JobStatusFailed, nil, nil)
	assert.Equal(t, "failing", result("dev"))
	assert.Equal(t, "passing", result("main"))
}
