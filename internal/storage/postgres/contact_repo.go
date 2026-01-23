package postgres

import (
	"context"
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
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (phone) DO UPDATE SET
			jid = EXCLUDED.jid,
			updated_at = EXCLUDED.updated_at
	`
	_, err := r.db.Pool.Exec(ctx, query, contact.Phone, contact.JID, now, now)
	return err
}

func (r *contactRepo) GetByPhone(ctx context.Context, phone string) (model.Contact, error) {
	query := `SELECT phone, jid, created_at, updated_at FROM contacts WHERE phone = $1`
	row := r.db.Pool.QueryRow(ctx, query, phone)

	var contact model.Contact
	err := row.Scan(&contact.Phone, &contact.JID, &contact.CreatedAt, &contact.UpdatedAt)
	if err.Error() == "no rows in result set" || err.Error() == "jackc/pgx/v5: no rows in result set" {
		return model.Contact{}, ErrNotFound
	}
	if err != nil {
		return model.Contact{}, err
	}

	return contact, nil
}
