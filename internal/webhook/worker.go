package webhook

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/webhook/delivery"
)

type Worker struct {
	queue        queue.Queue
	instanceRepo storage.InstanceRepository
	delivery     *delivery.Delivery
	log          *zap.Logger
}

func NewWorker(
	q queue.Queue,
	instanceRepo storage.InstanceRepository,
	delivery *delivery.Delivery,
	log *zap.Logger,
) *Worker {
	return &Worker{
		queue:        q,
		instanceRepo: instanceRepo,
		delivery:     delivery,
		log:          log,
	}
}

func (w *Worker) Start(ctx context.Context) {
	w.log.Info("webhook worker: iniciado")

	for {
		select {
		case <-ctx.Done():
			w.log.Info("webhook worker: encerrando")
			return
		default:
			w.processNext(ctx)
		}
	}
}

func (w *Worker) processNext(ctx context.Context) {
	event, err := w.queue.Dequeue(ctx, 5*time.Second)
	if err != nil {
		w.log.Error("webhook worker: erro ao desenfileirar", zap.Error(err))
		return
	}

	if event == nil {
		return // Timeout, sem eventos
	}

	w.log.Info("webhook worker: processando evento", zap.String("id", event.ID))

	// Obter instância para determinar webhook
	inst, err := w.instanceRepo.GetByID(ctx, event.InstanceID)
	if err != nil {
		w.log.Error("webhook worker: instância não encontrada", zap.Error(err))
		return
	}

	if inst.WebhookURL == "" {
		w.log.Warn("webhook worker: instância sem webhook configurado")
		return
	}

	// Preparar payload do evento
	payload := map[string]interface{}{
		"id":         event.ID,
		"instanceId": event.InstanceID,
		"type":       event.Type,
		"payload":    event.Payload,
		"createdAt":  event.CreatedAt,
	}

	// Entregar webhook
	if err := w.delivery.Deliver(ctx, inst.WebhookURL, inst.WebhookSecret, payload); err != nil {
		w.log.Error("webhook worker: falha na entrega", zap.Error(err))
		return
	}

	w.log.Info("webhook worker: evento entregue com sucesso", zap.String("eventId", event.ID))
}
