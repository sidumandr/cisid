// Package db provides PostgreSQL access.
package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// jobColumns is the column list scanned by scanJob.
const jobColumns = `id, repo, clone_url, branch, commit_sha, status, created_at,
	COALESCE(agent, ''), started_at, exit_code, failed_step, COALESCE(error, ''), finished_at`

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

// ClaimJob marks the oldest pending job as running on agent and returns it.
// Concurrent callers never receive the same job (FOR UPDATE SKIP LOCKED).
func (s *Store) ClaimJob(ctx context.Context, agent string) (models.Job, error) {
	q := `
		UPDATE jobs
		SET status = 'running', agent = $1, started_at = now(), updated_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'pending'
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING ` + jobColumns

	job, err := scanJob(s.pool.QueryRow(ctx, q, agent))
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
		SET status = 'pending', agent = NULL, started_at = NULL, updated_at = now()
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
		SET status = 'pending', agent = NULL, started_at = NULL, updated_at = now()
		WHERE status = 'running' AND agent = $1 AND id <> $2`

	tag, err := s.pool.Exec(ctx, q, agent, keep)
	if err != nil {
		return 0, fmt.Errorf("requeue jobs of %s: %w", agent, err)
	}
	return int(tag.RowsAffected()), nil
}

// FinishJob records the result of a job. The job must be running on agent,
// or pending again after a requeue whose late result is still welcome.
func (s *Store) FinishJob(ctx context.Context, agent string, res models.JobResult) error {
	if !res.Status.Finished() {
		return fmt.Errorf("%w: job %d has status %q", ErrInvalidJobResult, res.JobID, res.Status)
	}

	const q = `
		UPDATE jobs
		SET status = $3, agent = $2, exit_code = $4, failed_step = $5, error = NULLIF($6, ''),
		    finished_at = now(), updated_at = now()
		WHERE id = $1 AND ((status = 'running' AND agent = $2) OR status = 'pending')`

	tag, err := s.pool.Exec(ctx, q, res.JobID, agent, res.Status, res.ExitCode, res.FailedStep, res.Error)
	if err != nil {
		return fmt.Errorf("finish job %d: %w", res.JobID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: job %d is not running on %s", ErrJobConflict, res.JobID, agent)
	}
	return nil
}

// AppendJobLog stores a log chunk. A chunk already stored (same job and
// seq) is ignored, so resending is safe.
func (s *Store) AppendJobLog(ctx context.Context, l models.JobLog) error {
	const q = `
		INSERT INTO job_logs (job_id, seq, step, stream, data)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (job_id, seq) DO NOTHING`

	// PostgreSQL text cannot hold NUL bytes.
	data := strings.ReplaceAll(l.Data, "\x00", "")
	if _, err := s.pool.Exec(ctx, q, l.JobID, l.Seq, l.Step, l.Stream, data); err != nil {
		return fmt.Errorf("append log %d of job %d: %w", l.Seq, l.JobID, err)
	}
	return nil
}

// JobLogs returns the stored log chunks of a job in seq order.
func (s *Store) JobLogs(ctx context.Context, jobID int64) ([]models.JobLog, error) {
	const q = `SELECT job_id, step, stream, seq, data FROM job_logs WHERE job_id = $1 ORDER BY seq`

	rows, err := s.pool.Query(ctx, q, jobID)
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
		&j.Agent, &j.StartedAt, &j.ExitCode, &j.FailedStep, &j.Error, &j.FinishedAt)
	return j, err
}
