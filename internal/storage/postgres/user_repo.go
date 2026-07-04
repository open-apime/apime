package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-apime/apime/internal/storage/model"
)

type userRepo struct {
	db *DB
}

func NewUserRepository(db *DB) *userRepo {
	return &userRepo{db: db}
}

func (r *userRepo) Create(ctx context.Context, user model.User) (model.User, error) {
	if user.ID == "" {
		user.ID = uuid.New().String()
	}
	user.CreatedAt = time.Now()

	query := `
		INSERT INTO users (id, email, password_hash, role, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, email, password_hash, role, created_at
	`

	err := r.db.Pool.QueryRow(ctx, query,
		user.ID, user.Email, user.PasswordHash, user.Role, user.CreatedAt,
	).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt,
	)

	if err != nil {
		return model.User{}, err
	}

	return user, nil
}

func (r *userRepo) GetByID(ctx context.Context, id string) (model.User, error) {
	query := `
		SELECT id, email, password_hash, role, created_at
		FROM users
		WHERE id = $1
	`

	var user model.User
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}

	return user, nil
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	query := `
		SELECT id, email, password_hash, role, created_at
		FROM users
		WHERE email = $1
	`

	var user model.User
	err := r.db.Pool.QueryRow(ctx, query, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt,
	)

	if err == pgx.ErrNoRows {
		return model.User{}, ErrNotFound
	}
	if err != nil {
		return model.User{}, err
	}

	return user, nil
}

func (r *userRepo) List(ctx context.Context, searchQuery string, limit, offset int) ([]model.User, int, error) {
	whereClause := ""
	args := []any{}
	if searchQuery != "" {
		whereClause = " WHERE email ILIKE $1 "
		pattern := "%" + searchQuery + "%"
		args = append(args, pattern)
	}

	var total int
	countQuery := "SELECT COUNT(*) FROM users" + whereClause
	if err := r.db.Pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT id, email, password_hash, role, created_at
		FROM users
	` + whereClause + " ORDER BY created_at DESC"

	if limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2)
		args = append(args, limit, offset)
	}

	rows, err := r.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var user model.User
		if err := rows.Scan(
			&user.ID, &user.Email, &user.PasswordHash, &user.Role, &user.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		users = append(users, user)
	}

	return users, total, rows.Err()
}

func (r *userRepo) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	cmd, err := r.db.Pool.Exec(ctx, `
		UPDATE users
		SET password_hash = $2
		WHERE id = $1
	`, id, passwordHash)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *userRepo) Delete(ctx context.Context, id string) error {
	// Prevent deleting the last admin so the system always has at least one.
	var role string
	err := r.db.Pool.QueryRow(ctx, `SELECT role FROM users WHERE id = $1`, id).Scan(&role)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}

	if role == "admin" {
		var adminCount int
		if err := r.db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrLastAdmin
		}
	}

	cmd, err := r.db.Pool.Exec(ctx, `
		DELETE FROM users
		WHERE id = $1
	`, id)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
