package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/config"
)

type Client struct {
	rdb *redis.Client
	log *zap.Logger
}

func New(cfg config.RedisConfig, log *zap.Logger) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis: falha ao conectar: %w", err)
	}

	log.Info("redis: conectado com sucesso", zap.String("addr", cfg.Addr))

	return &Client{rdb: rdb, log: log}, nil
}

func (c *Client) Close() error {
	return c.rdb.Close()
}

func (c *Client) RDB() *redis.Client {
	return c.rdb
}
