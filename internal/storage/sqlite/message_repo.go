package sqlite

import (
	"context"
	"database/sql"
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
		INSERT INTO message_queue (id, instance_id, whatsapp_id, recipient, type, payload, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err = r.db.Conn.ExecContext(ctx, query,
		msg.ID, msg.InstanceID, msg.WhatsAppID, msg.To, msg.Type, string(payloadJSON), msg.Status, msg.CreatedAt.Format(time.RFC3339),
	)

	if err != nil {
		return model.Message{}, err
	}

	return msg, nil
}

func (r *messageRepo) ListByInstance(ctx context.Context, instanceID string) ([]model.Message, error) {
	query := `
		SELECT id, instance_id, whatsapp_id, recipient, type, payload, status, delivered_at, created_at
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
		var whatsappID, deliveredAt sql.NullString

		if err := rows.Scan(
			&msg.ID, &msg.InstanceID, &whatsappID, &msg.To, &msg.Type, &payloadStr, &msg.Status, &deliveredAt, &createdAt,
		); err != nil {
			return nil, err
		}

		msg.WhatsAppID = whatsappID.String
		if deliveredAt.Valid {
			t, _ := time.Parse(time.RFC3339, deliveredAt.String)
			msg.DeliveredAt = &t
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
	var deliveredAt interface{}
	if msg.DeliveredAt != nil {
		deliveredAt = msg.DeliveredAt.Format(time.RFC3339)
	}

	query := `
		UPDATE message_queue
		SET status = ?, whatsapp_id = ?, delivered_at = ?
		WHERE id = ?
	`
	_, err := r.db.Conn.ExecContext(ctx, query, msg.Status, msg.WhatsAppID, deliveredAt, msg.ID)
	return err
}

func (r *messageRepo) UpdateStatusByWhatsAppID(ctx context.Context, whatsappID string, status string) error {
	deliveredAt := time.Now().Format(time.RFC3339)
	query := `
		UPDATE message_queue
		SET status = ?, delivered_at = ?
		WHERE whatsapp_id = ?
	`
	_, err := r.db.Conn.ExecContext(ctx, query, status, deliveredAt, whatsappID)
	return err
}

func (r *messageRepo) GetByWhatsAppID(ctx context.Context, whatsappID string) (model.Message, error) {
	query := `
		SELECT id, instance_id, whatsapp_id, recipient, type, payload, status, delivered_at, created_at
		FROM message_queue
		WHERE whatsapp_id = ?
		LIMIT 1
	`

	var msg model.Message
	var payloadStr string
	var createdAt string
	var wID, deliveredAt sql.NullString

	err := r.db.Conn.QueryRowContext(ctx, query, whatsappID).Scan(
		&msg.ID, &msg.InstanceID, &wID, &msg.To, &msg.Type, &payloadStr, &msg.Status, &deliveredAt, &createdAt,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return model.Message{}, err // Retorna o erro nativo para evitar import cycle
		}
		return model.Message{}, err
	}

	msg.WhatsAppID = wID.String
	if deliveredAt.Valid {
		t, _ := time.Parse(time.RFC3339, deliveredAt.String)
		msg.DeliveredAt = &t
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
	return msg, nil
}

func (r *messageRepo) DeleteByInstanceID(ctx context.Context, instanceID string) error {
	query := `DELETE FROM message_queue WHERE instance_id = ?`
	_, err := r.db.Conn.ExecContext(ctx, query, instanceID)
	return err
}
