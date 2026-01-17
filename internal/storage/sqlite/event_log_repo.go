package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage/model"
)

type eventLogRepo struct {
	db *DB
}

func NewEventLogRepository(db *DB) *eventLogRepo {
	return &eventLogRepo{db: db}
}

func (r *eventLogRepo) Create(ctx context.Context, eventLog model.EventLog) (model.EventLog, error) {
	if eventLog.ID == "" {
		eventLog.ID = uuid.New().String()
	}
	eventLog.CreatedAt = time.Now()

	payloadJSON, err := json.Marshal(eventLog.Payload)
	if err != nil {
		return model.EventLog{}, err
	}

	var deliveredAt *string
	if eventLog.DeliveredAt != nil && !eventLog.DeliveredAt.IsZero() {
		t := eventLog.DeliveredAt.Format(time.RFC3339)
		deliveredAt = &t
	}

	query := `
		INSERT INTO event_logs (id, instance_id, type, payload, delivered_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err = r.db.Conn.ExecContext(ctx, query,
		eventLog.ID, eventLog.InstanceID, eventLog.Type, string(payloadJSON), deliveredAt, eventLog.CreatedAt.Format(time.RFC3339),
	)

	if err != nil {
		return model.EventLog{}, err
	}

	return eventLog, nil
}

func (r *eventLogRepo) ListByInstance(ctx context.Context, instanceID string) ([]model.EventLog, error) {
	query := `
		SELECT id, instance_id, type, payload, delivered_at, created_at
		FROM event_logs
		WHERE instance_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`

	rows, err := r.db.Conn.QueryContext(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var eventLogs []model.EventLog
	for rows.Next() {
		var eventLog model.EventLog
		var payloadStr string
		var createdAt string
		var deliveredAt sql.NullString

		if err := rows.Scan(
			&eventLog.ID, &eventLog.InstanceID, &eventLog.Type, &payloadStr, &deliveredAt, &createdAt,
		); err != nil {
			return nil, err
		}

			var payloadMap interface{}
		if err := json.Unmarshal([]byte(payloadStr), &payloadMap); err == nil {
			if str, ok := payloadMap.(string); ok {
				eventLog.Payload = str
			} else {
				eventLog.Payload = payloadStr
			}
		} else {
			eventLog.Payload = payloadStr
		}

		eventLog.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		if deliveredAt.Valid {
			eventLog.DeliveredAt = parseTimePtrEvent(deliveredAt.String)
		}

		eventLogs = append(eventLogs, eventLog)
	}

	return eventLogs, rows.Err()
}

func (r *eventLogRepo) DeleteByInstanceID(ctx context.Context, instanceID string) error {
	query := `DELETE FROM event_logs WHERE instance_id = ?`
	_, err := r.db.Conn.ExecContext(ctx, query, instanceID)
	return err
}

func parseTimePtrEvent(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}
