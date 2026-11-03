// Package hub is the server side of the agent protocol: it keeps the
// WebSocket connections of agents, hands pending jobs to idle agents and
// stores the logs and results they send back.
package hub

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	defaultPollInterval = 5 * time.Second
	defaultDrainTimeout = 5 * time.Second
	defaultLease        = 5 * time.Minute
	cancelPollInterval  = 2 * time.Second
	roundTimeout        = 30 * time.Second
)

// Store is the persistence the hub needs. *db.Store satisfies it.
type Store interface {
	ClaimJob(ctx context.Context, agent string, lease time.Duration) (models.Job, error)
	RenewLeases(ctx context.Context, agents []string, lease time.Duration) (int, error)
	RequeueExpired(ctx context.Context) ([]int64, error)
	AdoptJob(ctx context.Context, agent string, id int64, lease time.Duration) error
	CancelRequestedJobs(ctx context.Context, agents []string) ([]models.Job, error)
	SetJobSteps(ctx context.Context, id int64, names []string) error
	RequeueJob(ctx context.Context, id int64, agent string) error
	RequeueAgentJobs(ctx context.Context, agent string, keep int64) (int, error)
	FinishJob(ctx context.Context, agent string, res models.JobResult) error
	AppendJobLog(ctx context.Context, l models.JobLog) error
}

var _ Store = (*db.Store)(nil)

// Planner turns a job into what an agent runs. pipeline.Planner satisfies it.
type Planner interface {
	Plan(ctx context.Context, job models.Job) (models.JobAssignment, error)
}

var _ Planner = pipeline.Planner{}

// Option configures a Hub.
type Option func(*Hub)

// WithLogger sets the hub's logger.
func WithLogger(log *slog.Logger) Option {
	return func(h *Hub) { h.log = log }
}

// WithPollInterval sets how often the hub looks for pending jobs without
// being woken, e.g. for jobs created by another server (default 5s).
func WithPollInterval(d time.Duration) Option {
	return func(h *Hub) { h.pollInterval = d }
}

// WithDrainTimeout sets how long shutdown waits for agents to close their
// connections after server_closing before closing them itself (default 5s).
func WithDrainTimeout(d time.Duration) Option {
	return func(h *Hub) { h.drainTimeout = d }
}

// WithLease sets how long a job stays assigned to an agent that is no
// longer connected before it goes back to the queue (default 5m). It should
// comfortably exceed a server restart plus an agent's reconnect backoff.
func WithLease(d time.Duration) Option {
	return func(h *Hub) { h.lease = d }
}

// WithJobChanged registers a callback run after the hub changes a job's
// status (assigned, finished, failed), e.g. to report it elsewhere. It must
// not block.
func WithJobChanged(f func(jobID int64)) Option {
	return func(h *Hub) { h.jobChanged = f }
}

// Hub dispatches jobs to connected agents. Serve agents with ServeHTTP and
// run the dispatcher with Run.
type Hub struct {
	store        Store
	planner      Planner
	log          *slog.Logger
	pollInterval time.Duration
	drainTimeout time.Duration
	lease        time.Duration
	jobChanged   func(jobID int64)
	wake         chan struct{}

	mu      sync.Mutex
	agents  map[string]*agentConn
	closed  bool
	closing chan struct{}
	conns   sync.WaitGroup
}

// agentConn is one connected agent.
type agentConn struct {
	name string
	// out feeds the connection's writer goroutine.
	out chan models.Message
	// kick is closed when a newer connection with the same name replaces this one.
	kick chan struct{}
	// drain is closed on shutdown; the writer then sends server_closing.
	drain chan struct{}
	// processed is the ID of the last agent message handled; ackReady
	// tells the writer to acknowledge it.
	processed atomic.Int64
	ackReady  chan struct{}

	// Guarded by Hub.mu.
	jobID      int64 // job the agent is running, 0 when idle
	blocked    bool  // rejected work; no new jobs until it reports a result
	cancelSent int64 // job a cancel request was last sent for
}

