package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/open-apime/apime/internal/pkg/ratelimiter"
	"github.com/redis/go-redis/v9"
)

var rateLimitScript = redis.NewScript(`
local current = redis.call("INCR", KEYS[1])
if current == 1 then
    redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
local ttl = redis.call("PTTL", KEYS[1])
return {current, ttl}
`)

type RedisLimiter struct {
	client *redis.Client
}

func NewLimiter(client *redis.Client) *RedisLimiter {
	return &RedisLimiter{
		client: client,
	}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (*ratelimiter.Result, error) {
	windowMs := window.Milliseconds()

	vals, err := rateLimitScript.Run(ctx, l.client, []string{key}, windowMs).Int64Slice()
	if err != nil {
		return nil, fmt.Errorf("redis limiter: %w", err)
	}

	if len(vals) < 2 {
		return nil, fmt.Errorf("redis limiter: invalid response")
	}

	current := vals[0]
	ttlMs := vals[1]

	remaining := limit - int(current)
	if remaining < 0 {
		remaining = 0
	}

	// Calculate reset time
	resetAfter := time.Duration(ttlMs) * time.Millisecond
	if ttlMs < 0 {
		resetAfter = window // Fallback
	}

	reset := time.Now().Add(resetAfter)

	res := &ratelimiter.Result{
		Allowed:    current <= int64(limit),
		Remaining:  remaining,
		Reset:      reset,
		RetryAfter: resetAfter,
	}

	return res, nil
}
