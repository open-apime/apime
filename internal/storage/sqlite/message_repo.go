package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage/model"
)

type messageRepo struct {
	db *DB
}

func NewMessageRepository(db *DB) *messageRepo {
	return &messageRepo{db: db}
}

func (r *messageRepo) Create(ctx context.Context, msg model.Message) (model.Message, error) {
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	msg.CreatedAt = time.Now()

	payloadJSON, err := json.Marshal(map[string]interface{}{
		"text": msg.Payload,
	})
	if err != nil {
		return model.Message{}, err
	}

	query := `
		INSERT INTO message_queue (id, instance_id, recipient, type, payload, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err = r.db.Conn.ExecContext(ctx, query,
		msg.ID, msg.InstanceID, msg.To, msg.Type, string(payloadJSON), msg.Status, msg.CreatedAt.Format(time.RFC3339),
	)

	if err != nil {
		return model.Message{}, err
	}

	return msg, nil
}

func (r *messageRepo) ListByInstance(ctx context.Context, instanceID string) ([]model.Message, error) {
	query := `
		SELECT id, instance_id, recipient, type, payload, status, created_at
		FROM message_queue
		WHERE instance_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`

	rows, err := r.db.Conn.QueryContext(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []model.Message
	for rows.Next() {
		var msg model.Message
		var payloadStr string
		var createdAt string

		if err := rows.Scan(
			&msg.ID, &msg.InstanceID, &msg.To, &msg.Type, &payloadStr, &msg.Status, &createdAt,
		); err != nil {
			return nil, err
		}

		var payloadMap map[string]interface{}
		if err := json.Unmarshal([]byte(payloadStr), &payloadMap); err == nil {
			if text, ok := payloadMap["text"].(string); ok {
				msg.Payload = text
			} else {
				msg.Payload = payloadStr
			}
		} else {
			msg.Payload = payloadStr
		}

		msg.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

func (r *messageRepo) Update(ctx context.Context, msg model.Message) error {
	query := `
		UPDATE message_queue
		SET status = ?
		WHERE id = ?
	`
	_, err := r.db.Conn.ExecContext(ctx, query, msg.Status, msg.ID)
	return err
}

func (r *messageRepo) DeleteByInstanceID(ctx context.Context, instanceID string) error {
	query := `DELETE FROM message_queue WHERE instance_id = ?`
	_, err := r.db.Conn.ExecContext(ctx, query, instanceID)
	return err
}
