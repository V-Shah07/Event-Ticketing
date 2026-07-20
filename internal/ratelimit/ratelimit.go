// Package ratelimit implements a Redis sliding-window rate limiter backed by a
// single atomic Lua script, plus HTTP middleware that keys on client IP and (when
// authenticated) user ID.
package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// slidingWindow is a ZSET-based sliding-window log. Each request is a member
// scored by its timestamp; the script trims expired members, counts the rest,
// and admits only if under the limit — all atomically inside Redis.
var slidingWindow = redis.NewScript(`
local key    = KEYS[1]
local now    = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit  = tonumber(ARGV[3])
local member = ARGV[4]

redis.call('ZREMRANGEBYSCORE', key, 0, now - window)
local count = redis.call('ZCARD', key)
if count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, window)
    return 1
end
return 0
`)

// Limiter admits at most `limit` events per `window` per key.
type Limiter struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
}

func New(rdb *redis.Client, limit int, window time.Duration) *Limiter {
	return &Limiter{rdb: rdb, limit: limit, window: window}
}

func (l *Limiter) Limit() int            { return l.limit }
func (l *Limiter) Window() time.Duration { return l.window }

// Allow reports whether an event for `key` is admitted right now.
func (l *Limiter) Allow(ctx context.Context, key string) (bool, error) {
	nowMs := time.Now().UnixMilli()
	member := hex.EncodeToString(randBytes(8))
	res, err := slidingWindow.Run(ctx, l.rdb,
		[]string{"ratelimit:" + key},
		nowMs, l.window.Milliseconds(), l.limit, member,
	).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
