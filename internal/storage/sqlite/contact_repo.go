package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/open-apime/apime/internal/storage/model"
)

type contactRepo struct {
	db *DB
}

func NewContactRepository(db *DB) *contactRepo {
	return &contactRepo{db: db}
}

func (r *contactRepo) Upsert(ctx context.Context, contact model.Contact) error {
	now := time.Now()
	query := `
		INSERT INTO contacts (phone, jid, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(phone) DO UPDATE SET
			jid = excluded.jid,
			updated_at = excluded.updated_at
	`
	_, err := r.db.Conn.ExecContext(ctx, query, contact.Phone, contact.JID, now.Format(time.RFC3339), now.Format(time.RFC3339))
	return err
}

func (r *contactRepo) GetByPhone(ctx context.Context, phone string) (model.Contact, error) {
	query := `SELECT phone, jid, created_at, updated_at FROM contacts WHERE phone = ?`
	row := r.db.Conn.QueryRowContext(ctx, query, phone)

	var contact model.Contact
	var createdAt, updatedAt string
	err := row.Scan(&contact.Phone, &contact.JID, &createdAt, &updatedAt)
	if err == sql.ErrNoRows {
		return model.Contact{}, ErrNotFound
	}
	if err != nil {
		return model.Contact{}, err
	}

	contact.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	contact.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	return contact, nil
}
