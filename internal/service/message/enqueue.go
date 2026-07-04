package message

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage/model"
)

type EnqueueInput struct {
	InstanceID string
	To         string
	Type       string
	Payload    string
}

func (s *Service) Enqueue(ctx context.Context, input EnqueueInput) (model.Message, error) {
	if input.InstanceID == "" || input.To == "" || input.Payload == "" {
		return model.Message{}, ErrInvalidPayload
	}
	message := model.Message{
		ID:         uuid.NewString(),
		InstanceID: input.InstanceID,
		To:         input.To,
		Type:       input.Type,
		Payload:    input.Payload,
		Status:     "queued",
	}
	msg, err := s.repo.Create(ctx, message)
	if err != nil {
		return msg, err
	}

	if s.queue != nil {
		event := queue.Event{
			ID:         msg.ID,
			InstanceID: msg.InstanceID,
			Type:       msg.Type,
			Payload: map[string]interface{}{
				"to":   msg.To,
				"text": msg.Payload,
			},
			CreatedAt: msg.CreatedAt,
		}
		if err := s.queue.Enqueue(ctx, event); err != nil {
			s.log.Error("erro ao enfileirar mensagem para o worker", zap.Error(err))
		}
	}

	return msg, nil
}
