// Package db provides PostgreSQL access.
package db

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sidumandr/cisid/pkg/models"
)

var (
	// ErrJobNotFound is returned when a job does not exist.
	ErrJobNotFound = errors.New("job not found")
	// ErrNoPendingJob is returned by ClaimJob when the queue is empty.
	ErrNoPendingJob = errors.New("no pending job")
	// ErrJobConflict is returned when a job is not in the state, or not
	// owned by the agent, that an update requires.
	ErrJobConflict = errors.New("job state conflict")
	// ErrInvalidJobResult is returned for a result with a non-terminal status.
	ErrInvalidJobResult = errors.New("invalid job result")
	// ErrDuplicateDelivery is returned by QueueJob for a webhook payload
	// that already queued a job.
	ErrDuplicateDelivery = errors.New("duplicate webhook delivery")
)

// JobEventsChannel is the PostgreSQL NOTIFY channel on which the ID of a
// job is published whenever it gets new log output or finishes.
const JobEventsChannel = "cisid_job_events"

// requeueSet puts a job back into the queue, or cancels it if a cancel was
// requested while it ran: running it again would defeat the request.
const requeueSet = `
	status = CASE WHEN cancel_requested_at IS NULL THEN 'pending' ELSE 'canceled' END,
	agent = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE agent END,
	started_at = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE started_at END,
	finished_at = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE now() END,
	error = CASE WHEN cancel_requested_at IS NULL THEN error ELSE '` + CancelReason + `' END,
	lease_expires_at = NULL, updated_at = now()`

// CancelReason is the error recorded for a job canceled through the API.
const CancelReason = "canceled by request"

// cancelSet cancels a pending job at once and asks the agent of a running
// one to stop it (see CancelRequestedJobs).
const cancelSet = `
	status = CASE WHEN status = 'pending' THEN 'canceled' ELSE status END,
	finished_at = CASE WHEN status = 'pending' THEN now() ELSE finished_at END,
	error = CASE WHEN status = 'pending' THEN '` + CancelReason + `' ELSE error END,
	cancel_requested_at = COALESCE(cancel_requested_at, now()),
	updated_at = now()`

// querier runs statements on the pool or in a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// jobColumns is the column list scanned by scanJob.
const jobColumns = `id, repo, clone_url, branch, commit_sha, status, created_at,
	COALESCE(agent, ''), started_at, lease_expires_at, step_names,
	exit_code, failed_step, COALESCE(error, ''), finished_at, matrix, parent_id, trace_parent, logs_expired_at,
	event, COALESCE(pull_request, 0), COALESCE(target_branch, ''), fork`

// leaseUntil is the SQL expression for a lease of $N seconds from now,
// measured on the database clock so servers need not agree on the time.
func leaseUntil(param int) string {
	return fmt.Sprintf("now() + make_interval(secs => $%d)", param)
}

// Store is the PostgreSQL-backed persistence layer.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store that uses pool for all queries.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Ping checks that the database is reachable.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

// insertJob is the statement CreateJob and QueueJob insert a job with.
const insertJob = `
	INSERT INTO jobs (repo, clone_url, branch, commit_sha, status, trace_parent,
		event, pull_request, target_branch, fork)
	VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, 0), NULLIF($9, ''), $10)
	RETURNING id`

func insertJobArgs(job models.Job) []any {
	return []any{job.Repo, job.CloneURL, job.Branch, job.CommitSHA, job.Status, job.TraceParent,
		cmp.Or(job.Event, models.EventPush), job.PullRequest, job.TargetBranch, job.Fork}
}

// CreateJob inserts job and returns its generated ID.
func (s *Store) CreateJob(ctx context.Context, job models.Job) (int64, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, insertJob, insertJobArgs(job)...).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert job for %s@%s: %w", job.Repo, job.CommitSHA, err)
	}
	return id, nil
}

