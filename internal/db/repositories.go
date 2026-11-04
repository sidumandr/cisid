package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

// PutRepository connects r.Repo, or updates its clone URL and default
// branch when it is connected already, and reports whether it is new.
func (s *Store) PutRepository(ctx context.Context, r models.Repository) (bool, error) {
	const q = `
		INSERT INTO repositories (repo, clone_url, default_branch, added_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (repo) DO UPDATE SET clone_url = EXCLUDED.clone_url, default_branch = EXCLUDED.default_branch
		RETURNING xmax = 0`
	var created bool
	if err := s.pool.QueryRow(ctx, q, r.Repo, r.CloneURL, r.DefaultBranch, r.AddedBy).Scan(&created); err != nil {
		return false, fmt.Errorf("connect repository %s: %w", r.Repo, err)
	}
	return created, nil
}

// DeleteRepository disconnects repo: it is no longer listed, polled or
// built on a schedule. Its jobs, secrets and notifications stay. It
// reports whether there was anything to disconnect.
func (s *Store) DeleteRepository(ctx context.Context, repo string) (bool, error) {
	// One statement, so the three go together or not at all.
	const q = `
		WITH r AS (DELETE FROM repositories WHERE repo = $1 RETURNING 1),
		     w AS (DELETE FROM watches WHERE repo = $1 RETURNING 1),
		     s AS (DELETE FROM schedules WHERE repo = $1 RETURNING 1)
		SELECT (SELECT count(*) FROM r) + (SELECT count(*) FROM w) + (SELECT count(*) FROM s)`
	var removed int64
	if err := s.pool.QueryRow(ctx, q, repo).Scan(&removed); err != nil {
		return false, fmt.Errorf("disconnect repository %s: %w", repo, err)
	}
	return removed > 0, nil
}

// Repositories lists the connected repositories by key, and whether a
// branch of each is polled.
func (s *Store) Repositories(ctx context.Context) ([]models.Repository, error) {
	const q = `
		SELECT r.repo, r.clone_url, r.default_branch, r.added_by, r.added_at,
		       EXISTS (SELECT 1 FROM watches w WHERE w.repo = r.repo)
		FROM repositories r ORDER BY r.repo`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	repos, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.Repository])
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	return repos, nil
}
