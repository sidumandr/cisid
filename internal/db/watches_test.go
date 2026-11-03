package db

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreWatches(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	const repo = "gitea:office/app"

	created, err := s.PutWatch(ctx, models.Watch{Repo: repo, Branch: "main", CloneURL: "https://git.example.com/office/app.git"})
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.PutWatch(ctx, models.Watch{Repo: repo, Branch: "main", CloneURL: "https://git.example.com/office/app2.git"})
	require.NoError(t, err)
	assert.False(t, created, "watching again only updates the clone URL")
	_, err = s.PutWatch(ctx, models.Watch{Repo: repo, Branch: "dev", CloneURL: "u"})
	require.NoError(t, err)
	_, err = s.PutWatch(ctx, models.Watch{Repo: "other/repo", Branch: "main", CloneURL: "u"})
	require.NoError(t, err)

	list, err := s.Watches(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []string{"dev", "main"}, []string{list[0].Branch, list[1].Branch})
	assert.Equal(t, "https://git.example.com/office/app2.git", list[1].CloneURL)
	assert.Empty(t, list[1].LastSHA, "not polled yet")

	now := time.Now().Add(time.Second)
	due, err := s.ClaimDueWatches(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	assert.Len(t, due, 3, "new watches are polled at once")
	again, err := s.ClaimDueWatches(ctx, now, time.Minute, 10)
	require.NoError(t, err)
	assert.Empty(t, again, "a claimed poll is not claimed twice")
	later, err := s.ClaimDueWatches(ctx, now.Add(time.Minute), time.Minute, 10)
	require.NoError(t, err)
	assert.Len(t, later, 3, "and comes back after the interval")

	deleted, err := s.DeleteWatch(ctx, repo, "dev")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = s.DeleteWatch(ctx, repo, "dev")
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestQueuePolledJobBuildsEachHeadOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	w := models.Watch{Repo: "gitlab:group/app", Branch: "main", CloneURL: "https://gitlab.com/group/app.git"}
	_, err := s.PutWatch(ctx, w)
	require.NoError(t, err)
	claimed, err := s.ClaimDueWatches(ctx, time.Now().Add(time.Second), time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	w = claimed[0]

	job := models.Job{Repo: w.Repo, CloneURL: w.CloneURL, Branch: "main", CommitSHA: shaA, Status: models.JobStatusPending, Event: models.EventPush}

	// Two servers saw the same new head at once: one job.
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		queued []int64
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, ok, err := s.QueuePolledJob(ctx, w, job)
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				queued = append(queued, id)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Len(t, queued, 1)

	list, err := s.Watches(ctx, w.Repo)
	require.NoError(t, err)
	assert.Equal(t, shaA, list[0].LastSHA)
	require.NotNil(t, list[0].LastJobID)
	assert.Equal(t, queued[0], *list[0].LastJobID)
	got, err := s.GetJob(ctx, queued[0])
	require.NoError(t, err)
	assert.Equal(t, models.EventPush, got.Event)

	require.NoError(t, s.SetPollError(ctx, w.ID, "forge unavailable"))
	list, err = s.Watches(ctx, w.Repo)
	require.NoError(t, err)
	assert.Equal(t, "forge unavailable", list[0].LastError)

	w.LastSHA = shaA
	job.CommitSHA = shaB
	_, ok, err := s.QueuePolledJob(ctx, w, job)
	require.NoError(t, err)
	assert.True(t, ok, "a newer head builds")
	list, err = s.Watches(ctx, w.Repo)
	require.NoError(t, err)
	assert.Empty(t, list[0].LastError, "a good poll clears the error")
}