// QueueJob inserts job for a webhook delivery whose signed body has the
// given SHA-256, unless that body already queued a job (ErrDuplicateDelivery).
// Both happen in one transaction, so a failed insert can be redelivered.
func (s *Store) QueueJob(ctx context.Context, job models.Job, payloadSHA256, deliveryID string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("queue job: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO webhook_deliveries (payload_sha256, delivery_id) VALUES ($1, NULLIF($2, ''))
		ON CONFLICT (payload_sha256) DO NOTHING`, payloadSHA256, deliveryID)
	if err != nil {
		return 0, fmt.Errorf("record delivery %s: %w", deliveryID, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, fmt.Errorf("%w: %s", ErrDuplicateDelivery, deliveryID)
	}

	var id int64
	if err := tx.QueryRow(ctx, insertJob, insertJobArgs(job)...).Scan(&id); err != nil {
		return 0, fmt.Errorf("insert job for %s@%s: %w", job.Repo, job.CommitSHA, err)
	}
	if job.PullRequest > 0 {
		// A new commit supersedes the pull request's earlier jobs.
		if _, err := cancelPullRequestJobs(ctx, tx, job.Repo, job.PullRequest, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("queue job: %w", err)
	}
	return id, nil
}

// PruneDeliveries forgets webhook deliveries older than age and returns
// how many it removed.
func (s *Store) PruneDeliveries(ctx context.Context, age time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM webhook_deliveries WHERE received_at < now() - make_interval(secs => $1)", age.Seconds())
	if err != nil {
		return 0, fmt.Errorf("prune webhook deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ExpireLogs deletes the logs of up to batch jobs that finished more than
// age ago and returns how many jobs it expired. The jobs themselves stay,
// marked with logs_expired_at.
func (s *Store) ExpireLogs(ctx context.Context, age time.Duration, batch int) (int, error) {
	const q = `
		WITH expired AS (
			SELECT id FROM jobs
			WHERE finished_at < now() - make_interval(secs => $1) AND logs_expired_at IS NULL
			ORDER BY finished_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		), deleted AS (
			DELETE FROM job_logs WHERE job_id IN (SELECT id FROM expired)
		)
		UPDATE jobs SET logs_expired_at = now() WHERE id IN (SELECT id FROM expired)`

	tag, err := s.pool.Exec(ctx, q, age.Seconds(), batch)
	if err != nil {
		return 0, fmt.Errorf("expire job logs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// GetJob returns the job with the given ID.
func (s *Store) GetJob(ctx context.Context, id int64) (models.Job, error) {
	job, err := scanJob(s.pool.QueryRow(ctx, "SELECT "+jobColumns+" FROM jobs WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Job{}, fmt.Errorf("%w: %d", ErrJobNotFound, id)
	}
	if err != nil {
		return models.Job{}, fmt.Errorf("get job %d: %w", id, err)
	}
	return job, nil
}

// ClaimJob marks the oldest pending job as running on agent, with a lease
// that expires after lease unless renewed, and returns it. Concurrent
// callers never receive the same job (FOR UPDATE SKIP LOCKED).
func (s *Store) ClaimJob(ctx context.Context, agent string, lease time.Duration) (models.Job, error) {
	q := `
		UPDATE jobs
		SET status = 'running', agent = $1, started_at = now(), updated_at = now(),
		    lease_expires_at = ` + leaseUntil(2) + `
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING ` + jobColumns

	job, err := scanJob(s.pool.QueryRow(ctx, q, agent, lease.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Job{}, ErrNoPendingJob
	}
	if err != nil {
		return models.Job{}, fmt.Errorf("claim job for %s: %w", agent, err)
	}
	return job, nil
}

// RequeueJob puts a job that agent was running back into the queue.
func (s *Store) RequeueJob(ctx context.Context, id int64, agent string) error {
	const q = `
		UPDATE jobs
		SET ` + requeueSet + `
		WHERE id = $1 AND status = 'running' AND agent = $2`

	tag, err := s.pool.Exec(ctx, q, id, agent)
	if err != nil {
		return fmt.Errorf("requeue job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d is not running on %s", ErrJobConflict, id, agent)
	}
	return nil
}

// RequeueAgentJobs puts every job running on agent, except keep, back into
// the queue and returns how many it requeued. Used when an agent reconnects
// and reports which job it is actually running (keep = 0 for none).
func (s *Store) RequeueAgentJobs(ctx context.Context, agent string, keep int64) (int, error) {
	const q = `
		UPDATE jobs
		SET ` + requeueSet + `
		WHERE status = 'running' AND agent = $1 AND id <> $2`

	tag, err := s.pool.Exec(ctx, q, agent, keep)
	if err != nil {
		return 0, fmt.Errorf("requeue jobs of %s: %w", agent, err)
	}
	return int(tag.RowsAffected()), nil
}

// CancelJob cancels a pending job at once, or asks for a running job to be
// stopped by its agent (see CancelRequestedJobs). It returns the job after
// the change, ErrJobNotFound, or ErrJobConflict if the job already finished.
func (s *Store) CancelJob(ctx context.Context, id int64) (models.Job, error) {
	q := `UPDATE jobs SET` + cancelSet + `
		WHERE id = $1 AND status IN ('pending', 'running')
		RETURNING ` + jobColumns

	job, err := scanJob(s.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := s.GetJob(ctx, id); err != nil {
			return models.Job{}, err
		}
		return models.Job{}, fmt.Errorf("%w: job %d already finished", ErrJobConflict, id)
	}
	if err != nil {
		return models.Job{}, fmt.Errorf("cancel job %d: %w", id, err)
	}
	if _, err := s.pool.Exec(ctx, "SELECT pg_notify($1, $2)", JobEventsChannel, strconv.FormatInt(id, 10)); err != nil {
		return job, fmt.Errorf("announce canceled job %d: %w", id, err)
	}
	return job, nil
}

// CancelRequestedJobs returns the running jobs of the given agents that
// should be stopped.
func (s *Store) CancelRequestedJobs(ctx context.Context, agents []string) ([]models.Job, error) {
	if len(agents) == 0 {
		return nil, nil
	}
	q := "SELECT " + jobColumns + ` FROM jobs
		WHERE status = 'running' AND cancel_requested_at IS NOT NULL AND agent = ANY($1)
		ORDER BY id`

	rows, err := s.pool.Query(ctx, q, agents)
	if err != nil {
		return nil, fmt.Errorf("list jobs to cancel: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Job, error) { return scanJob(row) })
	if err != nil {
		return nil, fmt.Errorf("list jobs to cancel: %w", err)
	}
	return jobs, nil
}

// CancelPullRequest cancels the unfinished jobs of pull request number in
// repo, e.g. when it is closed, and returns their IDs.
func (s *Store) CancelPullRequest(ctx context.Context, repo string, number int) ([]int64, error) {
	return cancelPullRequestJobs(ctx, s.pool, repo, number, 0)
}

// cancelPullRequestJobs cancels the unfinished jobs of a pull request, only
// those older than job before unless it is 0, and announces each change.
func cancelPullRequestJobs(ctx context.Context, q querier, repo string, number int, before int64) ([]int64, error) {
	rows, err := q.Query(ctx, `UPDATE jobs SET`+cancelSet+`
		WHERE repo = $1 AND pull_request = $2 AND ($3 = 0 OR id < $3) AND status IN ('pending', 'running')
		RETURNING id`, repo, number, before)
	if err != nil {
		return nil, fmt.Errorf("cancel jobs of %s#%d: %w", repo, number, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("cancel jobs of %s#%d: %w", repo, number, err)
	}
	for _, id := range ids {
		if _, err := q.Exec(ctx, "SELECT pg_notify($1, $2)", JobEventsChannel, strconv.FormatInt(id, 10)); err != nil {
			return nil, fmt.Errorf("announce canceled job %d: %w", id, err)
		}
	}
	return ids, nil
}

// RerunJob queues a new job for the same commit as job id and returns it.
// traceParent is the trace the new job belongs to (see models.Job).
func (s *Store) RerunJob(ctx context.Context, id int64, traceParent string) (models.Job, error) {
	// A matrix job reruns its own combination only.
	const q = `
		INSERT INTO jobs (repo, clone_url, branch, commit_sha, status, matrix, parent_id, trace_parent,
			event, pull_request, target_branch, fork)
		SELECT repo, clone_url, branch, commit_sha, 'pending', matrix, parent_id, $2,
			event, pull_request, target_branch, fork
		FROM jobs WHERE id = $1
		RETURNING id`

	var newID int64
	err := s.pool.QueryRow(ctx, q, id, traceParent).Scan(&newID)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Job{}, fmt.Errorf("%w: %d", ErrJobNotFound, id)
	}
	if err != nil {
		return models.Job{}, fmt.Errorf("rerun job %d: %w", id, err)
	}
	return s.GetJob(ctx, newID)
}

// SetJobSteps records the names of the steps planned for job id.
func (s *Store) SetJobSteps(ctx context.Context, id int64, names []string) error {
	if _, err := s.pool.Exec(ctx, "UPDATE jobs SET step_names = $2 WHERE id = $1", id, names); err != nil {
		return fmt.Errorf("record steps of job %d: %w", id, err)
	}
	return nil
}

// PutSecret stores a sealed secret value, replacing an existing one.
func (s *Store) PutSecret(ctx context.Context, repo, name string, sealed []byte) error {
	const q = `
		INSERT INTO secrets (repo, name, value) VALUES ($1, $2, $3)
		ON CONFLICT (repo, name) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, repo, name, sealed); err != nil {
		return fmt.Errorf("store secret %s of %s: %w", name, repo, err)
	}
	return nil
}