// New returns a Hub.
func New(store Store, planner Planner, opts ...Option) *Hub {
	h := &Hub{
		store:        store,
		planner:      planner,
		log:          slog.Default(),
		pollInterval: defaultPollInterval,
		drainTimeout: defaultDrainTimeout,
		lease:        defaultLease,
		jobChanged:   func(int64) {},
		wake:         make(chan struct{}, 1),
		agents:       map[string]*agentConn{},
		closing:      make(chan struct{}),
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Wake asks the dispatcher to look for pending jobs now. It never blocks.
func (h *Hub) Wake() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

// Run dispatches jobs until ctx is canceled. It then asks every agent to
// close its connection (see models.MsgServerClosing), waits for them to end
// and returns nil.
func (h *Hub) Run(ctx context.Context) error {
	defer h.shutdown()

	tick := time.NewTicker(h.pollInterval)
	defer tick.Stop()
	leaseTick := time.NewTicker(h.lease / 3)
	defer leaseTick.Stop()
	cancelTick := time.NewTicker(cancelPollInterval)
	defer cancelTick.Stop()

	h.round(ctx, h.dispatch)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.wake:
			h.round(ctx, h.deliverCancels, h.dispatch)
		case <-tick.C:
			h.round(ctx, h.dispatch)
		case <-leaseTick.C:
			h.round(ctx, h.maintainLeases, h.dispatch)
		case <-cancelTick.C:
			// Cancel requests may come through another server's API.
			h.round(ctx, h.deliverCancels)
		}
	}
}

// round runs one round of background work to completion even if ctx is
// canceled meanwhile; Run stops between rounds. Canceling a query midway
// makes the driver close its connection in the background, which would
// hold up closing the database pool on shutdown.
func (h *Hub) round(ctx context.Context, work ...func(context.Context)) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), roundTimeout)
	defer cancel()
	for _, w := range work {
		w(ctx)
	}
}

// deliverCancels sends job_cancel to agents connected here that hold a job
// with a cancel request, once per job and connection.
func (h *Hub) deliverCancels(ctx context.Context) {
	names := h.agentNames()
	jobs, err := h.store.CancelRequestedJobs(ctx, names)
	if err != nil {
		if ctx.Err() == nil {
			h.log.ErrorContext(ctx, "list jobs to cancel", "error", err)
		}
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, job := range jobs {
		a := h.agents[job.Agent]
		if a == nil || a.jobID != job.ID || a.cancelSent == job.ID {
			continue
		}
		msg, err := models.NewMessage(models.MsgJobCancel, models.JobCancel{JobID: job.ID})
		if err != nil {
			continue
		}
		select {
		case a.out <- msg:
			a.cancelSent = job.ID
			h.log.InfoContext(ctx, "asked agent to cancel job", "job_id", job.ID, "agent", a.name)
		default:
		}
	}
}

func (h *Hub) agentNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	names := make([]string, 0, len(h.agents))
	for name := range h.agents {
		names = append(names, name)
	}
	return names
}

// maintainLeases renews the leases of jobs on agents connected here and
// requeues jobs whose lease expired, i.e. whose agent has been gone for a
// whole lease on every server. Requeued jobs are dispatched right after.
func (h *Hub) maintainLeases(ctx context.Context) {
	names := h.agentNames()
	if _, err := h.store.RenewLeases(ctx, names, h.lease); err != nil && ctx.Err() == nil {
		h.log.ErrorContext(ctx, "renew job leases", "error", err)
	}
	ids, err := h.store.RequeueExpired(ctx)
	if err != nil && ctx.Err() == nil {
		h.log.ErrorContext(ctx, "requeue jobs with expired leases", "error", err)
	}
	if len(ids) > 0 {
		h.log.WarnContext(ctx, "requeued jobs of agents that did not come back", "job_ids", ids)
	}
}

func (h *Hub) shutdown() {
	h.mu.Lock()
	if !h.closed {
		h.closed = true
		close(h.closing)
	}
	h.mu.Unlock()
	h.conns.Wait()
}

