package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-apime/apime/internal/storage/model"
)

type apiTokenRepo struct {
	db *DB
}

func NewAPITokenRepository(db *DB) *apiTokenRepo {
	return &apiTokenRepo{db: db}
}

func (r *apiTokenRepo) Create(ctx context.Context, token model.APIToken) (model.APIToken, error) {
	if token.ID == "" {
		token.ID = uuid.New().String()
	}
	now := time.Now()
	token.CreatedAt = now
	token.UpdatedAt = now

	query := `
		INSERT INTO api_tokens (id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
	`

	err := r.db.Pool.QueryRow(ctx, query,
		token.ID, token.Name, token.TokenHash, token.UserID, token.LastUsedAt, token.ExpiresAt, token.IsActive, token.CreatedAt, token.UpdatedAt,
	).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &token.LastUsedAt, &token.ExpiresAt, &token.IsActive, &token.CreatedAt, &token.UpdatedAt,
	)

	if err != nil {
		return model.APIToken{}, err
	}

	return token, nil
}

func (r *apiTokenRepo) GetByID(ctx context.Context, id string) (model.APIToken, error) {
	query := `
		SELECT id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
		FROM api_tokens
		WHERE id = $1
	`

	var token model.APIToken
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &token.LastUsedAt, &token.ExpiresAt, &token.IsActive, &token.CreatedAt, &token.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.APIToken{}, ErrNotFound
	}
	if err != nil {
		return model.APIToken{}, err
	}

	return token, nil
}

func (r *apiTokenRepo) GetByTokenHash(ctx context.Context, tokenHash string) (model.APIToken, error) {
	query := `
		SELECT id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
		FROM api_tokens
		WHERE token_hash = $1
	`

	var token model.APIToken
	err := r.db.Pool.QueryRow(ctx, query, tokenHash).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &token.LastUsedAt, &token.ExpiresAt, &token.IsActive, &token.CreatedAt, &token.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.APIToken{}, ErrNotFound
	}
	if err != nil {
		return model.APIToken{}, err
	}

	return token, nil
}

func (r *apiTokenRepo) ListByUser(ctx context.Context, userID string) ([]model.APIToken, error) {
	query := `
		SELECT id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
		FROM api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC
	`

	rows, err := r.db.Pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := make([]model.APIToken, 0)
	for rows.Next() {
		var token model.APIToken
		err := rows.Scan(
			&token.ID, &token.Name, &token.TokenHash, &token.UserID, &token.LastUsedAt, &token.ExpiresAt, &token.IsActive, &token.CreatedAt, &token.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

func (r *apiTokenRepo) Update(ctx context.Context, token model.APIToken) (model.APIToken, error) {
	token.UpdatedAt = time.Now()

	query := `
		UPDATE api_tokens
		SET name = $2, last_used_at = $3, expires_at = $4, is_active = $5, updated_at = $6
		WHERE id = $1
		RETURNING id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
	`

	err := r.db.Pool.QueryRow(ctx, query,
		token.ID, token.Name, token.LastUsedAt, token.ExpiresAt, token.IsActive, token.UpdatedAt,
	).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &token.LastUsedAt, &token.ExpiresAt, &token.IsActive, &token.CreatedAt, &token.UpdatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.APIToken{}, ErrNotFound
	}
	if err != nil {
		return model.APIToken{}, err
	}

	return token, nil
}

func (r *apiTokenRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM api_tokens WHERE id = $1`

	result, err := r.db.Pool.Exec(ctx, query, id)
	if err != nil {
		return err
	}

	if result.RowsAffected() == 0 {
		return ErrNotFound
	}

	return nil
}