// DeleteSecret removes a secret and reports whether it existed.
func (s *Store) DeleteSecret(ctx context.Context, repo, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM secrets WHERE repo = $1 AND name = $2", repo, name)
	if err != nil {
		return false, fmt.Errorf("delete secret %s of %s: %w", name, repo, err)
	}
	return tag.RowsAffected() > 0, nil
}

// SecretNames lists the secrets of repo by name, without values.
func (s *Store) SecretNames(ctx context.Context, repo string) ([]models.SecretInfo, error) {
	rows, err := s.pool.Query(ctx, "SELECT name, updated_at FROM secrets WHERE repo = $1 ORDER BY name", repo)
	if err != nil {
		return nil, fmt.Errorf("list secrets of %s: %w", repo, err)
	}
	infos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.SecretInfo])
	if err != nil {
		return nil, fmt.Errorf("list secrets of %s: %w", repo, err)
	}
	return infos, nil
}

// SealedSecrets returns the sealed values of the named secrets of repo
// that exist.
func (s *Store) SealedSecrets(ctx context.Context, repo string, names []string) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx, "SELECT name, value FROM secrets WHERE repo = $1 AND name = ANY($2)", repo, names)
	if err != nil {
		return nil, fmt.Errorf("read secrets of %s: %w", repo, err)
	}
	defer rows.Close()

	sealed := map[string][]byte{}
	for rows.Next() {
		var (
			name  string
			value []byte
		)
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("read secrets of %s: %w", repo, err)
		}
		sealed[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read secrets of %s: %w", repo, err)
	}
	return sealed, nil
}

