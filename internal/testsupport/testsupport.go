// Package testsupport provides shared setup for integration tests: a migrated
// Postgres pool and a flushed Redis client, both driven by the standard env
// vars. Tests skip cleanly when the backing services are unreachable.
package testsupport

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

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

// Redis returns a Redis client, skipping the test if unreachable. It does not
// flush the DB: tests use unique keys/IDs so they can run concurrently across
// packages against a shared Redis without stepping on each other.
func Redis(t *testing.T) *redis.Client {
	t.Helper()
	cfg := config.Load()
	rdb, err := cache.New(context.Background(), cfg.RedisAddr)
	if err != nil {
		t.Skipf("redis unavailable (%v); skipping integration test", err)
	}
	return rdb
}

// UniqueEmail returns a collision-free email for test registrations so suites
// are safe to re-run against a persistent database.
func UniqueEmail(prefix string) string {
	return fmt.Sprintf("%s-%d-%d@example.com", prefix, time.Now().UnixNano(), atomic.AddInt64(&emailCounter, 1))
}

var emailCounter int64

// KafkaBrokers returns the configured brokers, skipping the test if the broker
// is unreachable.
func KafkaBrokers(t *testing.T) []string {
	t.Helper()
	addr := os.Getenv("KAFKA_BROKERS")
	if addr == "" {
		addr = "localhost:9092"
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Skipf("kafka unavailable at %s (%v); skipping", addr, err)
	}
	conn.Close()
	return []string{addr}
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
