package db

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

// ErrNotificationNotFound is returned when a repository has no
// notification of the given name.
var ErrNotificationNotFound = errors.New("notification not found")

const notificationColumns = "id, repo, name, kind, hint, events, branches, pull_requests, last_sent_at, last_error, updated_at"

// SealedNotification is a notification with its sealed target.
type SealedNotification struct {
	models.Notification
	Sealed []byte
}

func scanNotification(row pgx.CollectableRow) (models.Notification, error) {
	var n models.Notification
	err := row.Scan(&n.ID, &n.Repo, &n.Name, &n.Kind, &n.Target, &n.On, &n.Branches, &n.PullRequests,
		&n.LastSentAt, &n.LastError, &n.UpdatedAt)
	return n, err
}

func scanSealedNotification(row pgx.CollectableRow) (SealedNotification, error) {
	var n SealedNotification
	err := row.Scan(&n.ID, &n.Repo, &n.Name, &n.Kind, &n.Target, &n.On, &n.Branches, &n.PullRequests,
		&n.LastSentAt, &n.LastError, &n.UpdatedAt, &n.Sealed)
	return n, err
}

// PutNotification creates the notification n.Name of n.Repo, or replaces
// it, with its target sealed. A replaced notification forgets how its last
// attempt went.
func (s *Store) PutNotification(ctx context.Context, n models.Notification, sealed []byte) error {
	const q = `
		INSERT INTO notifications (repo, name, kind, target, hint, events, branches, pull_requests)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (repo, name) DO UPDATE SET
			kind = EXCLUDED.kind, target = EXCLUDED.target, hint = EXCLUDED.hint,
			events = EXCLUDED.events, branches = EXCLUDED.branches, pull_requests = EXCLUDED.pull_requests,
			last_sent_at = NULL, last_error = '', updated_at = now()`
	branches := n.Branches
	if branches == nil {
		branches = []string{}
	}
	if _, err := s.pool.Exec(ctx, q, n.Repo, n.Name, n.Kind, sealed, n.Target, n.On, branches, n.PullRequests); err != nil {
		return fmt.Errorf("store notification %s of %s: %w", n.Name, n.Repo, err)
	}
	return nil
}

// DeleteNotification removes notification name of repo and reports
// whether it existed.
func (s *Store) DeleteNotification(ctx context.Context, repo, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM notifications WHERE repo = $1 AND name = $2", repo, name)
	if err != nil {
		return false, fmt.Errorf("delete notification %s of %s: %w", name, repo, err)
	}
	return tag.RowsAffected() > 0, nil
}

// Notifications lists the notifications of repo by name, without their
// targets.
func (s *Store) Notifications(ctx context.Context, repo string) ([]models.Notification, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+notificationColumns+" FROM notifications WHERE repo = $1 ORDER BY name", repo)
	if err != nil {
		return nil, fmt.Errorf("list notifications of %s: %w", repo, err)
	}
	list, err := pgx.CollectRows(rows, scanNotification)
	if err != nil {
		return nil, fmt.Errorf("list notifications of %s: %w", repo, err)
	}
	return list, nil
}

// SealedNotifications lists the notifications of repo by name, with their
// sealed targets.
func (s *Store) SealedNotifications(ctx context.Context, repo string) ([]SealedNotification, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+notificationColumns+", target FROM notifications WHERE repo = $1 ORDER BY name", repo)
	if err != nil {
		return nil, fmt.Errorf("read notifications of %s: %w", repo, err)
	}
	list, err := pgx.CollectRows(rows, scanSealedNotification)
	if err != nil {
		return nil, fmt.Errorf("read notifications of %s: %w", repo, err)
	}
	return list, nil
}

// SealedNotification returns notification name of repo with its sealed
// target, or ErrNotificationNotFound.
func (s *Store) SealedNotification(ctx context.Context, repo, name string) (SealedNotification, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+notificationColumns+", target FROM notifications WHERE repo = $1 AND name = $2", repo, name)
	if err != nil {
		return SealedNotification{}, fmt.Errorf("read notification %s of %s: %w", name, repo, err)
	}
	n, err := pgx.CollectExactlyOneRow(rows, scanSealedNotification)
	if errors.Is(err, pgx.ErrNoRows) {
		return SealedNotification{}, fmt.Errorf("%w: %s of %s", ErrNotificationNotFound, name, repo)
	}
	if err != nil {
		return SealedNotification{}, fmt.Errorf("read notification %s of %s: %w", name, repo, err)
	}
	return n, nil
}