// ExpandMatrix records the matrix combination of job id and queues a job
// for each of the sibling combinations, in one transaction, returning the
// new job IDs. A job whose combination is already recorded is left alone
// and no jobs are created.
func (s *Store) ExpandMatrix(ctx context.Context, id int64, combo map[string]string, siblings []map[string]string) ([]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("expand matrix of job %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, "UPDATE jobs SET matrix = $2 WHERE id = $1 AND matrix IS NULL", id, combo)
	if err != nil {
		return nil, fmt.Errorf("expand matrix of job %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}

	const q = `
		INSERT INTO jobs (repo, clone_url, branch, commit_sha, status, matrix, parent_id, trace_parent,
			event, pull_request, target_branch, fork)
		SELECT repo, clone_url, branch, commit_sha, 'pending', $2, id, trace_parent,
			event, pull_request, target_branch, fork
		FROM jobs WHERE id = $1
		RETURNING id`
	ids := make([]int64, 0, len(siblings))
	for _, sib := range siblings {
		var newID int64
		if err := tx.QueryRow(ctx, q, id, sib).Scan(&newID); err != nil {
			return nil, fmt.Errorf("queue matrix job for %d: %w", id, err)
		}
		ids = append(ids, newID)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("expand matrix of job %d: %w", id, err)
	}
	return ids, nil
}

// CountJobsByStatus returns the number of jobs per status.
func (s *Store) CountJobsByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, "SELECT status, count(*) FROM jobs GROUP BY status")
	if err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("count jobs: %w", err)
		}
		counts[status] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count jobs: %w", err)
	}
	return counts, nil
}

