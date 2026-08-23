package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/open-apime/apime/internal/storage/model"
)

type idempotencyRepo struct {
	db *DB
}

func NewIdempotencyRepository(db *DB) *idempotencyRepo {
	return &idempotencyRepo{db: db}
}

// TryAcquire relies on the primary key to settle the race: two concurrent
// requests with the same key both reach the INSERT, and only one affects a row.
// Checking with a SELECT first would let both through and send twice.
func (r *idempotencyRepo) TryAcquire(ctx context.Context, rec model.IdempotencyRecord) (bool, model.IdempotencyRecord, error) {
	// An expired key is as good as absent, so drop it before trying to own it.
	_, err := r.db.Conn.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE instance_id = ? AND idempotency_key = ? AND expires_at < ?`,
		rec.InstanceID, rec.Key, time.Now().Format(time.RFC3339))
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}

	res, err := r.db.Conn.ExecContext(ctx, `
		INSERT INTO idempotency_keys
			(instance_id, idempotency_key, request_hash, status, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (instance_id, idempotency_key) DO NOTHING
	`, rec.InstanceID, rec.Key, rec.RequestHash, model.IdempotencyStarted,
		rec.CreatedAt.Format(time.RFC3339), rec.ExpiresAt.Format(time.RFC3339))
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return true, model.IdempotencyRecord{}, nil
	}

	existing, err := r.get(ctx, rec.InstanceID, rec.Key)
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}
	return false, existing, nil
}

func (r *idempotencyRepo) get(ctx context.Context, instanceID, key string) (model.IdempotencyRecord, error) {
	row := r.db.Conn.QueryRowContext(ctx, `
		SELECT instance_id, idempotency_key, request_hash, status,
		       COALESCE(response_status, 0), COALESCE(response_body, ''), created_at, expires_at
		FROM idempotency_keys WHERE instance_id = ? AND idempotency_key = ?
	`, instanceID, key)

	var rec model.IdempotencyRecord
	var createdAt, expiresAt string
	err := row.Scan(&rec.InstanceID, &rec.Key, &rec.RequestHash, &rec.Status,
		&rec.ResponseStatus, &rec.ResponseBody, &createdAt, &expiresAt)
	if err == sql.ErrNoRows {
		return model.IdempotencyRecord{}, ErrNotFound
	}
	if err != nil {
		return model.IdempotencyRecord{}, err
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	rec.ExpiresAt, _ = time.Parse(time.RFC3339, expiresAt)
	return rec, nil
}

func (r *idempotencyRepo) Complete(ctx context.Context, instanceID, key string, status int, body string) error {
	_, err := r.db.Conn.ExecContext(ctx, `
		UPDATE idempotency_keys
		SET status = ?, response_status = ?, response_body = ?
		WHERE instance_id = ? AND idempotency_key = ?
	`, model.IdempotencyCompleted, status, body, instanceID, key)
	return err
}

// Release frees a key whose request never reached WhatsApp, so the caller can
// fix the payload and retry with the same key.
func (r *idempotencyRepo) Release(ctx context.Context, instanceID, key string) error {
	_, err := r.db.Conn.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE instance_id = ? AND idempotency_key = ?`, instanceID, key)
	return err
}

func (r *idempotencyRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.Conn.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE expires_at < ?`, now.Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
