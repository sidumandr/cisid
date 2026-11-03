package db

import (
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db/dbtest"
)

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"m/010_add_index.sql":   {Data: []byte("CREATE INDEX ...")},
		"m/002_create_logs.sql": {Data: []byte("CREATE TABLE logs ()")},
		"m/001_create_jobs.sql": {Data: []byte("CREATE TABLE jobs ()")},
		"m/README.md":           {Data: []byte("not a migration")},
	}

	got, err := loadMigrations(fsys, "m")

	require.NoError(t, err)
	assert.Equal(t, []migration{
		{Version: 1, Name: "create_jobs", SQL: "CREATE TABLE jobs ()"},
		{Version: 2, Name: "create_logs", SQL: "CREATE TABLE logs ()"},
		{Version: 10, Name: "add_index", SQL: "CREATE INDEX ..."},
	}, got)
}

func TestLoadMigrationsRejectsBadFiles(t *testing.T) {
	tests := map[string]fstest.MapFS{
		"no version":        {"m/create_jobs.sql": {}},
		"zero version":      {"m/000_init.sql": {}},
		"no name":           {"m/001_.sql": {}},
		"duplicate version": {"m/001_a.sql": {}, "m/1_b.sql": {}},
	}

	for name, fsys := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadMigrations(fsys, "m")
			assert.ErrorIs(t, err, ErrInvalidMigration)
		})
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	got, err := loadMigrations(migrationFiles, "migrations")

	require.NoError(t, err)
	require.NotEmpty(t, got)
	assert.Equal(t, 1, got[0].Version)
	assert.Equal(t, "create_jobs", got[0].Name)
}

func TestMigrateAppliesOnce(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	embedded, err := loadMigrations(migrationFiles, "migrations")
	require.NoError(t, err)

	applied, err := Migrate(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, len(embedded), applied)

	applied, err = Migrate(ctx, pool)
	require.NoError(t, err)
	assert.Zero(t, applied, "second run must be a no-op")

	var versions []int
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	require.NoError(t, err)
	for rows.Next() {
		var v int
		require.NoError(t, rows.Scan(&v))
		versions = append(versions, v)
	}
	require.NoError(t, rows.Err())
	assert.Len(t, versions, len(embedded))

	var exists bool
	require.NoError(t, pool.QueryRow(ctx, "SELECT to_regclass('jobs') IS NOT NULL").Scan(&exists))
	assert.True(t, exists, "jobs table must exist")
}

func TestMigrateRollsBackFailedMigration(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	fsys := fstest.MapFS{
		"m/001_first.sql":  {Data: []byte("CREATE TABLE first (id INT);")},
		"m/002_broken.sql": {Data: []byte("CREATE TABLE second (id INT);\nTHIS IS NOT SQL;")},
	}

	applied, err := migrate(ctx, pool, fsys, "m")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "002_broken")
	assert.Equal(t, 1, applied)

	var first, second bool
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT to_regclass('first') IS NOT NULL, to_regclass('second') IS NOT NULL").Scan(&first, &second))
	assert.True(t, first)
	assert.False(t, second, "a failed migration must leave no partial changes")

	var count int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestMigrateConcurrentServers(t *testing.T) {
	pool := dbtest.New(t)
	ctx := t.Context()
	embedded, err := loadMigrations(migrationFiles, "migrations")
	require.NoError(t, err)

	const servers = 5
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
		errs  []error
	)
	for range servers {
		wg.Go(func() {
			n, err := Migrate(ctx, pool)
			mu.Lock()
			defer mu.Unlock()
			total += n
			if err != nil {
				errs = append(errs, err)
			}
		})
	}
	wg.Wait()

	assert.Empty(t, errs)
	assert.Equal(t, len(embedded), total, "each migration must be applied exactly once")
}
