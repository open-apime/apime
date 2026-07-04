package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage/model"
)

type userRepo struct {
	db *DB
}

// NewUserRepository creates a new user repository.
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
		VALUES (?, ?, ?, ?, ?)
	`

	_, err := r.db.Conn.ExecContext(ctx, query,
		user.ID, user.Email, user.PasswordHash, user.Role, user.CreatedAt.Format(time.RFC3339),
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
		WHERE id = ?
	`

	var user model.User
	var createdAt string

	err := r.db.Conn.QueryRowContext(ctx, query, id).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &createdAt,
	)
	if err != nil {
		return model.User{}, mapError(err)
	}

	user.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return user, nil
}

func (r *userRepo) GetByEmail(ctx context.Context, email string) (model.User, error) {
	query := `
		SELECT id, email, password_hash, role, created_at
		FROM users
		WHERE email = ?
	`

	var user model.User
	var createdAt string

	err := r.db.Conn.QueryRowContext(ctx, query, email).Scan(
		&user.ID, &user.Email, &user.PasswordHash, &user.Role, &createdAt,
	)
	if err != nil {
		return model.User{}, mapError(err)
	}

	user.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return user, nil
}

func (r *userRepo) List(ctx context.Context, searchQuery string, limit, offset int) ([]model.User, int, error) {
	whereClause := ""
	args := []any{}
	if searchQuery != "" {
		whereClause = " WHERE email LIKE ? "
		pattern := "%" + searchQuery + "%"
		args = append(args, pattern)
	}

	var total int
	countQuery := "SELECT COUNT(*) FROM users" + whereClause
	if err := r.db.Conn.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := `
		SELECT id, email, password_hash, role, created_at
		FROM users
	` + whereClause + " ORDER BY created_at DESC"

	if limit > 0 {
		query += " LIMIT ? OFFSET ?"
		args = append(args, limit, offset)
	}

	rows, err := r.db.Conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var users []model.User
	for rows.Next() {
		var user model.User
		var createdAt string

		if err := rows.Scan(
			&user.ID, &user.Email, &user.PasswordHash, &user.Role, &createdAt,
		); err != nil {
			return nil, 0, err
		}

		user.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		users = append(users, user)
	}

	return users, total, rows.Err()
}

func (r *userRepo) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	result, err := r.db.Conn.ExecContext(ctx, `
		UPDATE users
		SET password_hash = ?
		WHERE id = ?
	`, passwordHash, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return mapError(sql.ErrNoRows)
	}
	return nil
}

func (r *userRepo) Delete(ctx context.Context, id string) error {
	// Prevent deleting the last admin, so the system always has one.
	var role string
	err := r.db.Conn.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, id).Scan(&role)
	if err != nil {
		return mapError(err)
	}

	if role == "admin" {
		var adminCount int
		if err := r.db.Conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&adminCount); err != nil {
			return err
		}
		if adminCount <= 1 {
			return ErrLastAdmin
		}
	}

	result, err := r.db.Conn.ExecContext(ctx, `
		DELETE FROM users
		WHERE id = ?
	`, id)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return mapError(sql.ErrNoRows)
	}
	return nil
}
