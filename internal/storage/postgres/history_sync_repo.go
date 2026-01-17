package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/open-apime/apime/internal/storage/model"
)

type historySyncRepo struct {
	db *DB
}

func NewHistorySyncRepository(db *DB) *historySyncRepo {
	return &historySyncRepo{db: db}
}

func (r *historySyncRepo) Create(ctx context.Context, payload model.WhatsappHistorySync) (model.WhatsappHistorySync, error) {
	if payload.ID == "" {
		payload.ID = uuid.New().String()
	}
	if payload.CreatedAt.IsZero() {
		payload.CreatedAt = time.Now()
	}

	query := `INSERT INTO whatsapp_history_syncs (id, instance_id, payload_type, payload, cycle_id, status, created_at, processed_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	_, err := r.db.Pool.Exec(ctx, query,
		payload.ID,
		payload.InstanceID,
		payload.PayloadType,
		payload.Payload,
		nullIfEmpty(payload.CycleID),
		string(payload.Status),
		payload.CreatedAt,
		payload.ProcessedAt,
	)
	if err != nil {
		return model.WhatsappHistorySync{}, err
	}

	return payload, nil
}

func (r *historySyncRepo) ListPendingByInstance(ctx context.Context, instanceID string) ([]model.WhatsappHistorySync, error) {
	query := `SELECT id, instance_id, payload_type, payload, COALESCE(cycle_id::text, ''), status, created_at, processed_at
        FROM whatsapp_history_syncs
        WHERE instance_id = $1 AND status != 'done'
        ORDER BY created_at ASC`

	rows, err := r.db.Pool.Query(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanRows(rows)
}

func (r *historySyncRepo) ListPendingByCycle(ctx context.Context, instanceID, cycleID string) ([]model.WhatsappHistorySync, error) {
	query := `SELECT id, instance_id, payload_type, payload, COALESCE(cycle_id::text, ''), status, created_at, processed_at
        FROM whatsapp_history_syncs
        WHERE instance_id = $1 AND COALESCE(cycle_id::text, '') = $2 AND status != 'done'
        ORDER BY created_at ASC`

	rows, err := r.db.Pool.Query(ctx, query, instanceID, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanRows(rows)
}

func (r *historySyncRepo) UpdateStatus(ctx context.Context, id string, status model.HistorySyncPayloadStatus, processedAt *time.Time) error {
	query := `UPDATE whatsapp_history_syncs SET status = $2, processed_at = $3 WHERE id = $1`
	result, err := r.db.Pool.Exec(ctx, query, id, string(status), processedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *historySyncRepo) DeleteByInstance(ctx context.Context, instanceID string) error {
	_, err := r.db.Pool.Exec(ctx, `DELETE FROM whatsapp_history_syncs WHERE instance_id = $1`, instanceID)
	return err
}

func (r *historySyncRepo) scanRows(rows pgx.Rows) ([]model.WhatsappHistorySync, error) {
	var list []model.WhatsappHistorySync
	for rows.Next() {
		var payload model.WhatsappHistorySync
		var (
			cycleID     string
			processedAt *time.Time
		)
		if err := rows.Scan(
			&payload.ID,
			&payload.InstanceID,
			&payload.PayloadType,
			&payload.Payload,
			&cycleID,
			&payload.Status,
			&payload.CreatedAt,
			&processedAt,
		); err != nil {
			return nil, err
		}
		payload.CycleID = cycleID
		payload.ProcessedAt = processedAt
		list = append(list, payload)
	}
	return list, rows.Err()
}
