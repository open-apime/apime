package webhook

import (
	"context"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/webhook/delivery"
)

type Pool struct {
	queue        queue.Queue
	instanceRepo storage.InstanceRepository
	delivery     *delivery.Delivery
	log          *zap.Logger

	numWorkers int
	workers    []*poolWorker
	taskChan   chan *queue.Event
	wg         sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

type poolWorker struct {
	id       int
	taskChan chan *queue.Event
	log      *zap.Logger
	delivery *delivery.Delivery
	instRepo storage.InstanceRepository
}

func NewPool(
	q queue.Queue,
	instanceRepo storage.InstanceRepository,
	delivery *delivery.Delivery,
	log *zap.Logger,
	numWorkers int,
) *Pool {
	if numWorkers <= 0 {
		numWorkers = 4
	}

	return &Pool{
		queue:        q,
		instanceRepo: instanceRepo,
		delivery:     delivery,
		log:          log,
		numWorkers:   numWorkers,
		workers:      make([]*poolWorker, numWorkers),
		taskChan:     make(chan *queue.Event, numWorkers*2),
	}
}

func (p *Pool) Start(ctx context.Context) {
	p.ctx, p.cancel = context.WithCancel(ctx)

	p.log.Info("webhook pool: iniciando", zap.Int("workers", p.numWorkers))

	for i := 0; i < p.numWorkers; i++ {
		worker := &poolWorker{
			id:       i,
			taskChan: p.taskChan,
			log:      p.log,
			delivery: p.delivery,
			instRepo: p.instanceRepo,
		}
		p.workers[i] = worker

		p.wg.Add(1)
		go p.runWorker(worker)
	}

	p.wg.Add(1)
	go p.runDispatcher()

	p.log.Info("webhook pool: iniciada com sucesso")
}

func (p *Pool) Stop() {
	p.log.Info("webhook pool: encerrando")
	p.cancel()
	p.wg.Wait()
	close(p.taskChan)
	p.log.Info("webhook pool: encerrada")
}

func (p *Pool) runDispatcher() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			event, err := p.queue.Dequeue(p.ctx, 1*time.Second)
			if err != nil {
				p.log.Error("webhook pool: erro ao desenfileirar", zap.Error(err))
				continue
			}

			if event == nil {
				continue
			}

			select {
			case p.taskChan <- event:
			case <-p.ctx.Done():
				return
			case <-time.After(5 * time.Second):
				p.log.Warn("webhook pool: taskChan cheio, descartando evento", zap.String("eventId", event.ID))
			}
		}
	}
}

func (p *Pool) runWorker(worker *poolWorker) {
	defer p.wg.Done()

	p.log.Info("webhook pool: worker iniciado", zap.Int("workerId", worker.id))

	for {
		select {
		case <-p.ctx.Done():
			p.log.Info("webhook pool: worker encerrando", zap.Int("workerId", worker.id))
			return
		case event := <-worker.taskChan:
			if event == nil {
				return
			}
			worker.processEvent(p.ctx, event)
		}
	}
}

func (w *poolWorker) processEvent(ctx context.Context, event *queue.Event) {
	w.log.Debug("webhook pool: processando evento", zap.Int("workerId", w.id), zap.String("eventId", event.ID))

	inst, err := w.instRepo.GetByID(ctx, event.InstanceID)
	if err != nil {
		w.log.Error("webhook pool: instância não encontrada",
			zap.Int("workerId", w.id),
			zap.String("eventId", event.ID),
			zap.Error(err),
		)
		return
	}

	if inst.WebhookURL == "" {
		w.log.Warn("webhook pool: instância sem webhook configurado",
			zap.Int("workerId", w.id),
			zap.String("instanceId", event.InstanceID),
		)
		return
	}

	payload := map[string]interface{}{
		"id":         event.ID,
		"instanceId": event.InstanceID,
		"type":       event.Type,
		"payload":    event.Payload,
		"createdAt":  event.CreatedAt,
	}

	if err := w.delivery.Deliver(ctx, inst.WebhookURL, inst.WebhookSecret, payload); err != nil {
		w.log.Error("webhook pool: falha na entrega",
			zap.Int("workerId", w.id),
			zap.String("eventId", event.ID),
			zap.Error(err),
		)
		return
	}

	w.log.Info("webhook pool: evento entregue com sucesso",
		zap.Int("workerId", w.id),
		zap.String("eventId", event.ID),
	)
}
