// Package dbtest provides throwaway PostgreSQL databases for tests.
package dbtest

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvURL names the variable holding a PostgreSQL URL with CREATE DATABASE rights.
const EnvURL = "CISID_TEST_DATABASE_URL"

// New creates an empty database and drops it when the test ends. The test is
// skipped when EnvURL is not set. Callers apply migrations themselves.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv(EnvURL)
	if url == "" {
		t.Skip(EnvURL + " not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	// The name is generated here, never taken from input.
	name := "cisid_test_" + strings.ToLower(rand.Text()[:10])
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}

	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	return pool
}
