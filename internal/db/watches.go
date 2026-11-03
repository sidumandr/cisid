package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

const watchColumns = "id, repo, branch, clone_url, last_sha, last_poll_at, last_job_id, last_error, created_at"

// PutWatch starts watching w.Branch of w.Repo, or updates its clone URL,
// and reports whether the watch is new. A new watch is polled at once.
func (s *Store) PutWatch(ctx context.Context, w models.Watch) (bool, error) {
	const q = `
		INSERT INTO watches (repo, branch, clone_url) VALUES ($1, $2, $3)
		ON CONFLICT (repo, branch) DO UPDATE SET clone_url = EXCLUDED.clone_url
		RETURNING xmax = 0`
	var created bool
	if err := s.pool.QueryRow(ctx, q, w.Repo, w.Branch, w.CloneURL).Scan(&created); err != nil {
		return false, fmt.Errorf("store watch of %s %s: %w", w.Repo, w.Branch, err)
	}
	return created, nil
}

// DeleteWatch stops watching branch of repo and reports whether it was
// watched.
func (s *Store) DeleteWatch(ctx context.Context, repo, branch string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM watches WHERE repo = $1 AND branch = $2", repo, branch)
	if err != nil {
		return false, fmt.Errorf("delete watch of %s %s: %w", repo, branch, err)
	}
	return tag.RowsAffected() > 0, nil
}

// Watches lists the watched branches of repo by name.
func (s *Store) Watches(ctx context.Context, repo string) ([]models.Watch, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+watchColumns+" FROM watches WHERE repo = $1 ORDER BY branch", repo)
	if err != nil {
		return nil, fmt.Errorf("list watches of %s: %w", repo, err)
	}
	watches, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.Watch])
	if err != nil {
		return nil, fmt.Errorf("list watches of %s: %w", repo, err)
	}
	return watches, nil
}

// ClaimDueWatches takes up to limit watches due at now, moves each to its
// next poll, every later, and returns them. Servers sharing the database
// never claim the same poll: the rows stay locked, and are skipped by
// others, until the move is committed.
func (s *Store) ClaimDueWatches(ctx context.Context, now time.Time, every time.Duration, limit int) ([]models.Watch, error) {
	const q = `
		WITH due AS (
			SELECT id FROM watches WHERE next_poll_at <= $1
			ORDER BY next_poll_at LIMIT $3 FOR UPDATE SKIP LOCKED
		)
		UPDATE watches w SET next_poll_at = $1 + make_interval(secs => $2), last_poll_at = $1
		FROM due WHERE w.id = due.id
		RETURNING ` + "w.id, w.repo, w.branch, w.clone_url, w.last_sha, w.last_poll_at, w.last_job_id, w.last_error, w.created_at"
	rows, err := s.pool.Query(ctx, q, now, every.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim watches: %w", err)
	}
	watches, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.Watch])
	if err != nil {
		return nil, fmt.Errorf("claim watches: %w", err)
	}
	return watches, nil
}

// QueuePolledJob queues job for the new head of w's branch, unless another
// poll already moved the watch past w.LastSHA; queued is false then. The
// watch moves to job's commit in the same transaction.
func (s *Store) QueuePolledJob(ctx context.Context, w models.Watch, job models.Job) (id int64, queued bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("queue polled job: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, "UPDATE watches SET last_sha = $2, last_error = '' WHERE id = $1 AND last_sha = $3",
		w.ID, job.CommitSHA, w.LastSHA)
	if err != nil {
		return 0, false, fmt.Errorf("move watch %d to %s: %w", w.ID, job.CommitSHA, err)
	}
	if tag.RowsAffected() == 0 {
		return 0, false, nil
	}
	if err := tx.QueryRow(ctx, insertJob, insertJobArgs(job)...).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("insert job for %s@%s: %w", job.Repo, job.CommitSHA, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE watches SET last_job_id = $2 WHERE id = $1", w.ID, id); err != nil {
		return 0, false, fmt.Errorf("record job of watch %d: %w", w.ID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("queue polled job: %w", err)
	}
	return id, true, nil
}

// SetPollError records why the last poll of watch id failed; an empty msg
// clears it.
func (s *Store) SetPollError(ctx context.Context, id int64, msg string) error {
	if _, err := s.pool.Exec(ctx, "UPDATE watches SET last_error = $2 WHERE id = $1", id, msg); err != nil {
		return fmt.Errorf("record poll of watch %d: %w", id, err)
	}
	return nil
}