// RecordNotification records how sending notification id went: sent when
// problem is empty, else why it failed.
func (s *Store) RecordNotification(ctx context.Context, id int64, problem string) error {
	q := "UPDATE notifications SET last_sent_at = now(), last_error = '' WHERE id = $1"
	args := []any{id}
	if problem != "" {
		q = "UPDATE notifications SET last_error = $2 WHERE id = $1"
		args = append(args, problem)
	}
	if _, err := s.pool.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("record notification %d: %w", id, err)
	}
	return nil
}

// ClaimFinishedJobs takes up to limit finished jobs whose results no
// server has taken to announce yet, oldest first. Servers sharing the
// database never take the same job: the rows stay locked, and are skipped
// by the others, until the claim is committed.
func (s *Store) ClaimFinishedJobs(ctx context.Context, limit int) ([]models.Job, error) {
	q := `
		UPDATE jobs SET notified_at = now()
		WHERE id IN (
			SELECT id FROM jobs
			WHERE notified_at IS NULL AND status IN ('success', 'failed', 'canceled')
			ORDER BY id LIMIT $1
			FOR UPDATE SKIP LOCKED)
		RETURNING ` + jobColumns
	rows, err := s.pool.Query(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("claim finished jobs: %w", err)
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.Job, error) { return scanJob(row) })
	if err != nil {
		return nil, fmt.Errorf("claim finished jobs: %w", err)
	}
	slices.SortFunc(jobs, func(a, b models.Job) int { return cmp.Compare(a.ID, b.ID) })
	return jobs, nil
}

// PreviousResult returns how the build before job went: the status of the
// latest job before it that passed or failed on the same line, the same
// branch (pushes, manual and scheduled builds alike) or the same pull
// request, and with the same matrix combination. ok is false when there is
// none.
func (s *Store) PreviousResult(ctx context.Context, job models.Job) (status models.JobStatus, ok bool, err error) {
	q := `SELECT status FROM jobs WHERE repo = $1 AND id < $2 AND status IN ('success', 'failed')`
	args := []any{job.Repo, job.ID}
	if job.Event == models.EventPullRequest {
		q += ` AND event = 'pull_request' AND pull_request = $3`
		args = append(args, job.PullRequest)
	} else {
		q += ` AND event <> 'pull_request' AND branch = $3`
		args = append(args, job.Branch)
	}
	if job.Matrix == nil {
		q += ` AND matrix IS NULL`
	} else {
		q += ` AND matrix = $4`
		args = append(args, job.Matrix)
	}
	q += ` ORDER BY id DESC LIMIT 1`

	err = s.pool.QueryRow(ctx, q, args...).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find the build before job %d: %w", job.ID, err)
	}
	return status, true, nil
}

// BranchResult reports how the latest build of branch in repo went that
// passed or failed: failed when any of its matrix combinations failed,
// counting only the latest run of each (a rerun replaces the run before
// it). Pull request builds do not count. ok is false when the branch has
// no such build.
func (s *Store) BranchResult(ctx context.Context, repo, branch string) (failed, ok bool, err error) {
	const q = `
		WITH last AS (
			SELECT COALESCE(parent_id, id) AS root FROM jobs
			WHERE repo = $1 AND branch = $2 AND event <> 'pull_request' AND status IN ('success', 'failed')
			ORDER BY id DESC LIMIT 1
		), latest AS (
			SELECT DISTINCT ON (j.matrix) j.status FROM last
			JOIN jobs j ON j.id = last.root OR j.parent_id = last.root
			WHERE j.status IN ('success', 'failed') AND j.event <> 'pull_request'
			ORDER BY j.matrix, j.id DESC
		)
		SELECT bool_or(status = 'failed') FROM latest`
	var res *bool
	if err := s.pool.QueryRow(ctx, q, repo, branch).Scan(&res); err != nil {
		return false, false, fmt.Errorf("find the latest build of %s %s: %w", repo, branch, err)
	}
	if res == nil {
		return false, false, nil
	}
	return *res, true, nil
}

// RepoHasJobs reports whether repo has ever been built.
func (s *Store) RepoHasJobs(ctx context.Context, repo string) (bool, error) {
	var ok bool
	if err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE repo = $1)", repo).Scan(&ok); err != nil {
		return false, fmt.Errorf("look up jobs of %s: %w", repo, err)
	}
	return ok, nil
}
