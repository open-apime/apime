package session

import (
	"context"
	"time"

	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

type Watchdog struct {
	instanceID   string
	repo         storage.InstanceRepository
	log          *zap.Logger
	onDisconnect func(string)
}

func NewWatchdog(instanceID string, repo storage.InstanceRepository, log *zap.Logger) *Watchdog {
	return &Watchdog{
		instanceID: instanceID,
		repo:       repo,
		log:        log,
	}
}

func (w *Watchdog) SetOnDisconnect(fn func(string)) {
	w.onDisconnect = fn
}

func (w *Watchdog) Start(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			inst, err := w.repo.GetByID(ctx, w.instanceID)
			if err != nil {
				w.log.Error("watchdog: erro ao obter instância", zap.Error(err))
				continue
			}

			if inst.Status == model.InstanceStatusError {
				w.log.Warn("watchdog: instância em erro", zap.String("instance", w.instanceID))
				if w.onDisconnect != nil {
					w.onDisconnect(w.instanceID)
				}
			}
		}
	}
}

func (w *Watchdog) HandleEvent(evt any) {
	switch evt.(type) {
	case *events.Disconnected:
		w.log.Warn("watchdog: desconectado", zap.String("instance", w.instanceID))
		ctx := context.Background()
		inst, err := w.repo.GetByID(ctx, w.instanceID)
		if err == nil {
			inst.Status = model.InstanceStatusError
			w.repo.Update(ctx, inst)
		}
		if w.onDisconnect != nil {
			w.onDisconnect(w.instanceID)
		}
	case *events.Connected:
		w.log.Info("watchdog: conectado", zap.String("instance", w.instanceID))
		ctx := context.Background()
		inst, err := w.repo.GetByID(ctx, w.instanceID)
		if err == nil {
			inst.Status = model.InstanceStatusActive
			w.repo.Update(ctx, inst)
		}
	}
}
