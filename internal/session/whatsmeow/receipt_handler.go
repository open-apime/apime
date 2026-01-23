package whatsmeow

import (
	"context"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage"
)

type ReceiptHandler struct {
	messageRepo storage.MessageRepository
	log         *zap.Logger
}

func NewReceiptHandler(messageRepo storage.MessageRepository, log *zap.Logger) *ReceiptHandler {
	return &ReceiptHandler{
		messageRepo: messageRepo,
		log:         log,
	}
}

func (h *ReceiptHandler) HandleReceipt(ctx context.Context, instanceID string, receipt *events.Receipt) {
	if receipt == nil || len(receipt.MessageIDs) == 0 {
		return
	}

	var newStatus string
	switch receipt.Type {
	case types.ReceiptTypeDelivered:
		newStatus = "delivered"
	case types.ReceiptTypeRead:
		newStatus = "read"
	case types.ReceiptTypePlayed:
		newStatus = "played"
	case types.ReceiptTypeRetry:
		newStatus = "retry"
	default:
		return
	}

	for _, msgID := range receipt.MessageIDs {
		if err := h.messageRepo.UpdateStatusByWhatsAppID(ctx, msgID, newStatus); err != nil {
			h.log.Debug("falha ao atualizar status via receipt",
				zap.String("msg_id", msgID),
				zap.Error(err))
		} else {
			h.log.Info("status da mensagem atualizado via receipt",
				zap.String("msg_id", msgID),
				zap.String("status", newStatus))
		}
	}
}

func shouldUpdateStatus(currentStatus, newStatus string) bool {
	statusPriority := map[string]int{
		"pending":   0,
		"queued":    1,
		"sent":      2,
		"retry":     2,
		"delivered": 3,
		"read":      4,
		"played":    4,
		"failed":    -1,
	}

	current, okCurrent := statusPriority[currentStatus]
	new, okNew := statusPriority[newStatus]

	if !okCurrent || !okNew {
		return false
	}

	if current == -1 {
		return false
	}

	return new > current
}

type MessageStuckDetector struct {
	messageRepo   storage.MessageRepository
	sessionMgr    *Manager
	log           *zap.Logger
	stuckTimeout  time.Duration
	checkTicker   *time.Ticker
	stopChan      chan struct{}
	resetAttempts map[string]time.Time
	attemptsMu    sync.RWMutex
}

func NewMessageStuckDetector(
	messageRepo storage.MessageRepository,
	sessionMgr *Manager,
	log *zap.Logger,
	stuckTimeout time.Duration,
) *MessageStuckDetector {
	return &MessageStuckDetector{
		messageRepo:   messageRepo,
		sessionMgr:    sessionMgr,
		log:           log,
		stuckTimeout:  stuckTimeout,
		stopChan:      make(chan struct{}),
		resetAttempts: make(map[string]time.Time),
	}
}

func (d *MessageStuckDetector) Start(ctx context.Context, checkInterval time.Duration) {
	d.checkTicker = time.NewTicker(checkInterval)

	go func() {
		d.log.Info("detector de mensagens stuck iniciado",
			zap.Duration("check_interval", checkInterval),
			zap.Duration("stuck_timeout", d.stuckTimeout))

		for {
			select {
			case <-d.checkTicker.C:
				d.checkStuckMessages(ctx)
			case <-d.stopChan:
				d.log.Info("detector de mensagens stuck parado")
				return
			case <-ctx.Done():
				d.log.Info("detector de mensagens stuck cancelado via contexto")
				return
			}
		}
	}()
}

func (d *MessageStuckDetector) Stop() {
	if d.checkTicker != nil {
		d.checkTicker.Stop()
	}
	close(d.stopChan)
}

func (d *MessageStuckDetector) checkStuckMessages(ctx context.Context) {
	instances := d.sessionMgr.ListInstances()

	for _, instanceID := range instances {
		messages, err := d.messageRepo.ListByInstance(ctx, instanceID)
		if err != nil {
			d.log.Error("erro ao buscar mensagens para verificar stuck",
				zap.String("instance_id", instanceID),
				zap.Error(err))
			continue
		}

		for _, msg := range messages {
			isStuck := (msg.Status == "sent" || msg.Status == "retry") && time.Since(msg.CreatedAt) > d.stuckTimeout

			isRetry := msg.Status == "retry"

			if isStuck || isRetry {
				resetKey := instanceID + ":" + msg.To
				d.attemptsMu.RLock()
				lastAttempt, attempted := d.resetAttempts[resetKey]
				d.attemptsMu.RUnlock()

				retryDebounce := 1 * time.Hour
				if isRetry {
					retryDebounce = 5 * time.Minute
				}

				if attempted && time.Since(lastAttempt) < retryDebounce {
					if msg.Status != "failed_stuck" && !isRetry {
						msg.Status = "failed_stuck"
						if err := d.messageRepo.Update(ctx, msg); err != nil {
							d.log.Error("erro ao atualizar status de mensagem stuck",
								zap.String("message_id", msg.ID),
								zap.Error(err))
						}
					}
					continue
				}

				d.log.Warn("mensagem stuck detectada - acionando reset automático",
					zap.String("instance_id", instanceID),
					zap.String("message_id", msg.ID),
					zap.String("recipient", msg.To),
					zap.Duration("stuck_time", time.Since(msg.CreatedAt)))

				d.attemptsMu.Lock()
				d.resetAttempts[resetKey] = time.Now()
				d.attemptsMu.Unlock()

				if err := d.sessionMgr.ResetContactSession(ctx, instanceID, msg.To); err != nil {
					d.log.Error("FALHA ao resetar sessão do contato stuck - destinatário pode ser inválido",
						zap.String("instance_id", instanceID),
						zap.String("recipient", msg.To),
						zap.String("message_id", msg.ID),
						zap.Error(err))
					msg.Status = "failed_stuck"
					if updateErr := d.messageRepo.Update(ctx, msg); updateErr != nil {
						d.log.Error("erro ao atualizar status após falha no reset",
							zap.String("message_id", msg.ID),
							zap.Error(updateErr))
					}
				} else {
					d.log.Info("SUCESSO ao resetar sessão - próximo envio terá nova tentativa de key exchange",
						zap.String("instance_id", instanceID),
						zap.String("recipient", msg.To),
						zap.String("message_id", msg.ID))
					msg.Status = "failed_stuck"
					if err := d.messageRepo.Update(ctx, msg); err != nil {
						d.log.Error("erro ao atualizar status de mensagem stuck",
							zap.String("message_id", msg.ID),
							zap.Error(err))
					}
				}
			}
		}
	}
}
