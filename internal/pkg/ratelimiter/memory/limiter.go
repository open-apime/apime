package memory

import (
	"context"
	"sync"
	"time"

	"github.com/open-apime/apime/internal/pkg/ratelimiter"
)

type item struct {
	count     int
	expiresAt time.Time
}

type MemoryLimiter struct {
	mu    sync.Mutex
	items map[string]*item
}

func NewLimiter() *MemoryLimiter {
	l := &MemoryLimiter{
		items: make(map[string]*item),
	}
	// Start cleanup routine
	go l.cleanupLoop()
	return l
}

func (l *MemoryLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (*ratelimiter.Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	val, exists := l.items[key]

	if !exists || now.After(val.expiresAt) {
		// New window or expired
		l.items[key] = &item{
			count:     1,
			expiresAt: now.Add(window),
		}
		return &ratelimiter.Result{
			Allowed:   true,
			Remaining: limit - 1,
			Reset:     now.Add(window),
		}, nil
	}

	// Existing active window
	val.count++
	remaining := limit - val.count
	if remaining < 0 {
		remaining = 0
	}

	res := &ratelimiter.Result{
		Allowed:    val.count <= limit,
		Remaining:  remaining,
		Reset:      val.expiresAt,
		RetryAfter: val.expiresAt.Sub(now),
	}

	return res, nil
}

func (l *MemoryLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for k, v := range l.items {
			if now.After(v.expiresAt) {
				delete(l.items, k)
			}
		}
		l.mu.Unlock()
	}
}
