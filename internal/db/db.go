// Package db provides PostgreSQL access.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/sidumandr/cisid/pkg/models"
)

// Store is the PostgreSQL-backed persistence layer.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store that uses pool for all queries.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
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
