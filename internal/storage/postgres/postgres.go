package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/config"
)

type DB struct {
	Pool *pgxpool.Pool
	log  *zap.Logger
}

func New(cfg config.DatabaseConfig, log *zap.Logger) (*DB, error) {
	dsn := cfg.DSN()

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: falha ao conectar: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("postgres: falha ao ping: %w", err)
	}

	log.Info("postgres: conectado com sucesso",
		zap.String("host", cfg.Host),
		zap.Int("port", cfg.Port),
		zap.String("database", cfg.Name),
		zap.String("user", cfg.User),
	)

	return &DB{Pool: pool, log: log}, nil
}

func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
}
