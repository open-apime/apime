package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := r.db.Conn.ExecContext(ctx, query,
		inst.ID, inst.Name, inst.OwnerUserID, string(inst.Status), inst.SessionBlob,
		nullIfEmpty(inst.WebhookURL), nullIfEmpty(inst.WebhookSecret), nullIfEmpty(inst.TokenHash),
		inst.TokenUpdatedAt, inst.CreatedAt.Format(time.RFC3339), inst.UpdatedAt.Format(time.RFC3339),
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
		WHERE instance_token_hash = ?
	`

	var inst model.Instance
	var createdAt, updatedAt string
	var tokenUpdatedAt sql.NullString

	err := r.db.Conn.QueryRowContext(ctx, query, tokenHash).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.SessionBlob, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &tokenUpdatedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return model.Instance{}, mapError(err)
	}

	inst.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	inst.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if tokenUpdatedAt.Valid {
		inst.TokenUpdatedAt = parseTimePtr(tokenUpdatedAt.String)
	}

	return inst, nil
}

func (r *instanceRepo) GetByID(ctx context.Context, id string) (model.Instance, error) {
	query := `
		SELECT id, name, owner_user_id, status, session_blob, COALESCE(webhook_url, ''), COALESCE(webhook_secret, ''), COALESCE(instance_token_hash, ''), instance_token_updated_at, created_at, updated_at
		FROM instances
		WHERE id = ?
	`

	var inst model.Instance
	var createdAt, updatedAt string
	var tokenUpdatedAt sql.NullString

	err := r.db.Conn.QueryRowContext(ctx, query, id).Scan(
		&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.Status, &inst.SessionBlob, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &tokenUpdatedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return model.Instance{}, mapError(err)
	}

	inst.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	inst.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
	if tokenUpdatedAt.Valid {
		inst.TokenUpdatedAt = parseTimePtr(tokenUpdatedAt.String)
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

	rows, err := r.db.Conn.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []model.Instance
	for rows.Next() {
		var inst model.Instance
		var createdAt, updatedAt string
		var tokenUpdatedAt sql.NullString

		if err := rows.Scan(
			&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.OwnerEmail, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &tokenUpdatedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}

		inst.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		inst.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if tokenUpdatedAt.Valid {
			inst.TokenUpdatedAt = parseTimePtr(tokenUpdatedAt.String)
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
		WHERE i.owner_user_id = ?
		ORDER BY i.created_at DESC
	`

	rows, err := r.db.Conn.QueryContext(ctx, query, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []model.Instance
	for rows.Next() {
		var inst model.Instance
		var createdAt, updatedAt string
		var tokenUpdatedAt sql.NullString

		if err := rows.Scan(
			&inst.ID, &inst.Name, &inst.OwnerUserID, &inst.OwnerEmail, &inst.Status, &inst.WebhookURL, &inst.WebhookSecret, &inst.TokenHash, &tokenUpdatedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}

		inst.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		inst.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		if tokenUpdatedAt.Valid {
			inst.TokenUpdatedAt = parseTimePtr(tokenUpdatedAt.String)
		}

		instances = append(instances, inst)
	}

	return instances, rows.Err()
}

func (r *instanceRepo) Update(ctx context.Context, inst model.Instance) (model.Instance, error) {
	inst.UpdatedAt = time.Now()

	query := `
		UPDATE instances
		SET name = ?, owner_user_id = ?, status = ?, session_blob = ?, webhook_url = ?, webhook_secret = ?, instance_token_hash = ?, instance_token_updated_at = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := r.db.Conn.ExecContext(ctx, query,
		inst.Name, inst.OwnerUserID, string(inst.Status), inst.SessionBlob,
		nullIfEmpty(inst.WebhookURL), nullIfEmpty(inst.WebhookSecret), nullIfEmpty(inst.TokenHash),
		formatTimePtr(inst.TokenUpdatedAt), inst.UpdatedAt.Format(time.RFC3339), inst.ID,
	)
	if err != nil {
		return model.Instance{}, err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return model.Instance{}, mapError(sql.ErrNoRows)
	}

	return inst, nil
}

func nullIfEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func parseTimePtr(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

func (r *instanceRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM instances WHERE id = ?`
	result, err := r.db.Conn.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return mapError(sql.ErrNoRows)
	}
	return nil
}
