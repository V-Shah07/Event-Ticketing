// Package db provides the Postgres connection pool and a minimal migration runner.
package db

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v-shah07/event-ticketing/migrations"
)

// Connect opens a pgx pool and waits for the database to become reachable.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.MaxConns = 20

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("new pool: %w", err)
	}

	// Wait for readiness (containers may still be starting).
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = pool.Ping(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("db not ready: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return pool, nil
}

// Migrate applies every embedded migration in lexical order, tracking applied
// versions in a schema_migrations table so it is safe to run repeatedly.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".sql" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err = pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err = pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err = pool.Exec(ctx,
			`INSERT INTO schema_migrations(version) VALUES($1)`, name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}
