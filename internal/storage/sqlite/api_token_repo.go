package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

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

	var lastUsedAt, expiresAt *string
	if token.LastUsedAt != nil && !token.LastUsedAt.IsZero() {
		t := token.LastUsedAt.Format(time.RFC3339)
		lastUsedAt = &t
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.IsZero() {
		t := token.ExpiresAt.Format(time.RFC3339)
		expiresAt = &t
	}

	query := `
		INSERT INTO api_tokens (id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := r.db.Conn.ExecContext(ctx, query,
		token.ID, token.Name, token.TokenHash, token.UserID, lastUsedAt, expiresAt, token.IsActive, token.CreatedAt.Format(time.RFC3339), token.UpdatedAt.Format(time.RFC3339),
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
		WHERE id = ?
	`

	var token model.APIToken
	var lastUsedAt, expiresAt, createdAt, updatedAt sql.NullString

	err := r.db.Conn.QueryRowContext(ctx, query, id).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &lastUsedAt, &expiresAt, &token.IsActive, &createdAt, &updatedAt,
	)
	if err != nil {
		return model.APIToken{}, mapError(err)
	}

	if lastUsedAt.Valid {
		token.LastUsedAt = parseTimePtrToken(lastUsedAt.String)
	}
	if expiresAt.Valid {
		token.ExpiresAt = parseTimePtrToken(expiresAt.String)
	}
	if createdAt.Valid {
		token.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	}
	if updatedAt.Valid {
		token.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt.String)
	}

	return token, nil
}

func (r *apiTokenRepo) GetByTokenHash(ctx context.Context, tokenHash string) (model.APIToken, error) {
	query := `
		SELECT id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
		FROM api_tokens
		WHERE token_hash = ?
	`

	var token model.APIToken
	var lastUsedAt, expiresAt, createdAt, updatedAt sql.NullString

	err := r.db.Conn.QueryRowContext(ctx, query, tokenHash).Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.UserID, &lastUsedAt, &expiresAt, &token.IsActive, &createdAt, &updatedAt,
	)
	if err != nil {
		return model.APIToken{}, mapError(err)
	}

	if lastUsedAt.Valid {
		token.LastUsedAt = parseTimePtrToken(lastUsedAt.String)
	}
	if expiresAt.Valid {
		token.ExpiresAt = parseTimePtrToken(expiresAt.String)
	}
	if createdAt.Valid {
		token.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	}
	if updatedAt.Valid {
		token.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt.String)
	}

	return token, nil
}

func (r *apiTokenRepo) ListByUser(ctx context.Context, userID string) ([]model.APIToken, error) {
	query := `
		SELECT id, name, token_hash, user_id, last_used_at, expires_at, is_active, created_at, updated_at
		FROM api_tokens
		WHERE user_id = ?
		ORDER BY created_at DESC
	`

	rows, err := r.db.Conn.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tokens := make([]model.APIToken, 0)
	for rows.Next() {
		var token model.APIToken
		var lastUsedAt, expiresAt, createdAt, updatedAt sql.NullString

		err := rows.Scan(
			&token.ID, &token.Name, &token.TokenHash, &token.UserID, &lastUsedAt, &expiresAt, &token.IsActive, &createdAt, &updatedAt,
		)
		if err != nil {
			return nil, err
		}

		if lastUsedAt.Valid {
			token.LastUsedAt = parseTimePtrToken(lastUsedAt.String)
		}
		if expiresAt.Valid {
			token.ExpiresAt = parseTimePtrToken(expiresAt.String)
		}
		if createdAt.Valid {
			token.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		}
		if updatedAt.Valid {
			token.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt.String)
		}

		tokens = append(tokens, token)
	}

	return tokens, rows.Err()
}

func (r *apiTokenRepo) Update(ctx context.Context, token model.APIToken) (model.APIToken, error) {
	token.UpdatedAt = time.Now()

	var lastUsedAt, expiresAt *string
	if token.LastUsedAt != nil && !token.LastUsedAt.IsZero() {
		t := token.LastUsedAt.Format(time.RFC3339)
		lastUsedAt = &t
	}
	if token.ExpiresAt != nil && !token.ExpiresAt.IsZero() {
		t := token.ExpiresAt.Format(time.RFC3339)
		expiresAt = &t
	}

	query := `
		UPDATE api_tokens
		SET name = ?, last_used_at = ?, expires_at = ?, is_active = ?, updated_at = ?
		WHERE id = ?
	`

	result, err := r.db.Conn.ExecContext(ctx, query,
		token.Name, lastUsedAt, expiresAt, token.IsActive, token.UpdatedAt.Format(time.RFC3339), token.ID,
	)
	if err != nil {
		return model.APIToken{}, err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return model.APIToken{}, mapError(sql.ErrNoRows)
	}

	return token, nil
}

func (r *apiTokenRepo) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM api_tokens WHERE id = ?`

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

func parseTimePtrToken(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
