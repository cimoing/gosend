package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Config struct {
	Driver          string
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func Open(ctx context.Context, cfg Config) (Store, error) {
	driver, err := NormalizeDriver(cfg.Driver)
	if err != nil {
		return nil, err
	}
	if driver == "memory" {
		return NewMemory(), nil
	}
	if strings.TrimSpace(cfg.DSN) == "" {
		return nil, fmt.Errorf("database DSN is required for %s", driver)
	}

	driverName := driver
	if driver == "postgres" {
		driverName = "pgx"
	}
	database, err := sql.Open(driverName, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", driver, err)
	}

	configurePool(database, driver, cfg)
	pingContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := database.PingContext(pingContext); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to %s database: %w", driver, err)
	}
	if driver == "sqlite" {
		for _, pragma := range []string{
			"PRAGMA foreign_keys = ON",
			"PRAGMA busy_timeout = 5000",
			"PRAGMA journal_mode = WAL",
		} {
			if _, err := database.ExecContext(pingContext, pragma); err != nil {
				_ = database.Close()
				return nil, fmt.Errorf("configure SQLite: %w", err)
			}
		}
	}

	sqlStore := &SQL{database: database, dialect: driver}
	if err := sqlStore.migrate(pingContext); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("migrate %s database: %w", driver, err)
	}
	return sqlStore, nil
}

func NormalizeDriver(driver string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "memory", "mem":
		return "memory", nil
	case "sqlite", "sqlite3":
		return "sqlite", nil
	case "mysql", "mariadb":
		return "mysql", nil
	case "postgres", "postgresql", "pgsql", "pg":
		return "postgres", nil
	default:
		return "", errors.New("database driver must be one of memory, sqlite, mysql, or postgres")
	}
}

func configurePool(database *sql.DB, driver string, cfg Config) {
	maxOpen := cfg.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 10
	}
	maxIdle := cfg.MaxIdleConns
	if maxIdle < 0 {
		maxIdle = 0
	} else if maxIdle == 0 {
		maxIdle = 5
	}
	lifetime := cfg.ConnMaxLifetime
	if lifetime <= 0 {
		lifetime = 3 * time.Minute
	}

	if driver == "sqlite" {
		maxOpen = 1
		maxIdle = 1
		lifetime = 0
	}
	database.SetMaxOpenConns(maxOpen)
	database.SetMaxIdleConns(maxIdle)
	database.SetConnMaxLifetime(lifetime)
}
