// Package inventory exposes the remaining-capacity read path with a Redis cache
// in front of Postgres. Writes (ticket sales) happen in the payment package
// under a SELECT ... FOR UPDATE row lock; this package only caches reads and is
// invalidated on every sale so the cache never reflects uncommitted inventory.
package inventory

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// ErrTierNotFound is returned when the tier does not exist.
var ErrTierNotFound = errors.New("tier not found")

func cacheKey(tierID string) string { return "inventory:tier:" + tierID }

type Cache struct {
	pool *pgxpool.Pool
	rdb  *redis.Client
	ttl  time.Duration
}

func NewCache(pool *pgxpool.Pool, rdb *redis.Client) *Cache {
	return &Cache{pool: pool, rdb: rdb, ttl: 30 * time.Second}
}

// Remaining returns the number of tickets still available for a tier. It serves
// from Redis when warm and falls back to Postgres on a miss, repopulating the
// cache. The bool reports whether the value came from cache (used in tests).
func (c *Cache) Remaining(ctx context.Context, tierID string) (int, bool, error) {
	if v, err := c.rdb.Get(ctx, cacheKey(tierID)).Result(); err == nil {
		if n, perr := strconv.Atoi(v); perr == nil {
			return n, true, nil
		}
	}

	var capacity, sold int
	err := c.pool.QueryRow(ctx,
		`SELECT capacity, sold FROM ticket_tiers WHERE id=$1`, tierID,
	).Scan(&capacity, &sold)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrTierNotFound
	}
	if err != nil {
		return 0, false, err
	}
	remaining := capacity - sold
	if remaining < 0 {
		remaining = 0
	}
	// Best-effort cache fill; a failure here just means the next read hits the DB.
	_ = c.rdb.Set(ctx, cacheKey(tierID), remaining, c.ttl).Err()
	return remaining, false, nil
}

// Invalidate drops the cached count. Call it inside/after any transaction that
// changes a tier's sold count so stale inventory is never served.
func (c *Cache) Invalidate(ctx context.Context, tierID string) {
	_ = c.rdb.Del(ctx, cacheKey(tierID)).Err()
}
