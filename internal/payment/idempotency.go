package payment

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// idempotencyStore records processed Stripe payment-intent IDs in Redis so
// duplicate webhook deliveries (Stripe retries, or a burst of concurrent
// replays) are recognized before any side effect runs.
type idempotencyStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func newIdempotencyStore(rdb *redis.Client) *idempotencyStore {
	return &idempotencyStore{rdb: rdb, ttl: 24 * time.Hour}
}

func key(intentID string) string { return "idempotency:stripe:" + intentID }

// acquire atomically claims processing rights for intentID. It returns true to
// exactly one caller (the first); every concurrent or later caller gets false.
func (s *idempotencyStore) acquire(ctx context.Context, intentID string) (bool, error) {
	return s.rdb.SetNX(ctx, key(intentID), "processing", s.ttl).Result()
}

// markDone flags the key as fully processed (informational; the value moves
// from "processing" to "done").
func (s *idempotencyStore) markDone(ctx context.Context, intentID string) error {
	return s.rdb.Set(ctx, key(intentID), "done", s.ttl).Err()
}

// release removes the key so a failed attempt can be retried by Stripe.
func (s *idempotencyStore) release(ctx context.Context, intentID string) {
	_ = s.rdb.Del(ctx, key(intentID)).Err()
}
