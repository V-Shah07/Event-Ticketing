// Package testsupport provides shared setup for integration tests: a migrated
// Postgres pool and a flushed Redis client, both driven by the standard env
// vars. Tests skip cleanly when the backing services are unreachable.
package testsupport

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/v-shah07/event-ticketing/internal/cache"
	"github.com/v-shah07/event-ticketing/internal/config"
	"github.com/v-shah07/event-ticketing/internal/db"
)

// Pool returns a migrated pool, skipping the test if Postgres is unreachable.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg := config.Load()
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		t.Skipf("postgres unavailable (%v); skipping integration test", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// Redis returns a flushed Redis client, skipping the test if unreachable.
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	cfg := config.Load()
	rdb, err := cache.New(context.Background(), cfg.RedisAddr)
	if err != nil {
		t.Skipf("redis unavailable (%v); skipping integration test", err)
	}
	if err := rdb.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush redis: %v", err)
	}
	return rdb
}

// Truncate clears all mutable tables so each test starts clean.
func Truncate(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`TRUNCATE tickets, purchases, ticket_tiers, events, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
