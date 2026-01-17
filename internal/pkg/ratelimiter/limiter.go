package ratelimiter

import (
	"context"
	"time"
)

type Result struct {
	Allowed    bool
	Remaining  int
	Reset      time.Time
	RetryAfter time.Duration
}

type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (*Result, error)
}
