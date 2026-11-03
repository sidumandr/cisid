package trigger

import (
	"context"
	"log/slog"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	// DefaultPollInterval is how often each watched branch is polled.
	DefaultPollInterval = time.Minute
	// MinPollInterval keeps polling from eating the forges' rate limits.
	MinPollInterval = 10 * time.Second

	pollTick         = 5 * time.Second
	pollRoundTimeout = 2 * time.Minute
	pollLimit        = 50
)

// PollStore keeps watched branches and queues their builds. *db.Store
// satisfies it.
type PollStore interface {
	ClaimDueWatches(ctx context.Context, now time.Time, every time.Duration, limit int) ([]models.Watch, error)
	QueuePolledJob(ctx context.Context, w models.Watch, job models.Job) (id int64, queued bool, err error)
	SetPollError(ctx context.Context, id int64, msg string) error
}

var _ PollStore = (*db.Store)(nil)

// HeadResolver finds the commit a branch points at. *forge.Set satisfies it.
type HeadResolver interface {
	BranchHead(ctx context.Context, repo, branch string) (string, error)
}

// PollerOption configures a Poller.
type PollerOption func(*Poller)

// WithPollLogger sets the poller's logger.
func WithPollLogger(log *slog.Logger) PollerOption {
	return func(p *Poller) { p.log = log }
}

// WithPollQueued registers a callback run with each job a poll queues,
// e.g. to wake the dispatcher. It must not block.
func WithPollQueued(f func(jobID int64)) PollerOption {
	return func(p *Poller) { p.queued = f }
}

// Poller builds watched branches whenever their head moves, for servers
// the forges' webhooks cannot reach. A poll's build is a push build.
type Poller struct {
	store  PollStore
	heads  HeadResolver
	every  time.Duration
	log    *slog.Logger
	queued func(jobID int64)
	tick   time.Duration
	now    func() time.Time
}

// NewPoller returns a Poller that polls each watched branch every every.
func NewPoller(store PollStore, heads HeadResolver, every time.Duration, opts ...PollerOption) *Poller {
	p := &Poller{
		store:  store,
		heads:  heads,
		every:  every,
		log:    slog.Default(),
		queued: func(int64) {},
		tick:   min(pollTick, every),
		now:    time.Now,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run polls due branches until ctx is canceled.
func (p *Poller) Run(ctx context.Context) error {
	tick := time.NewTicker(p.tick)
	defer tick.Stop()
	for {
		p.round(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// round claims the due watches and polls them. It is not cut short by
// ctx, so a claimed poll is not lost on shutdown.
func (p *Poller) round(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pollRoundTimeout)
	defer cancel()

	due, err := p.store.ClaimDueWatches(ctx, p.now(), p.every, pollLimit)
	if err != nil {
		p.log.ErrorContext(ctx, "claim watched branches", "error", err)
		return
	}
	for _, w := range due {
		p.poll(ctx, w)
	}
}

func (p *Poller) poll(ctx context.Context, w models.Watch) {
	sha, err := p.heads.BranchHead(ctx, w.Repo, w.Branch)
	if err != nil {
		p.log.WarnContext(ctx, "poll skipped", "repo", w.Repo, "branch", w.Branch, "error", err)
		if err := p.store.SetPollError(ctx, w.ID, err.Error()); err != nil {
			p.log.ErrorContext(ctx, "record poll", "repo", w.Repo, "branch", w.Branch, "error", err)
		}
		return
	}
	if sha == w.LastSHA {
		if w.LastError != "" {
			if err := p.store.SetPollError(ctx, w.ID, ""); err != nil {
				p.log.ErrorContext(ctx, "record poll", "repo", w.Repo, "branch", w.Branch, "error", err)
			}
		}
		return
	}

	id, queued, err := p.store.QueuePolledJob(ctx, w, models.Job{
		Repo:      w.Repo,
		CloneURL:  w.CloneURL,
		Branch:    w.Branch,
		CommitSHA: sha,
		Status:    models.JobStatusPending,
		Event:     models.EventPush,
	})
	switch {
	case err != nil:
		p.log.ErrorContext(ctx, "queue polled build", "repo", w.Repo, "branch", w.Branch, "error", err)
	case queued:
		p.log.InfoContext(ctx, "polled build queued", "repo", w.Repo, "branch", w.Branch, "commit", sha, "job_id", id)
		p.queued(id)
	}
}
