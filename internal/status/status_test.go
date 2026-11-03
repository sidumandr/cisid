package status

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/pkg/models"
)

func ptr[T any](v T) *T { return &v }

func TestDescribe(t *testing.T) {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	end := start.Add(83*time.Second + 400*time.Millisecond)

	tests := []struct {
		name  string
		job   models.Job
		state string
		desc  string
	}{
		{"pending", models.Job{Status: models.JobStatusPending}, "pending", "Waiting for an agent"},
		{"running", models.Job{Status: models.JobStatusRunning, Agent: "worker-1"}, "pending", "Running on worker-1"},
		{"success", models.Job{Status: models.JobStatusSuccess, StartedAt: &start, FinishedAt: &end}, "success", "Passed in 1m23s"},
		{"success without times", models.Job{Status: models.JobStatusSuccess}, "success", "Passed"},
		{"clone failed", models.Job{Status: models.JobStatusFailed, FailedStep: ptr(0), ExitCode: ptr(128)}, "failure", "Clone failed with exit code 128"},
		{"step failed", models.Job{Status: models.JobStatusFailed, FailedStep: ptr(2), ExitCode: ptr(1)}, "failure", "Step 2 failed with exit code 1"},
		{"named step failed", models.Job{Status: models.JobStatusFailed, FailedStep: ptr(1), ExitCode: ptr(2),
			StepNames: []string{"clone", "test"}}, "failure", `Step "test" failed with exit code 2`},
		{"runner error", models.Job{Status: models.JobStatusFailed, Error: "no .cisid.yml in repository"}, "failure", "Failed: no .cisid.yml in repository"},
		{"canceled", models.Job{Status: models.JobStatusCanceled}, "error", "Canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := Describe(tt.job)
			assert.Equal(t, github.CommitStatus{State: tt.state, Description: tt.desc, Context: "cisid"}, st)
		})
	}
}

func TestDescribeTruncatesLongErrors(t *testing.T) {
	st := Describe(models.Job{Status: models.JobStatusFailed, Error: strings.Repeat("ç", 300)})
	assert.Equal(t, maxDescription, len([]rune(st.Description)))
	assert.True(t, strings.HasSuffix(st.Description, "…"))
}

// fakeStore keeps reported statuses in memory.
type fakeStore struct {
	mu       sync.Mutex
	jobs     []models.Job
	reported map[int64]models.JobStatus
}

func (s *fakeStore) UnreportedJobs(_ context.Context, limit int) ([]models.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.Job
	for _, j := range s.jobs {
		if s.reported[j.ID] != j.Status && len(out) < limit {
			out = append(out, j)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkReported(_ context.Context, id int64, st models.JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reported == nil {
		s.reported = map[int64]models.JobStatus{}
	}
	s.reported[id] = st
	return nil
}

func (s *fakeStore) set(job models.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].ID == job.ID {
			s.jobs[i] = job
			return
		}
	}
	s.jobs = append(s.jobs, job)
}

// fakePoster records posted statuses and fails while err is set.
type fakePoster struct {
	mu     sync.Mutex
	posted []string
	err    error
	posts  chan struct{}
}

func (p *fakePoster) CreateStatus(_ context.Context, repo, sha string, st github.CommitStatus) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() {
		if p.posts != nil {
			p.posts <- struct{}{}
		}
	}()
	if p.err != nil {
		return p.err
	}
	p.posted = append(p.posted, fmt.Sprintf("%s@%s %s %s %s", repo, sha[:7], st.State, st.Description, st.TargetURL))
	return nil
}

func (p *fakePoster) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *fakePoster) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.posted...)
}

func job(id int64, st models.JobStatus) models.Job {
	return models.Job{ID: id, Repo: "sidumandr/cisid", CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246", Status: st, Agent: "w1"}
}

func startReporter(t *testing.T, store Store, poster Poster) *Reporter {
	t.Helper()
	r := New(store, poster, WithLogger(slog.New(slog.DiscardHandler)), WithInterval(time.Hour),
		WithJobURL(func(id int64) string { return fmt.Sprintf("https://ci.example.com/jobs/%d", id) }))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		assert.NoError(t, r.Run(ctx))
	}()
	t.Cleanup(func() { cancel(); <-done })
	return r
}

func TestReporterReportsEachTransitionOnce(t *testing.T) {
	store := &fakeStore{}
	store.set(job(1, models.JobStatusPending))
	poster := &fakePoster{}
	r := startReporter(t, store, poster)

	require.Eventually(t, func() bool { return len(poster.all()) == 1 }, time.Second, 5*time.Millisecond)

	store.set(job(1, models.JobStatusRunning))
	r.Wake()
	require.Eventually(t, func() bool { return len(poster.all()) == 2 }, time.Second, 5*time.Millisecond)

	r.Wake() // nothing changed
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, []string{
		"sidumandr/cisid@6113728 pending Waiting for an agent https://ci.example.com/jobs/1",
		"sidumandr/cisid@6113728 pending Running on w1 https://ci.example.com/jobs/1",
	}, poster.all())
}

func TestReporterRetriesWhenGitHubIsDown(t *testing.T) {
	store := &fakeStore{}
	store.set(job(1, models.JobStatusSuccess))
	store.set(job(2, models.JobStatusFailed))
	poster := &fakePoster{err: fmt.Errorf("post: %w", github.ErrTemporary), posts: make(chan struct{}, 10)}
	r := startReporter(t, store, poster)

	<-poster.posts
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, poster.posts, 0, "the round stops at the first temporary failure")
	store.mu.Lock()
	assert.Empty(t, store.reported)
	store.mu.Unlock()

	poster.setErr(nil)
	r.Wake()
	require.Eventually(t, func() bool { return len(poster.all()) == 2 }, time.Second, 5*time.Millisecond)
}

func TestReporterGivesUpOnPermanentErrors(t *testing.T) {
	store := &fakeStore{}
	store.set(job(1, models.JobStatusSuccess))
	poster := &fakePoster{err: errors.New("unexpected response 403 Forbidden"), posts: make(chan struct{}, 10)}
	startReporter(t, store, poster)

	<-poster.posts
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.reported[1] == models.JobStatusSuccess
	}, time.Second, 5*time.Millisecond, "a permanent failure is not retried forever")
}

func TestDescribeMatrixJobHasItsOwnContext(t *testing.T) {
	st := Describe(models.Job{Status: models.JobStatusPending, Matrix: map[string]string{"GO": "1.27", "DB": "18"}})
	assert.Equal(t, "cisid (DB=18, GO=1.27)", st.Context)
}
