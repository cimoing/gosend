package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const migrationSeparator = "-- gosend:split"

//go:embed migrations/*/*.sql
var migrationFiles embed.FS

func (store *SQL) migrate(ctx context.Context) error {
	createMigrationTable := `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		applied_at VARCHAR(35) NOT NULL
	)`
	if _, err := store.database.ExecContext(ctx, createMigrationTable); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	applied, err := store.appliedMigrations(ctx)
	if err != nil {
		return err
	}
	pattern := filepath.ToSlash(filepath.Join("migrations", store.dialect, "*.sql"))
	names, err := fs.Glob(migrationFiles, pattern)
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if applied[version] {
			continue
		}
		content, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := store.applyMigration(ctx, version, string(content)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func (store *SQL) appliedMigrations(ctx context.Context) (map[int64]bool, error) {
	rows, err := store.database.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("list applied migrations: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan migration version: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func (store *SQL) applyMigration(ctx context.Context, version int64, content string) error {
	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()

	for _, statement := range strings.Split(content, migrationSeparator) {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		store.query("INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)"),
		version,
		formatTime(time.Now().UTC()),
	); err != nil {
		return err
	}
	return transaction.Commit()
}

func migrationVersion(name string) (int64, error) {
	base := filepath.Base(name)
	prefix, _, found := strings.Cut(base, "_")
	if !found {
		return 0, fmt.Errorf("migration file %q has no version prefix", name)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse migration version in %q: %w", name, err)
	}
	return version, nil
}