// ListJobs returns the most recent jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]models.Job, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+jobColumns+" FROM jobs ORDER BY id DESC LIMIT $1", limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Job, error) { return scanJob(row) })
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return jobs, nil
}

// UnreportedJobs returns up to limit jobs, oldest first, whose current
// status has not been reported to GitHub yet. Jobs older than a day are
// left alone.
func (s *Store) UnreportedJobs(ctx context.Context, limit int) ([]models.Job, error) {
	q := "SELECT " + jobColumns + ` FROM jobs
		WHERE reported_status IS DISTINCT FROM status AND created_at > now() - interval '1 day'
		ORDER BY id
		LIMIT $1`

	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list unreported jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Job, error) { return scanJob(row) })
	if err != nil {
		return nil, fmt.Errorf("list unreported jobs: %w", err)
	}
	return jobs, nil
}

// MarkReported records that status of job id has been reported. If the job
// has moved on since, it shows up in UnreportedJobs again.
func (s *Store) MarkReported(ctx context.Context, id int64, status models.JobStatus) error {
	if _, err := s.pool.Exec(ctx, "UPDATE jobs SET reported_status = $2 WHERE id = $1", id, status); err != nil {
		return fmt.Errorf("mark status of job %d reported: %w", id, err)
	}
	return nil
}

// RenewLeases extends the leases of all jobs running on the given agents,
// which are known to be connected, and returns how many it renewed.
func (s *Store) RenewLeases(ctx context.Context, agents []string, lease time.Duration) (int, error) {
	if len(agents) == 0 {
		return 0, nil
	}
	q := `
		UPDATE jobs SET lease_expires_at = ` + leaseUntil(2) + `
		WHERE status = 'running' AND agent = ANY($1)`

	tag, err := s.pool.Exec(ctx, q, agents, lease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("renew leases of %d agents: %w", len(agents), err)
	}
	return int(tag.RowsAffected()), nil
}

// RequeueExpired puts every running job whose lease has expired back into
// the queue and returns their IDs. Their agents are presumed gone.
func (s *Store) RequeueExpired(ctx context.Context) ([]int64, error) {
	const q = `
		UPDATE jobs
		SET ` + requeueSet + `
		WHERE status = 'running' AND lease_expires_at < now()
		RETURNING id`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("requeue expired jobs: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("requeue expired jobs: %w", err)
	}
	slices.Sort(ids)
	return ids, nil
}

