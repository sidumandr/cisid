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

	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/pkg/models"
)

const sha = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"

func TestNext(t *testing.T) {
	from := time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC)
	for spec, want := range map[string]time.Time{
		"0 3 * * *":                          time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC),
		"*/15 * * * *":                       time.Date(2026, 11, 3, 10, 45, 0, 0, time.UTC),
		"@daily":                             time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		"0 9 * * 1-5":                        time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), // a Wednesday
		"CRON_TZ=Europe/Istanbul 0 14 * * *": time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC),
	} {
		got, err := Next(spec, from)
		require.NoError(t, err, spec)
		assert.True(t, want.Equal(got), "%s: got %s, want %s", spec, got, want)
	}
	for _, bad := range []string{"", "every day", "61 * * * *", "* * * *", "0 0 30 2 *"} {
		_, err := Next(bad, from)
		assert.ErrorIs(t, err, ErrInvalidCron, bad)
	}
}

type fakeResolver struct {
	repoErr, headErr error
}

func (f fakeResolver) Repository(_ context.Context, repo string) (github.Repository, error) {
	return github.Repository{CloneURL: "https://github.com/" + repo + ".git", DefaultBranch: "main"}, f.repoErr
}

func (f fakeResolver) BranchHead(_ context.Context, repo, branch string) (string, error) {
	if branch == "gone" {
		return "", github.ErrNotFound
	}
	return sha, f.headErr
}

func TestBranchJob(t *testing.T) {
	job, err := BranchJob(t.Context(), fakeResolver{}, "sidumandr/cisid", "", models.EventManual)
	require.NoError(t, err)
	assert.Equal(t, models.Job{
		Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git", Branch: "main",
		CommitSHA: sha, Status: models.JobStatusPending, Event: models.EventManual,
	}, job, "no branch: the default branch")

	job, err = BranchJob(t.Context(), fakeResolver{}, "sidumandr/cisid", "release/1.0", models.EventSchedule)
	require.NoError(t, err)
	assert.Equal(t, "release/1.0", job.Branch)

	_, err = BranchJob(t.Context(), fakeResolver{}, "sidumandr/cisid", "gone", models.EventManual)
	assert.ErrorIs(t, err, github.ErrNotFound)
}

// fakeStore hands out the schedules it holds as due and records the rest.
type fakeStore struct {
	mu     sync.Mutex
	due    []models.Schedule
	jobs   []models.Job
	marked map[int64]int64
}

func (f *fakeStore) ClaimDueSchedules(_ context.Context, now time.Time,
	next func(string, time.Time) (time.Time, error), _ int) ([]models.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	due := f.due
	f.due = nil
	for i := range due {
		n, err := next(due[i].Cron, now)
		if err != nil {
			return nil, err
		}
		due[i].NextRunAt = n
	}
	return due, nil
}

func (f *fakeStore) CreateJob(_ context.Context, job models.Job) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return int64(len(f.jobs)), nil
}

func (f *fakeStore) MarkScheduleRun(_ context.Context, id, jobID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.marked == nil {
		f.marked = map[int64]int64{}
	}
	f.marked[id] = jobID
	return nil
}

func TestSchedulerQueuesDueSchedules(t *testing.T) {
	store := &fakeStore{due: []models.Schedule{
		{ID: 1, Repo: "sidumandr/cisid", Name: "nightly", Cron: "0 3 * * *"},
		{ID: 2, Repo: "sidumandr/cisid", Name: "broken", Cron: "0 4 * * *", Branch: "gone"},
		{ID: 3, Repo: "sidumandr/other", Name: "weekly", Cron: "@weekly", Branch: "dev"},
	}}
	var queued []int64
	s := NewScheduler(store, fakeResolver{}, WithLogger(slog.New(slog.DiscardHandler)),
		WithQueued(func(id int64) { queued = append(queued, id) }))

	s.round(t.Context())

	require.Len(t, store.jobs, 2, "a schedule whose branch is gone is skipped, the others run")
	assert.Equal(t, models.EventSchedule, store.jobs[0].Event)
	assert.Equal(t, "main", store.jobs[0].Branch)
	assert.Equal(t, "sidumandr/other", store.jobs[1].Repo)
	assert.Equal(t, "dev", store.jobs[1].Branch)
	assert.Equal(t, map[int64]int64{1: 1, 3: 2}, store.marked)
	assert.Equal(t, []int64{1, 2}, queued)
}

func TestSchedulerKeepsGoingWhenGitHubIsDown(t *testing.T) {
	store := &fakeStore{due: []models.Schedule{{ID: 1, Repo: "sidumandr/cisid", Name: "nightly", Cron: "@daily"}}}
	s := NewScheduler(store, fakeResolver{repoErr: errors.New("connection refused")},
		WithLogger(slog.New(slog.DiscardHandler)))

	s.round(t.Context())

	assert.Empty(t, store.jobs, "the run is skipped; the schedule goes on at its next time")
}
