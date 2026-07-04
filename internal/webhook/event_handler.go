package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/media"
)

type InstanceChecker interface {
	HasWebhook(ctx context.Context, instanceID string) bool
}

type JIDConfirmer interface {
	ConfirmJID(ctx context.Context, jid types.JID)
}

type EventHandler struct {
	queue           queue.Queue
	log             *zap.Logger
	mediaStorage    *media.Storage
	messageRepo     storage.MessageRepository
	apiBaseURL      string
	instanceChecker InstanceChecker
	jidConfirmer    JIDConfirmer
}

func NewEventHandler(q queue.Queue, log *zap.Logger, mediaStorage *media.Storage, messageRepo storage.MessageRepository, apiBaseURL string, instanceChecker InstanceChecker, jidConfirmer JIDConfirmer) *EventHandler {
	return &EventHandler{
		queue:           q,
		log:             log,
		mediaStorage:    mediaStorage,
		messageRepo:     messageRepo,
		apiBaseURL:      apiBaseURL,
		instanceChecker: instanceChecker,
		jidConfirmer:    jidConfirmer,
	}
}

func (h *EventHandler) Handle(ctx context.Context, instanceID string, instanceJID string, client *whatsmeow.Client, evt any) {
	if h.instanceChecker != nil && !h.instanceChecker.HasWebhook(ctx, instanceID) {
		h.log.Info("[dispatcher] evento ignorado: instância sem webhook configurado", zap.String("instance", instanceID))
		return
	}

	h.log.Debug("[dispatcher] processando evento para webhook", zap.String("instance", instanceID), zap.String("type", fmt.Sprintf("%T", evt)))

	h.confirmJIDFromEvent(ctx, evt)

	if receipt, ok := evt.(*events.Receipt); ok {
		status := string(receipt.Type)
		if receipt.Type == types.ReceiptTypeRetry {
			h.log.Warn("[dispatcher] RECEBIDO RETRY RECEIPT - Destinatário não conseguiu decriptar a mensagem",
				zap.Strings("msg_ids", receipt.MessageIDs),
				zap.String("chat", receipt.Chat.String()))
		}

		for _, msgID := range receipt.MessageIDs {
			if err := h.messageRepo.UpdateStatusByWhatsAppID(ctx, msgID, status); err != nil {
				h.log.Warn("[dispatcher] erro ao atualizar status da mensagem via receipt",
					zap.String("msg_id", msgID),
					zap.String("status", status),
					zap.Error(err))
			} else {
				h.log.Info("[dispatcher] status da mensagem atualizado via receipt",
					zap.String("msg_id", msgID),
					zap.String("status", status))
			}
		}
	}

	normalized := h.normalizeEvent(ctx, instanceID, client, evt)

	// Events marked as "ignore" don't generate a webhook (e.g. an undecryptable
	// message coming from myself or from a group).
	if t, _ := normalized["type"].(string); t == "ignore" {
		return
	}

	if instanceJID != "" {
		normalized["instanceJID"] = instanceJID
	}

	event := queue.Event{
		ID:         h.generateEventID(),
		InstanceID: instanceID,
		Type:       normalized["type"].(string),
		Payload:    normalized,
		CreatedAt:  time.Now(),
	}

	if err := h.queue.Enqueue(ctx, event); err != nil {
		h.log.Error("[dispatcher] event handler: erro ao enfileirar", zap.Error(err))
		return
	}

	h.log.Info("[dispatcher] evento enfileirado", zap.String("type", event.Type), zap.String("instance", instanceID))

	payloadInfo, _ := json.Marshal(event.Payload)
	h.log.Debug("[dispatcher] event handler: detalhes do evento",
		zap.String("type", event.Type),
		zap.String("instance", instanceID),
		zap.String("instanceJID", instanceJID),
		zap.String("payload", string(payloadInfo)),
	)
}
