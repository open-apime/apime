package postgres

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
		INSERT INTO message_queue (id, instance_id, whatsapp_id, recipient, type, payload, status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8)
		RETURNING id, instance_id, whatsapp_id, recipient, type, payload, status, created_at
	`

	var payloadBytes []byte
	var whatsappID *string
	err = r.db.Pool.QueryRow(ctx, query,
		msg.ID, msg.InstanceID, msg.WhatsAppID, msg.To, msg.Type, payloadJSON, msg.Status, msg.CreatedAt,
	).Scan(
		&msg.ID, &msg.InstanceID, &whatsappID, &msg.To, &msg.Type, &payloadBytes, &msg.Status, &msg.CreatedAt,
	)

	if err != nil {
		return model.Message{}, err
	}

	if whatsappID != nil {
		msg.WhatsAppID = *whatsappID
	}

	var payloadMap map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payloadMap); err == nil {
		if text, ok := payloadMap["text"].(string); ok {
			msg.Payload = text
		} else {
			msg.Payload = string(payloadBytes)
		}
	} else {
		msg.Payload = string(payloadBytes)
	}

	return msg, nil
}

func (r *messageRepo) ListByInstance(ctx context.Context, instanceID string) ([]model.Message, error) {
	query := `
		SELECT id, instance_id, whatsapp_id, recipient, type, payload, status, delivered_at, created_at
		FROM message_queue
		WHERE instance_id = $1
		ORDER BY created_at DESC
		LIMIT 100
	`

	rows, err := r.db.Pool.Query(ctx, query, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []model.Message
	for rows.Next() {
		var msg model.Message
		var payloadBytes []byte
		var whatsappID *string
		if err := rows.Scan(
			&msg.ID, &msg.InstanceID, &whatsappID, &msg.To, &msg.Type, &payloadBytes, &msg.Status, &msg.DeliveredAt, &msg.CreatedAt,
		); err != nil {
			return nil, err
		}

		if whatsappID != nil {
			msg.WhatsAppID = *whatsappID
		}

		var payloadMap map[string]interface{}
		if err := json.Unmarshal(payloadBytes, &payloadMap); err == nil {
			if text, ok := payloadMap["text"].(string); ok {
				msg.Payload = text
			} else {
				msg.Payload = string(payloadBytes)
			}
		} else {
			msg.Payload = string(payloadBytes)
		}

		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

func (r *messageRepo) Update(ctx context.Context, msg model.Message) error {
	query := `
		UPDATE message_queue
		SET status = $1, whatsapp_id = $2, delivered_at = $3
		WHERE id = $4
	`
	_, err := r.db.Pool.Exec(ctx, query, msg.Status, msg.WhatsAppID, msg.DeliveredAt, msg.ID)
	return err
}

func (r *messageRepo) UpdateStatusByWhatsAppID(ctx context.Context, whatsappID string, status string) error {
	query := `
		UPDATE message_queue
		SET status = $1, delivered_at = NOW()
		WHERE whatsapp_id = $2
	`
	_, err := r.db.Pool.Exec(ctx, query, status, whatsappID)
	return err
}

func (r *messageRepo) GetByWhatsAppID(ctx context.Context, whatsappID string) (model.Message, error) {
	query := `
		SELECT id, instance_id, whatsapp_id, recipient, type, payload, status, delivered_at, created_at
		FROM message_queue
		WHERE whatsapp_id = $1
		LIMIT 1
	`

	var msg model.Message
	var payloadBytes []byte
	var wID *string
	err := r.db.Pool.QueryRow(ctx, query, whatsappID).Scan(
		&msg.ID, &msg.InstanceID, &wID, &msg.To, &msg.Type, &payloadBytes, &msg.Status, &msg.DeliveredAt, &msg.CreatedAt,
	)

	if err != nil {
		return model.Message{}, err
	}

	if wID != nil {
		msg.WhatsAppID = *wID
	}

	var payloadMap map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payloadMap); err == nil {
		if text, ok := payloadMap["text"].(string); ok {
			msg.Payload = text
		} else {
			msg.Payload = string(payloadBytes)
		}
	} else {
		msg.Payload = string(payloadBytes)
	}

	return msg, nil
}

func (r *messageRepo) DeleteByInstanceID(ctx context.Context, instanceID string) error {
	query := `DELETE FROM message_queue WHERE instance_id = $1`
	_, err := r.db.Pool.Exec(ctx, query, instanceID)
	return err
}
