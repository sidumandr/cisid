// Package dbtest provides throwaway PostgreSQL databases for tests.
package dbtest

import (
	"context"
	"crypto/rand"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvURL names the variable holding a PostgreSQL URL with CREATE DATABASE rights.
const EnvURL = "CISID_TEST_DATABASE_URL"

// connect retries for a while, so tests can start alongside a database
// service that is still booting.
func connect(ctx context.Context, url string) (*pgx.Conn, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := pgx.Connect(ctx, url)
		if err == nil || time.Now().After(deadline) {
			return conn, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// New creates an empty database, connects to it and drops it when the test
// ends. The test is skipped when EnvURL is not set. Callers apply
// migrations themselves.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), NewURL(t))
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	t.Cleanup(pool.Close) // runs before the drop registered by NewURL
	return pool
}

// NewURL creates an empty database, returns its connection URL and drops it
// when the test ends. The test is skipped when EnvURL is not set.
func NewURL(t testing.TB) string {
	t.Helper()

	base := os.Getenv(EnvURL)
	if base == "" {
		t.Skip(EnvURL + " not set; skipping PostgreSQL integration test")
	}

	ctx := context.Background()
	admin, err := connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvURL, err)
	}

	// The name is generated here, never taken from input.
	name := "cisid_test_" + strings.ToLower(rand.Text()[:10])
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close(context.Background())
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %s: %v", EnvURL, err)
	}
	u.Path = "/" + name
	return u.String()
}
