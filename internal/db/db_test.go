package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// newTestPool connects to the database in CISID_TEST_DATABASE_URL and applies
// the migrations. The test is skipped when the variable is not set.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("CISID_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CISID_TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	migration, err := os.ReadFile("migrations/001_create_jobs.sql")
	require.NoError(t, err)
	_, err = pool.Exec(ctx, string(migration))
	require.NoError(t, err)

	return pool
}

func TestStoreCreateJob(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	store := New(pool)

	job := models.Job{
		Repo:      "sidumandr/cisid",
		CloneURL:  "https://github.com/sidumandr/cisid.git",
		Branch:    "main",
		CommitSHA: "6113728f27ae82c7b1a177c8d03f9e96e0adf246",
		Status:    models.JobStatusPending,
	}

	id, err := store.CreateJob(ctx, job)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM jobs WHERE id = $1", id)
	})
	assert.Positive(t, id)

	var got models.Job
	err = pool.QueryRow(ctx,
		"SELECT repo, clone_url, branch, commit_sha, status FROM jobs WHERE id = $1", id,
	).Scan(&got.Repo, &got.CloneURL, &got.Branch, &got.CommitSHA, &got.Status)
	require.NoError(t, err)

	job.ID = 0
	assert.Equal(t, job, got)
}
