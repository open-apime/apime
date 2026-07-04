package whatsmeow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/storage/model"
)

func (m *Manager) initHistorySyncCycle(instanceID string) {
	ctx := context.Background()

	if m.instanceRepo == nil || m.historySyncRepo == nil {
		m.log.Warn("repositórios não disponíveis para history sync", zap.String("instance_id", instanceID))
		return
	}

	cycleID := uuid.New().String()
	now := time.Now()

	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		m.log.Error("erro ao buscar instância para iniciar ciclo de sync",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	inst.HistorySyncCycleID = cycleID
	inst.HistorySyncStatus = model.HistorySyncStatusRunning
	inst.HistorySyncUpdatedAt = &now

	if _, err := m.instanceRepo.Update(ctx, inst); err != nil {
		m.log.Error("erro ao atualizar instância com cycle_id",
			zap.String("instance_id", instanceID),
			zap.String("cycle_id", cycleID),
			zap.Error(err),
		)
		return
	}

	m.log.Info("ciclo de history sync iniciado",
		zap.String("instance_id", instanceID),
		zap.String("cycle_id", cycleID),
	)

	workerCtx, workerCancel := context.WithCancel(context.Background())
	m.mu.Lock()
	if oldCancel, exists := m.syncWorkers[instanceID]; exists {
		oldCancel()
	}
	m.syncWorkers[instanceID] = workerCancel
	m.mu.Unlock()

	go m.runHistorySyncWorker(workerCtx, instanceID, cycleID)
}

func (m *Manager) persistHistorySyncNotification(instanceID string, notif any) {
	ctx := context.Background()

	if m.historySyncRepo == nil {
		m.log.Warn("history sync repo não disponível", zap.String("instance_id", instanceID))
		return
	}

	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		m.log.Error("erro ao buscar instância para persistir notification",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	payloadBytes, err := json.Marshal(notif)
	if err != nil {
		m.log.Error("erro ao serializar notification de history sync",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	payload := model.WhatsappHistorySync{
		InstanceID:  instanceID,
		PayloadType: "HistorySyncNotification",
		Payload:     payloadBytes,
		CycleID:     inst.HistorySyncCycleID,
		Status:      model.HistorySyncPayloadPending,
		CreatedAt:   time.Now(),
	}

	if _, err := m.historySyncRepo.Create(ctx, payload); err != nil {
		m.log.Error("erro ao persistir notification de history sync",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	m.log.Info("notification de history sync persistido",
		zap.String("instance_id", instanceID),
		zap.String("cycle_id", inst.HistorySyncCycleID),
	)
}

func (m *Manager) runHistorySyncWorker(ctx context.Context, instanceID, cycleID string) {
	m.log.Info("worker de history sync iniciado",
		zap.String("instance_id", instanceID),
		zap.String("cycle_id", cycleID),
	)

	// With ManualHistorySyncDownload enabled, history is not downloaded automatically.
	// The worker just marks the cycle as complete after a delay.
	time.Sleep(10 * time.Second)

	m.log.Info("finalizando ciclo de history sync (modo manual ativado)",
		zap.String("instance_id", instanceID),
		zap.String("cycle_id", cycleID),
	)

	m.finalizeHistorySyncCycle(instanceID, model.HistorySyncStatusCompleted)
}

func (m *Manager) processHistorySyncPayload(ctx context.Context, instanceID string, payload model.WhatsappHistorySync) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return nil
	}

	var notif struct {
		FileSHA256    []byte `json:"fileSHA256"`
		FileLength    uint64 `json:"fileLength"`
		MediaKey      []byte `json:"mediaKey"`
		FileEncSHA256 []byte `json:"fileEncSHA256"`
		DirectPath    string `json:"directPath"`
		SyncType      int32  `json:"syncType"`
		ChunkOrder    uint32 `json:"chunkOrder"`
	}

	if err := json.Unmarshal(payload.Payload, &notif); err != nil {
		m.log.Error("erro ao deserializar notification",
			zap.String("instance_id", instanceID),
			zap.String("payload_id", payload.ID),
			zap.Error(err),
		)
		return err
	}

	m.log.Info("processando history sync notification",
		zap.String("instance_id", instanceID),
		zap.String("payload_id", payload.ID),
		zap.Int32("sync_type", notif.SyncType),
	)

	// Mark as processed without a real download to avoid errors: the history was
	// already downloaded automatically by WhatsMeow before we enabled ManualHistorySyncDownload.
	return nil
}

func (m *Manager) finalizeHistorySyncCycle(instanceID string, status model.HistorySyncStatus) {
	ctx := context.Background()

	if m.instanceRepo == nil {
		return
	}

	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		m.log.Error("erro ao buscar instância para finalizar ciclo",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	now := time.Now()
	inst.HistorySyncStatus = status
	inst.HistorySyncUpdatedAt = &now

	if _, err := m.instanceRepo.Update(ctx, inst); err != nil {
		m.log.Error("erro ao finalizar ciclo de sync",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	m.log.Info("ciclo de history sync finalizado",
		zap.String("instance_id", instanceID),
		zap.String("status", string(status)),
	)

	m.mu.Lock()
	if cancel, exists := m.syncWorkers[instanceID]; exists {
		cancel()
		delete(m.syncWorkers, instanceID)
	}
	m.mu.Unlock()
}
