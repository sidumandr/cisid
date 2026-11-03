package db

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// newTestDB creates an empty database on the server in
// CISID_TEST_DATABASE_URL and drops it when the test ends. The test is
// skipped when the variable is not set.
func newTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("CISID_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("CISID_TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	// The name is generated here, never taken from input.
	name := pgx.Identifier{"cisid_test_" + strings.ToLower(rand.Text()[:10])}.Sanitize()
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	cfg, err := pgxpool.ParseConfig(url)
	require.NoError(t, err)
	cfg.ConnConfig.Database = strings.Trim(name, `"`)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		assert.NoError(t, err)
	})
	return pool
}

func TestStoreCreateJob(t *testing.T) {
	pool := newTestDB(t)
	ctx := t.Context()
	_, err := Migrate(ctx, pool)
	require.NoError(t, err)
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
	assert.Positive(t, id)

	var got models.Job
	err = pool.QueryRow(ctx,
		"SELECT repo, clone_url, branch, commit_sha, status FROM jobs WHERE id = $1", id,
	).Scan(&got.Repo, &got.CloneURL, &got.Branch, &got.CommitSHA, &got.Status)
	require.NoError(t, err)
	assert.Equal(t, job, got)
}

func TestStorePing(t *testing.T) {
	pool := newTestDB(t)
	require.NoError(t, New(pool).Ping(t.Context()))
}
