package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID serializes migrations across servers sharing a database.
const migrationLockID int64 = 0x6369736964 // "cisid"

// ErrInvalidMigration is returned for a migration file that is misnamed or
// duplicates another version.
var ErrInvalidMigration = errors.New("invalid migration")

// migration is one SQL file named <version>_<name>.sql.
type migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrate applies the embedded migrations that have not run yet, each in its
// own transaction, and returns how many it applied. Concurrent callers on the
// same database are serialized with an advisory lock.
func Migrate(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	return migrate(ctx, pool, migrationFiles, "migrations")
}

func migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir string) (int, error) {
	migrations, err := loadMigrations(fsys, dir)
	if err != nil {
		return 0, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection for migrations: %w", err)
	}
	defer conn.Release()

	// Session-level lock: held across the per-migration transactions.
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return 0, fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	const createTable = `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INT         PRIMARY KEY,
			name       TEXT        NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return 0, fmt.Errorf("read applied migrations: %w", err)
	}
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read applied migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("read applied migrations: %w", err)
	}

	n := 0
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %03d_%s: %w", m.Version, m.Name, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return fmt.Errorf("apply migration %03d_%s: %w", m.Version, m.Name, err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name); err != nil {
		return fmt.Errorf("record migration %03d_%s: %w", m.Version, m.Name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %03d_%s: %w", m.Version, m.Name, err)
	}
	return nil
}

// loadMigrations reads the *.sql files in dir, sorted by version.
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var migrations []migration
	seen := map[int]string{}
	for _, e := range entries {
		file := e.Name()
		base, ok := strings.CutSuffix(file, ".sql")
		if e.IsDir() || !ok {
			continue
		}

		prefix, name, _ := strings.Cut(base, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 || name == "" {
			return nil, fmt.Errorf("%w: %s: want <version>_<name>.sql", ErrInvalidMigration, file)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("%w: %s and %s share version %d", ErrInvalidMigration, other, file, version)
		}
		seen[version] = file

		sql, err := fs.ReadFile(fsys, path.Join(dir, file))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", file, err)
		}
		migrations = append(migrations, migration{Version: version, Name: name, SQL: string(sql)})
	}

	slices.SortFunc(migrations, func(a, b migration) int { return a.Version - b.Version })
	return migrations, nil
}
