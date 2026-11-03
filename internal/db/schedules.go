package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

const scheduleColumns = "id, repo, name, cron, branch, next_run_at, last_run_at, last_job_id"

// parked is when a schedule whose cron no longer parses runs next: never,
// rather than being claimed again every round.
var parked = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// PutSchedule creates schedule s.Name of s.Repo or replaces its cron,
// branch and next run.
func (s *Store) PutSchedule(ctx context.Context, sch models.Schedule) error {
	const q = `
		INSERT INTO schedules (repo, name, cron, branch, next_run_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (repo, name) DO UPDATE SET cron = EXCLUDED.cron, branch = EXCLUDED.branch,
			next_run_at = EXCLUDED.next_run_at, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, sch.Repo, sch.Name, sch.Cron, sch.Branch, sch.NextRunAt); err != nil {
		return fmt.Errorf("store schedule %s of %s: %w", sch.Name, sch.Repo, err)
	}
	return nil
}

// DeleteSchedule removes a schedule and reports whether it existed.
func (s *Store) DeleteSchedule(ctx context.Context, repo, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM schedules WHERE repo = $1 AND name = $2", repo, name)
	if err != nil {
		return false, fmt.Errorf("delete schedule %s of %s: %w", name, repo, err)
	}
	return tag.RowsAffected() > 0, nil
}

// Schedules lists the schedules of repo by name.
func (s *Store) Schedules(ctx context.Context, repo string) ([]models.Schedule, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+scheduleColumns+" FROM schedules WHERE repo = $1 ORDER BY name", repo)
	if err != nil {
		return nil, fmt.Errorf("list schedules of %s: %w", repo, err)
	}
	schedules, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.Schedule])
	if err != nil {
		return nil, fmt.Errorf("list schedules of %s: %w", repo, err)
	}
	return schedules, nil
}

// ClaimDueSchedules takes up to limit schedules due at now, moves each to
// its next run as next computes it, and returns them. Servers sharing the
// database never claim the same run: the rows stay locked, and are skipped
// by others, until the move is committed.
func (s *Store) ClaimDueSchedules(ctx context.Context, now time.Time,
	next func(cron string, after time.Time) (time.Time, error), limit int) ([]models.Schedule, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim schedules: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, "SELECT "+scheduleColumns+` FROM schedules
		WHERE next_run_at <= $1 ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim schedules: %w", err)
	}
	due, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.Schedule])
	if err != nil {
		return nil, fmt.Errorf("claim schedules: %w", err)
	}

	for i := range due {
		n, err := next(due[i].Cron, now)
		if err != nil {
			n = parked
		}
		if _, err := tx.Exec(ctx, "UPDATE schedules SET next_run_at = $2, last_run_at = $3, updated_at = now() WHERE id = $1",
			due[i].ID, n, now); err != nil {
			return nil, fmt.Errorf("claim schedule %d: %w", due[i].ID, err)
		}
		ran := now
		due[i].NextRunAt, due[i].LastRunAt = n, &ran
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim schedules: %w", err)
	}
	return due, nil
}

// MarkScheduleRun records the job a schedule's run queued.
func (s *Store) MarkScheduleRun(ctx context.Context, id, jobID int64) error {
	if _, err := s.pool.Exec(ctx, "UPDATE schedules SET last_job_id = $2 WHERE id = $1", id, jobID); err != nil {
		return fmt.Errorf("record run of schedule %d: %w", id, err)
	}
	return nil
}
