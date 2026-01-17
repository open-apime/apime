package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-apime/apime/internal/storage/model"
)

type instanceRepo struct {
	db *DB
}

func NewInstanceRepository(db *DB) *instanceRepo {
	return &instanceRepo{db: db}
}

func (r *instanceRepo) Create(ctx context.Context, inst model.Instance) (model.Instance, error) {
	if inst.ID == "" {
		inst.ID = uuid.New().String()
	}
	now := time.Now()
	inst.CreatedAt = now
	inst.UpdatedAt = now

	query := `
		INSERT INTO instances (id, name, owner_user_id, status, session_blob, webhook_url, webhook_secret, instance_token_hash, instance_token_updated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, name, owner_user_id, status, COALESCE(webhook_url, ''), COALESCE(webhook_secret, ''), COALESCE(instance_token_hash, ''), instance_token_updated_at, created_at, updated_at
	`

	err := r.db.Pool.QueryRow(ctx, query,
		inst.ID, inst.Name, inst.OwnerUserID, string(inst.Status), inst.SessionBlob, nullIfEmpty(inst.WebhookURL), nullIfEmpty(inst.WebhookSecret), nullIfEmpty(inst.TokenHash), inst.TokenUpdatedAt, inst.CreatedAt, inst.UpdatedAt,
	).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
	)

	if err != nil {
		return model.Instance{}, err
	}

	return inst, nil
}

func (r *instanceRepo) GetByTokenHash(ctx context.Context, tokenHash string) (model.Instance, error) {
	query := `
		SELECT id, name, owner_user_id, status, session_blob, COALESCE(webhook_url, ''), COALESCE(webhook_secret, ''), COALESCE(instance_token_hash, ''), instance_token_updated_at, created_at, updated_at
		FROM instances
		WHERE instance_token_hash = $1
	`

	var inst model.Instance
	err := r.db.Pool.QueryRow(ctx, query, tokenHash).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.SessionBlob, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return model.Instance{}, ErrNotFound
	}
	if err != nil {
		return model.Instance{}, err
	}
	return inst, nil
}

func (r *instanceRepo) GetByID(ctx context.Context, id string) (model.Instance, error) {
	query := `
		SELECT id, name, owner_user_id, status, session_blob, COALESCE(webhook_url, ''), COALESCE(webhook_secret, ''), COALESCE(instance_token_hash, ''), instance_token_updated_at, created_at, updated_at
		FROM instances
		WHERE id = $1
	`

	var inst model.Instance

	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.SessionBlob, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.Instance{}, ErrNotFound
	}
	if err != nil {
		return model.Instance{}, err
	}

	return inst, nil
}

func (r *instanceRepo) List(ctx context.Context) ([]model.Instance, error) {
	query := `
		SELECT i.id, i.name, i.owner_user_id, COALESCE(u.email, ''), i.status, COALESCE(i.webhook_url, ''), COALESCE(i.webhook_secret, ''), COALESCE(i.instance_token_hash, ''), i.instance_token_updated_at, i.created_at, i.updated_at
		FROM instances i
		LEFT JOIN users u ON i.owner_user_id = u.id
		ORDER BY i.created_at DESC
	`

	rows, err := r.db.Pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []model.Instance
	for rows.Next() {
		var inst model.Instance
		if err := rows.Scan(
			&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.OwnerEmail, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
		); err != nil {
			return nil, err
		}

		instances = append(instances, inst)
	}

	return instances, rows.Err()
}

func (r *instanceRepo) ListByOwner(ctx context.Context, ownerUserID string) ([]model.Instance, error) {
	query := `
		SELECT i.id, i.name, i.owner_user_id, COALESCE(u.email, ''), i.status, COALESCE(i.webhook_url, ''), COALESCE(i.webhook_secret, ''), COALESCE(i.instance_token_hash, ''), i.instance_token_updated_at, i.created_at, i.updated_at
		FROM instances i
		LEFT JOIN users u ON i.owner_user_id = u.id
		WHERE i.owner_user_id = $1
		ORDER BY i.created_at DESC
	`

	rows, err := r.db.Pool.Query(ctx, query, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []model.Instance
	for rows.Next() {
		var inst model.Instance
		if err := rows.Scan(
			&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.OwnerEmail, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
		); err != nil {
			return nil, err
		}

		instances = append(instances, inst)
	}

	return instances, rows.Err()
}

func (r *instanceRepo) Update(ctx context.Context, inst model.Instance) (model.Instance, error) {
	inst.UpdatedAt = time.Now()

	query := `
		UPDATE instances
		SET name = $2, owner_user_id = $3, status = $4, session_blob = $5, webhook_url = $6, webhook_secret = $7, instance_token_hash = $8, instance_token_updated_at = $9, updated_at = $10
		WHERE id = $1
		RETURNING id, name, owner_user_id, status, COALESCE(webhook_url, ''), COALESCE(webhook_secret, ''), COALESCE(instance_token_hash, ''), instance_token_updated_at, created_at, updated_at
	`

	err := r.db.Pool.QueryRow(ctx, query,
		inst.ID, inst.Name, inst.OwnerUserID, string(inst.Status), inst.SessionBlob, nullIfEmpty(inst.WebhookURL), nullIfEmpty(inst.WebhookSecret), nullIfEmpty(inst.TokenHash), inst.TokenUpdatedAt, inst.UpdatedAt,
	).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &inst.TokenUpdatedAt, &inst.CreatedAt, &inst.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.Instance{}, ErrNotFound
	}
	if err != nil {
		return model.Instance{}, err
	}

	return inst, nil
}

func nullIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func (r *instanceRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM instances WHERE id = $1`
	result, err := r.db.Pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