// dispatch offers pending jobs to every idle agent.
func (h *Hub) dispatch(ctx context.Context) {
	for _, a := range h.idleAgents() {
		if !h.dispatchTo(ctx, a) {
			return
		}
	}
}

// dispatchTo claims a job for a and sends it. It returns false when there
// are no more pending jobs (or the store failed), so dispatch can stop.
func (h *Hub) dispatchTo(ctx context.Context, a *agentConn) bool {
	for {
		job, err := h.store.ClaimJob(ctx, a.name, h.lease)
		if errors.Is(err, db.ErrNoPendingJob) {
			return false
		}
		if err != nil {
			if ctx.Err() == nil {
				h.log.ErrorContext(ctx, "claim job", "agent", a.name, "error", err)
			}
			return false
		}

		asg, err := h.planner.Plan(ctx, job)
		if errors.Is(err, pipeline.ErrTemporary) {
			// Retry on a later round; continuing now would claim the same job again.
			h.log.WarnContext(ctx, "planning failed, job requeued", "job_id", job.ID, "error", err)
			h.requeue(ctx, job.ID, a.name)
			return false
		}
		if err != nil {
			h.failJob(ctx, a.name, job.ID, "plan job: "+err.Error())
			continue
		}

		asg.JobID = job.ID
		names := make([]string, len(asg.Steps))
		for i, s := range asg.Steps {
			names[i] = s.Name
		}
		if err := h.store.SetJobSteps(ctx, job.ID, names); err != nil {
			h.log.WarnContext(ctx, "record job steps", "job_id", job.ID, "error", err)
		}

		if h.assign(a, asg) {
			h.log.InfoContext(ctx, "job assigned", "job_id", job.ID, "agent", a.name,
				"steps", len(asg.Steps), "services", len(asg.Services))
			h.jobChanged(job.ID)
			return true
		}
		// The agent went away or became busy since idleAgents.
		h.requeue(ctx, job.ID, a.name)
		return true
	}
}

// requeue gives a claimed job back. It runs even when ctx is canceled, so
// a job claimed during shutdown is not left running on nobody.
func (h *Hub) requeue(ctx context.Context, jobID int64, agent string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := h.store.RequeueJob(ctx, jobID, agent); err != nil {
		h.log.ErrorContext(ctx, "requeue job", "job_id", jobID, "error", err)
	}
}

func (h *Hub) failJob(ctx context.Context, agent string, jobID int64, reason string) {
	h.log.WarnContext(ctx, "failing job", "job_id", jobID, "reason", reason)
	err := h.store.FinishJob(ctx, agent, models.JobResult{
		JobID: jobID, Status: models.JobStatusFailed, ExitCode: -1, Error: reason,
	})
	if err != nil {
		h.log.ErrorContext(ctx, "record job failure", "job_id", jobID, "error", err)
		return
	}
	h.jobChanged(jobID)
}

// assign queues the assignment on a's connection if a is still the
// registered, idle connection for its name.
func (h *Hub) assign(a *agentConn, asg models.JobAssignment) bool {
	msg, err := models.NewMessage(models.MsgJobAssigned, asg)
	if err != nil {
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.agents[a.name] != a || a.jobID != 0 || a.blocked {
		return false
	}
	select {
	case a.out <- msg:
		a.jobID = asg.JobID
		return true
	default:
		return false
	}
}

// idleAgents returns the agents that can take a job, sorted by name.
func (h *Hub) idleAgents() []*agentConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	var idle []*agentConn
	for _, a := range h.agents {
		if a.jobID == 0 && !a.blocked {
			idle = append(idle, a)
		}
	}
	slices.SortFunc(idle, func(x, y *agentConn) int { return cmp.Compare(x.name, y.name) })
	return idle
}

// holds reports whether the agent called name is running jobID.
func (h *Hub) holds(name string, jobID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.agents[name]
	return a != nil && jobID != 0 && a.jobID == jobID
}
