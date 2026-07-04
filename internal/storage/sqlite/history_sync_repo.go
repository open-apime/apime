package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"

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
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := r.db.Conn.ExecContext(
		ctx,
		query,
		payload.ID,
		payload.InstanceID,
		payload.PayloadType,
		payload.Payload,
		nullIfEmpty(payload.CycleID),
		string(payload.Status),
		payload.CreatedAt.Format(time.RFC3339),
		formatTimePtr(payload.ProcessedAt),
	)
	if err != nil {
		return model.WhatsappHistorySync{}, err
	}

	return payload, nil
}

func (r *historySyncRepo) ListPendingByInstance(ctx context.Context, instanceID string) ([]model.WhatsappHistorySync, error) {
	query := `SELECT id, instance_id, payload_type, payload, COALESCE(cycle_id, ''), status, created_at, processed_at
        FROM whatsapp_history_syncs
        WHERE instance_id = ? AND status != 'done'
        ORDER BY created_at ASC`

	rows, err := r.db.Conn.QueryContext(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanRows(rows)
}

func (r *historySyncRepo) ListPendingByCycle(ctx context.Context, instanceID, cycleID string) ([]model.WhatsappHistorySync, error) {
	query := `SELECT id, instance_id, payload_type, payload, COALESCE(cycle_id, ''), status, created_at, processed_at
        FROM whatsapp_history_syncs
        WHERE instance_id = ? AND COALESCE(cycle_id, '') = ? AND status != 'done'
        ORDER BY created_at ASC`

	rows, err := r.db.Conn.QueryContext(ctx, query, instanceID, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return r.scanRows(rows)
}

func (r *historySyncRepo) UpdateStatus(ctx context.Context, id string, status model.HistorySyncPayloadStatus, processedAt *time.Time) error {
	query := `UPDATE whatsapp_history_syncs
        SET status = ?, processed_at = ?
        WHERE id = ?`

	result, err := r.db.Conn.ExecContext(ctx, query, string(status), formatTimePtr(processedAt), id)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *historySyncRepo) DeleteByInstance(ctx context.Context, instanceID string) error {
	_, err := r.db.Conn.ExecContext(ctx, `DELETE FROM whatsapp_history_syncs WHERE instance_id = ?`, instanceID)
	return err
}

func (r *historySyncRepo) scanRows(rows *sql.Rows) ([]model.WhatsappHistorySync, error) {
	var list []model.WhatsappHistorySync
	for rows.Next() {
		var (
			payload      model.WhatsappHistorySync
			cycleID      string
			createdAtStr string
			processedAt  sql.NullString
		)
		if err := rows.Scan(
			&payload.ID,
			&payload.InstanceID,
			&payload.PayloadType,
			&payload.Payload,
			&cycleID,
			&payload.Status,
			&createdAtStr,
			&processedAt,
		); err != nil {
			return nil, err
		}
		payload.CycleID = cycleID
		if t, err := time.Parse(time.RFC3339, createdAtStr); err == nil {
			payload.CreatedAt = t
		}
		payload.ProcessedAt = parseTimePtr(processedAt.String)
		list = append(list, payload)
	}
	return list, rows.Err()
}
