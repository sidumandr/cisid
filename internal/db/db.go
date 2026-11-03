// Package db provides PostgreSQL access.
package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
)

// JobEventsChannel is the PostgreSQL NOTIFY channel on which the ID of a
// job is published whenever it gets new log output or finishes.
const JobEventsChannel = "cisid_job_events"

// jobColumns is the column list scanned by scanJob.
const jobColumns = `id, repo, clone_url, branch, commit_sha, status, created_at,
	COALESCE(agent, ''), started_at, lease_expires_at,
	exit_code, failed_step, COALESCE(error, ''), finished_at`

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

// CreateJob inserts job and returns its generated ID.
func (s *Store) CreateJob(ctx context.Context, job models.Job) (int64, error) {
	const q = `
		INSERT INTO jobs (repo, clone_url, branch, commit_sha, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`

	var id int64
	err := s.pool.QueryRow(ctx, q, job.Repo, job.CloneURL, job.Branch, job.CommitSHA, job.Status).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert job for %s@%s: %w", job.Repo, job.CommitSHA, err)
	}
	return id, nil
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
		SET status = 'pending', agent = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = now()
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
		SET status = 'pending', agent = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = now()
		WHERE status = 'running' AND agent = $1 AND id <> $2`

	tag, err := s.pool.Exec(ctx, q, agent, keep)
	if err != nil {
		return 0, fmt.Errorf("requeue jobs of %s: %w", agent, err)
	}
	return int(tag.RowsAffected()), nil
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
		SET status = 'pending', agent = NULL, started_at = NULL, lease_expires_at = NULL, updated_at = now()
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
		&j.Agent, &j.StartedAt, &j.LeaseExpiresAt, &j.ExitCode, &j.FailedStep, &j.Error, &j.FinishedAt)
	return j, err
}
