package trigger

import (
	"context"
	"log/slog"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	defaultInterval = 30 * time.Second
	roundTimeout    = time.Minute
	claimLimit      = 20
)

// Store keeps schedules and queues their jobs. *db.Store satisfies it.
type Store interface {
	ClaimDueSchedules(ctx context.Context, now time.Time,
		next func(cron string, after time.Time) (time.Time, error), limit int) ([]models.Schedule, error)
	CreateJob(ctx context.Context, job models.Job) (int64, error)
	MarkScheduleRun(ctx context.Context, id, jobID int64) error
}

var _ Store = (*db.Store)(nil)

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithLogger sets the scheduler's logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Scheduler) { s.log = log }
}

// WithInterval sets how often due schedules are looked for (default 30s).
func WithInterval(d time.Duration) Option {
	return func(s *Scheduler) { s.interval = d }
}

// WithQueued registers a callback run with each job a schedule queues,
// e.g. to wake the dispatcher. It must not block.
func WithQueued(f func(jobID int64)) Option {
	return func(s *Scheduler) { s.queued = f }
}

// Scheduler queues a build of a branch's head whenever one of its schedules
// is due. A run that cannot be queued, e.g. while GitHub is unreachable, is
// skipped; the schedule goes on at its next time. Runs missed while no
// server was up are made up once, not once per missed time.
type Scheduler struct {
	store    Store
	resolver Resolver
	log      *slog.Logger
	interval time.Duration
	queued   func(jobID int64)
	now      func() time.Time
}

// NewScheduler returns a Scheduler.
func NewScheduler(store Store, resolver Resolver, opts ...Option) *Scheduler {
	s := &Scheduler{
		store:    store,
		resolver: resolver,
		log:      slog.Default(),
		interval: defaultInterval,
		queued:   func(int64) {},
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run queues the jobs of due schedules until ctx is canceled.
func (s *Scheduler) Run(ctx context.Context) error {
	tick := time.NewTicker(s.interval)
	defer tick.Stop()
	for {
		s.round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// round claims the due schedules and queues their jobs. It is not cut
// short by ctx, so a claimed run is not lost on shutdown.
func (s *Scheduler) round(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roundTimeout)
	defer cancel()

	due, err := s.store.ClaimDueSchedules(ctx, s.now(), Next, claimLimit)
	if err != nil {
		s.log.ErrorContext(ctx, "claim due schedules", "error", err)
		return
	}
	for _, sch := range due {
		job, err := BranchJob(ctx, s.resolver, sch.Repo, sch.Branch, models.EventSchedule)
		if err != nil {
			s.log.WarnContext(ctx, "scheduled build skipped", "repo", sch.Repo, "schedule", sch.Name, "error", err)
			continue
		}
		id, err := s.store.CreateJob(ctx, job)
		if err != nil {
			s.log.ErrorContext(ctx, "queue scheduled build", "repo", sch.Repo, "schedule", sch.Name, "error", err)
			continue
		}
		if err := s.store.MarkScheduleRun(ctx, sch.ID, id); err != nil {
			s.log.WarnContext(ctx, "record scheduled build", "schedule", sch.Name, "job_id", id, "error", err)
		}
		s.log.InfoContext(ctx, "scheduled build queued", "repo", sch.Repo, "schedule", sch.Name,
			"branch", job.Branch, "job_id", id, "next_run_at", sch.NextRunAt)
		s.queued(id)
	}
}
