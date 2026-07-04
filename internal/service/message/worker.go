package message

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/open-apime/apime/internal/pkg/queue"
	"go.uber.org/zap"
)

type OutboxWorker struct {
	service    *Service
	queue      queue.Queue
	log        *zap.Logger
	numWorkers int
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

func NewOutboxWorker(service *Service, q queue.Queue, log *zap.Logger, numWorkers int) *OutboxWorker {
	if numWorkers <= 0 {
		numWorkers = 5
	}
	return &OutboxWorker{
		service:    service,
		queue:      q,
		log:        log,
		numWorkers: numWorkers,
	}
}

func (w *OutboxWorker) Start(ctx context.Context) {
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.log.Info("outbox worker: iniciando", zap.Int("workers", w.numWorkers))

	for i := 0; i < w.numWorkers; i++ {
		w.wg.Add(1)
		go w.runWorker(i + 1)
	}

	w.wg.Add(1)
	go w.runStuckRecovery()
}

func (w *OutboxWorker) Stop() {
	w.log.Info("outbox worker: encerrando")
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *OutboxWorker) runWorker(id int) {
	defer w.wg.Done()
	prefix := fmt.Sprintf("[outbox-worker %d]", id)
	w.log.Info(prefix + ": iniciado")

	for {
		select {
		case <-w.ctx.Done():
			w.log.Info(prefix + ": parando")
			return
		default:
			event, err := w.queue.Dequeue(w.ctx, 1*time.Second)
			if err != nil {
				w.log.Error(prefix+": erro ao desenfileirar", zap.Error(err))
				continue
			}

			if event == nil {
				continue
			}

			w.processEvent(prefix, event)
		}
	}
}

func (w *OutboxWorker) processEvent(prefix string, event *queue.Event) {
	w.log.Info(prefix+": processando mensagem da fila",
		zap.String("id", event.ID),
		zap.String("instance_id", event.InstanceID))

	to, _ := event.Payload["to"].(string)
	text, _ := event.Payload["text"].(string)

	input := SendInput{
		InstanceID: event.InstanceID,
		To:         to,
		Type:       event.Type,
		Text:       text,
		MessageID:  event.ID,
	}

	// We use service.Send here because it already has the retry loop and AUTO-TRUST
	_, err := w.service.Send(w.ctx, input)
	if err != nil {
		w.log.Error(prefix+": falha final ao enviar mensagem",
			zap.String("id", event.ID),
			zap.Error(err))
	}
}

func (w *OutboxWorker) runStuckRecovery() {
	defer w.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			messages, err := w.service.repo.GetPendingMessages(w.ctx, 50)
			if err != nil {
				w.log.Error("outbox recovery: erro ao buscar mensagens pendentes", zap.Error(err))
				continue
			}

			if len(messages) == 0 {
				continue
			}

			w.log.Info("outbox recovery: recuperando mensagens pendentes do banco", zap.Int("count", len(messages)))
			for _, msg := range messages {
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
				_ = w.queue.Enqueue(w.ctx, event)
			}
		}
	}
}
