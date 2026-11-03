// Package status reports job results to GitHub as commit statuses.
//
// The reporter compares each job's status with the one it last reported
// (jobs.reported_status) and posts the difference, so no transition is lost
// across restarts and a GitHub outage only delays reports.
package status

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	// Context names cisid's status among the checks of a commit.
	Context = "cisid"

	defaultInterval = 5 * time.Second
	roundTimeout    = 30 * time.Second
	batchSize       = 50
	maxDescription  = 140 // GitHub's limit
)

// Store tracks which job statuses have been reported. *db.Store satisfies it.
type Store interface {
	UnreportedJobs(ctx context.Context, limit int) ([]models.Job, error)
	MarkReported(ctx context.Context, id int64, status models.JobStatus) error
}

var _ Store = (*db.Store)(nil)

// Poster sets commit statuses. *github.Client satisfies it.
type Poster interface {
	CreateStatus(ctx context.Context, repo, sha string, st github.CommitStatus) error
}

var _ Poster = (*github.Client)(nil)

// Option configures a Reporter.
type Option func(*Reporter)

// WithLogger sets the reporter's logger.
func WithLogger(log *slog.Logger) Option {
	return func(r *Reporter) { r.log = log }
}

// WithInterval sets how often the reporter looks for changes without being
// woken (default 5s).
func WithInterval(d time.Duration) Option {
	return func(r *Reporter) { r.interval = d }
}

// WithJobURL sets the link shown with each status, e.g. the job's page.
func WithJobURL(f func(jobID int64) string) Option {
	return func(r *Reporter) { r.jobURL = f }
}

// Reporter posts job statuses to GitHub.
type Reporter struct {
	store    Store
	poster   Poster
	log      *slog.Logger
	interval time.Duration
	jobURL   func(int64) string
	wake     chan struct{}
}

// New returns a Reporter.
func New(store Store, poster Poster, opts ...Option) *Reporter {
	r := &Reporter{
		store:    store,
		poster:   poster,
		log:      slog.Default(),
		interval: defaultInterval,
		wake:     make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Wake asks the reporter to look for changes now. It never blocks.
func (r *Reporter) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run reports status changes until ctx is canceled.
func (r *Reporter) Run(ctx context.Context) error {
	tick := time.NewTicker(r.interval)
	defer tick.Stop()
	for {
		r.round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
		case <-tick.C:
		}
	}
}

// round reports changes without being interrupted by ctx: a query
// canceled midway would hold up closing the database pool on shutdown.
// Run stops between rounds.
func (r *Reporter) round(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roundTimeout)
	defer cancel()
	r.reportChanges(ctx)
}

func (r *Reporter) reportChanges(ctx context.Context) {
	for {
		jobs, err := r.store.UnreportedJobs(ctx, batchSize)
		if err != nil {
			if ctx.Err() == nil {
				r.log.ErrorContext(ctx, "list unreported jobs", "error", err)
			}
			return
		}
		for _, job := range jobs {
			if !r.report(ctx, job) {
				return
			}
		}
		if len(jobs) < batchSize {
			return
		}
	}
}

// report posts job's status and records it. It returns false when the
// round should stop and be retried later.
func (r *Reporter) report(ctx context.Context, job models.Job) bool {
	st := Describe(job)
	if r.jobURL != nil {
		st.TargetURL = r.jobURL(job.ID)
	}

	err := r.poster.CreateStatus(ctx, job.Repo, job.CommitSHA, st)
	switch {
	case errors.Is(err, github.ErrTemporary):
		if ctx.Err() == nil {
			r.log.WarnContext(ctx, "GitHub unavailable, will retry commit status", "job_id", job.ID, "error", err)
		}
		return false
	case err != nil:
		// Retrying cannot fix a missing permission or a deleted commit.
		r.log.WarnContext(ctx, "could not set commit status, giving up", "job_id", job.ID, "error", err)
	default:
		r.log.DebugContext(ctx, "commit status set", "job_id", job.ID, "state", st.State)
	}

	if err := r.store.MarkReported(ctx, job.ID, job.Status); err != nil {
		r.log.ErrorContext(ctx, "record reported status", "job_id", job.ID, "error", err)
		return false
	}
	return true
}

// Describe returns the commit status for job, without a target URL.
func Describe(job models.Job) github.CommitStatus {
	st := github.CommitStatus{Context: Context}
	switch job.Status {
	case models.JobStatusPending:
		st.State, st.Description = "pending", "Waiting for an agent"
	case models.JobStatusRunning:
		st.State, st.Description = "pending", "Running on "+job.Agent
	case models.JobStatusSuccess:
		st.State, st.Description = "success", "Passed"
		if d, ok := duration(job); ok {
			st.Description += " in " + d.String()
		}
	case models.JobStatusFailed:
		st.State, st.Description = "failure", failure(job)
	case models.JobStatusCanceled:
		st.State, st.Description = "error", "Canceled"
	default:
		st.State, st.Description = "error", fmt.Sprintf("Unknown status %q", job.Status)
	}
	st.Description = truncate(st.Description, maxDescription)
	return st
}

func failure(job models.Job) string {
	if job.Error != "" {
		return "Failed: " + job.Error
	}
	if job.FailedStep == nil || job.ExitCode == nil {
		return "Failed"
	}
	step := *job.FailedStep
	if step >= 0 && step < len(job.StepNames) {
		return fmt.Sprintf("Step %q failed with exit code %d", job.StepNames[step], *job.ExitCode)
	}
	if step == 0 {
		return fmt.Sprintf("Clone failed with exit code %d", *job.ExitCode)
	}
	return fmt.Sprintf("Step %d failed with exit code %d", step, *job.ExitCode)
}

func duration(job models.Job) (time.Duration, bool) {
	if job.StartedAt == nil || job.FinishedAt == nil {
		return 0, false
	}
	return job.FinishedAt.Sub(*job.StartedAt).Round(time.Second), true
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}
