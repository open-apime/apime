package idempotency

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage"
)

// Cleaner drops expired idempotency keys. Without it the table only grows, and
// these hosts run SQLite on a small disk.
type Cleaner struct {
	repo storage.IdempotencyRepository
	log  *zap.Logger
}

func NewCleaner(repo storage.IdempotencyRepository, log *zap.Logger) *Cleaner {
	return &Cleaner{repo: repo, log: log}
}

func (c *Cleaner) Start(ctx context.Context, interval time.Duration) {
	if c.repo == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.sweep(ctx)
			}
		}
	}()
}

func (c *Cleaner) sweep(ctx context.Context) {
	removed, err := c.repo.DeleteExpired(ctx, time.Now())
	if err != nil {
		c.log.Warn("erro ao limpar chaves de idempotência", zap.Error(err))
		return
	}
	if removed > 0 {
		c.log.Info("chaves de idempotência expiradas removidas", zap.Int64("count", removed))
	}
}
