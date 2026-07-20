// Package cache wraps the Redis client construction used across the app.
package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// New returns a Redis client and verifies connectivity.
func New(ctx context.Context, addr string) (*redis.Client, error) {
	c := redis.NewClient(&redis.Options{Addr: addr})
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		return nil, err
	}
	return c, nil
}