// AdoptJob gives a job back to an agent that reconnects still running it:
// the job keeps running on agent with a fresh lease, even if it had been
// requeued meanwhile, as long as no other agent has claimed it.
func (s *Store) AdoptJob(ctx context.Context, agent string, id int64, lease time.Duration) error {
	q := `
		UPDATE jobs
		SET status = 'running', agent = $2, started_at = COALESCE(started_at, now()),
		    lease_expires_at = ` + leaseUntil(3) + `, updated_at = now()
		WHERE id = $1 AND (
			(status = 'running' AND agent = $2) OR (status = 'pending' AND agent IS NULL)
		)`

	tag, err := s.pool.Exec(ctx, q, id, agent, lease.Seconds())
	if err != nil {
		return fmt.Errorf("adopt job %d for %s: %w", id, agent, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d is finished or running elsewhere", ErrJobConflict, id)
	}
	return nil
}

// FinishJob records the result of a job, which must be running on agent.
func (s *Store) FinishJob(ctx context.Context, agent string, res models.JobResult) error {
	if !res.Status.Finished() {
		return fmt.Errorf("%w: job %d has status %q", ErrInvalidJobResult, res.JobID, res.Status)
	}

	const q = `
		WITH finished AS (
			UPDATE jobs
			SET status = $3, exit_code = $4, failed_step = $5, error = NULLIF($6, ''),
			    lease_expires_at = NULL, finished_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'running' AND agent = $2
			RETURNING id
		)
		SELECT pg_notify('` + JobEventsChannel + `', id::text) FROM finished`

	tag, err := s.pool.Exec(ctx, q, res.JobID, agent, res.Status, res.ExitCode, res.FailedStep, res.Error)
	if err != nil {
		return fmt.Errorf("finish job %d: %w", res.JobID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d is not running on %s", ErrJobConflict, res.JobID, agent)
	}
	return nil
}

// AppendJobLog stores a log chunk and notifies JobEventsChannel. A chunk
// already stored (same job and seq) is ignored, so resending is safe.
func (s *Store) AppendJobLog(ctx context.Context, l models.JobLog) error {
	const q = `
		WITH inserted AS (
			INSERT INTO job_logs (job_id, seq, step, stream, data)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (job_id, seq) DO NOTHING
			RETURNING job_id
		)
		SELECT pg_notify('` + JobEventsChannel + `', job_id::text) FROM inserted`

	// PostgreSQL text cannot hold NUL bytes.
	data := strings.ReplaceAll(l.Data, "\x00", "")
	if _, err := s.pool.Exec(ctx, q, l.JobID, l.Seq, l.Step, l.Stream, data); err != nil {
		return fmt.Errorf("append log %d of job %d: %w", l.Seq, l.JobID, err)
	}
	return nil
}

// JobLogs returns all stored log chunks of a job in seq order.
func (s *Store) JobLogs(ctx context.Context, jobID int64) ([]models.JobLog, error) {
	return s.JobLogsAfter(ctx, jobID, 0, -1)
}

// JobLogsAfter returns up to limit log chunks of a job with seq greater
// than after, in seq order. A negative limit means no limit.
func (s *Store) JobLogsAfter(ctx context.Context, jobID, after int64, limit int) ([]models.JobLog, error) {
	const q = `
		SELECT job_id, step, stream, seq, data FROM job_logs
		WHERE job_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT CASE WHEN $3 < 0 THEN NULL ELSE $3 END`

	rows, err := s.pool.Query(ctx, q, jobID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("query logs of job %d: %w", jobID, err)
	}
	logs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.JobLog, error) {
		var l models.JobLog
		err := row.Scan(&l.JobID, &l.Step, &l.Stream, &l.Seq, &l.Data)
		return l, err
	})
	if err != nil {
		return nil, fmt.Errorf("read logs of job %d: %w", jobID, err)
	}
	return logs, nil
}

func scanJob(row pgx.Row) (models.Job, error) {
	var j models.Job
	err := row.Scan(&j.ID, &j.Repo, &j.CloneURL, &j.Branch, &j.CommitSHA, &j.Status, &j.CreatedAt,
		&j.Agent, &j.StartedAt, &j.LeaseExpiresAt, &j.StepNames, &j.ExitCode, &j.FailedStep, &j.Error, &j.FinishedAt,
		&j.Matrix, &j.ParentID, &j.TraceParent, &j.LogsExpiredAt,
		&j.Event, &j.PullRequest, &j.TargetBranch, &j.Fork)
	return j, err
}
