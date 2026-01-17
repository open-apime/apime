package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage/model"
)

type deviceConfigRepo struct {
	db *DB
}

func NewDeviceConfigRepository(db *DB) *deviceConfigRepo {
	return &deviceConfigRepo{db: db}
}

func (r *deviceConfigRepo) Get(ctx context.Context) (model.DeviceConfig, error) {
	query := `
		SELECT id, platform_type, os_name, created_at, updated_at
		FROM device_config
		ORDER BY created_at ASC
		LIMIT 1
	`

	var config model.DeviceConfig
	var createdAt, updatedAt string

	err := r.db.Conn.QueryRowContext(ctx, query).Scan(
		&config.ID, &config.PlatformType,
		&config.OSName, &createdAt, &updatedAt,
	)
	if err == sql.ErrNoRows {
		return model.DeviceConfig{
			ID:           uuid.NewString(),
			PlatformType: "DESKTOP",
			OSName:       "ApiMe",
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}, nil
	}
	if err != nil {
		return model.DeviceConfig{}, err
	}

	config.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	config.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	return config, nil
}

func (r *deviceConfigRepo) Update(ctx context.Context, config model.DeviceConfig) (model.DeviceConfig, error) {
	config.UpdatedAt = time.Now()

	if config.ID == "" {
		config.ID = "00000000-0000-0000-0000-000000000001"
	}
	if config.CreatedAt.IsZero() {
		config.CreatedAt = time.Now()
	}

	query := `
		INSERT OR REPLACE INTO device_config (id, platform_type, os_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := r.db.Conn.ExecContext(ctx, query,
		config.ID, config.PlatformType,
		config.OSName, config.CreatedAt.Format(time.RFC3339), config.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return model.DeviceConfig{}, err
	}

	return config, nil
}
