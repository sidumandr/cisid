package trigger

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// fakePollStore hands out its watches as due once and records the rest.
type fakePollStore struct {
	mu      sync.Mutex
	due     []models.Watch
	every   time.Duration
	jobs    []models.Job
	errs    map[int64]string
	claimed int
}

func (f *fakePollStore) ClaimDueWatches(_ context.Context, _ time.Time, every time.Duration, _ int) ([]models.Watch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.every = every
	f.claimed++
	due := f.due
	f.due = nil
	return due, nil
}

func (f *fakePollStore) QueuePolledJob(_ context.Context, w models.Watch, job models.Job) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w.Branch == "raced" {
		return 0, false, nil // another server got there first
	}
	f.jobs = append(f.jobs, job)
	return int64(len(f.jobs)), true, nil
}

func (f *fakePollStore) SetPollError(_ context.Context, id int64, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = map[int64]string{}
	}
	f.errs[id] = msg
	return nil
}

type fakeHeads map[string]string

func (f fakeHeads) BranchHead(_ context.Context, repo, branch string) (string, error) {
	if sha, ok := f[repo+" "+branch]; ok {
		return sha, nil
	}
	return "", errors.New("forge unavailable")
}

func TestPollerBuildsMovedHeads(t *testing.T) {
	store := &fakePollStore{due: []models.Watch{
		{ID: 1, Repo: "gitea:office/app", Branch: "main", CloneURL: "https://git.example.com/office/app.git", LastSHA: "old"},
		{ID: 2, Repo: "gitea:office/app", Branch: "dev", LastSHA: "same"},
		{ID: 3, Repo: "gitea:office/app", Branch: "gone", LastError: ""},
		{ID: 4, Repo: "gitea:office/app", Branch: "fixed", LastSHA: "same", LastError: "forge unavailable"},
		{ID: 5, Repo: "gitea:office/app", Branch: "raced", LastSHA: "old"},
		{ID: 6, Repo: "gitlab:group/app", Branch: "main"}, // never polled
	}}
	heads := fakeHeads{
		"gitea:office/app main":  "new",
		"gitea:office/app dev":   "same",
		"gitea:office/app fixed": "same",
		"gitea:office/app raced": "new",
		"gitlab:group/app main":  "first",
	}
	var queued []int64
	p := NewPoller(store, heads, 2*time.Minute, WithPollLogger(slog.New(slog.DiscardHandler)),
		WithPollQueued(func(id int64) { queued = append(queued, id) }))

	p.round(t.Context())

	assert.Equal(t, 2*time.Minute, store.every)
	require.Len(t, store.jobs, 2)
	assert.Equal(t, models.Job{
		Repo: "gitea:office/app", CloneURL: "https://git.example.com/office/app.git", Branch: "main",
		CommitSHA: "new", Status: models.JobStatusPending, Event: models.EventPush,
	}, store.jobs[0], "a moved head is built as a push")
	assert.Equal(t, "first", store.jobs[1].CommitSHA, "the first poll builds the head the branch has")
	assert.Equal(t, []int64{1, 2}, queued, "a poll another server won queues nothing here")
	assert.Equal(t, map[int64]string{3: "forge unavailable", 4: ""}, store.errs,
		"a failed poll is recorded and cleared by the next good one")
}

func TestPollerRunsUntilCanceled(t *testing.T) {
	store := &fakePollStore{}
	p := NewPoller(store, fakeHeads{}, time.Minute, WithPollLogger(slog.New(slog.DiscardHandler)))
	p.tick = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error)
	go func() { done <- p.Run(ctx) }()
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.claimed >= 3
	}, time.Second, time.Millisecond)
	cancel()
	assert.NoError(t, <-done)
}
