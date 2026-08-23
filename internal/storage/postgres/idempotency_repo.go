package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
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
	_, err := r.db.Pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2 AND expires_at < $3`,
		rec.InstanceID, rec.Key, time.Now())
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}

	tag, err := r.db.Pool.Exec(ctx, `
		INSERT INTO idempotency_keys
			(instance_id, idempotency_key, request_hash, status, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (instance_id, idempotency_key) DO NOTHING
	`, rec.InstanceID, rec.Key, rec.RequestHash, model.IdempotencyStarted, rec.CreatedAt, rec.ExpiresAt)
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}
	if tag.RowsAffected() == 1 {
		return true, model.IdempotencyRecord{}, nil
	}

	existing, err := r.get(ctx, rec.InstanceID, rec.Key)
	if err != nil {
		return false, model.IdempotencyRecord{}, err
	}
	return false, existing, nil
}

func (r *idempotencyRepo) get(ctx context.Context, instanceID, key string) (model.IdempotencyRecord, error) {
	row := r.db.Pool.QueryRow(ctx, `
		SELECT instance_id, idempotency_key, request_hash, status,
		       COALESCE(response_status, 0), COALESCE(response_body, ''), created_at, expires_at
		FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2
	`, instanceID, key)

	var rec model.IdempotencyRecord
	err := row.Scan(&rec.InstanceID, &rec.Key, &rec.RequestHash, &rec.Status,
		&rec.ResponseStatus, &rec.ResponseBody, &rec.CreatedAt, &rec.ExpiresAt)
	if err == pgx.ErrNoRows {
		return model.IdempotencyRecord{}, ErrNotFound
	}
	if err != nil {
		return model.IdempotencyRecord{}, err
	}
	return rec, nil
}

func (r *idempotencyRepo) Complete(ctx context.Context, instanceID, key string, status int, body string) error {
	_, err := r.db.Pool.Exec(ctx, `
		UPDATE idempotency_keys
		SET status = $1, response_status = $2, response_body = $3
		WHERE instance_id = $4 AND idempotency_key = $5
	`, model.IdempotencyCompleted, status, body, instanceID, key)
	return err
}

// Release frees a key whose request never reached WhatsApp, so the caller can
// fix the payload and retry with the same key.
func (r *idempotencyRepo) Release(ctx context.Context, instanceID, key string) error {
	_, err := r.db.Pool.Exec(ctx,
		`DELETE FROM idempotency_keys WHERE instance_id = $1 AND idempotency_key = $2`, instanceID, key)
	return err
}

func (r *idempotencyRepo) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.db.Pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < $1`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
