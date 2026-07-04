package whatsmeow

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/crypto"
	"github.com/open-apime/apime/internal/storage/model"
)

func (m *Manager) RestoreSession(ctx context.Context, instanceID string, encryptedBlob []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := crypto.Decrypt(encryptedBlob, m.encKey)
	if err != nil {
		return fmt.Errorf("whatsmeow: descriptografar: %w", err)
	}

	clientLog := &zapLogger{log: m.log, module: "whatsmeow"}
	dbPath := fmt.Sprintf("file:%s?_foreign_keys=on", filepath.Join(m.baseDir, instanceID+".db"))
	container, err := sqlstore.New(ctx, "sqlite3", dbPath, clientLog)
	if err != nil {
		return fmt.Errorf("whatsmeow: criar store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("whatsmeow: obter device: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.EnableAutoReconnect = true
	client.ManualHistorySyncDownload = false // downloads history (recent/limited) in the background → delivers NctSalt + tokens (fix 463) and recent messages
	client.AutomaticMessageRerequestFromPhone = true
	client.GetMessageForRetry = m.getMessageForRetryCallback(instanceID)

	err = client.Connect()
	if err != nil {
		return fmt.Errorf("whatsmeow: conectar: %w", err)
	}

	m.clients[instanceID] = client
	return nil
}

func (m *Manager) logoutClient(instanceID string, client *whatsmeow.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.Logout(ctx); err != nil {
		m.log.Warn("logout falhou, forçando disconnect",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		client.Disconnect()
		return
	}

	m.log.Info("logout concluído",
		zap.String("instance_id", instanceID),
	)
}

func (m *Manager) Disconnect(instanceID string) error {
	return m.DeleteSession(instanceID)
}

func (m *Manager) DeleteSession(instanceID string) error {
	var client *whatsmeow.Client

	m.mu.Lock()
	m.expectedDisconnect[instanceID] = true
	if cancel, exists := m.qrContexts[instanceID]; exists {
		cancel()
		delete(m.qrContexts, instanceID)
	}
	if c, exists := m.clients[instanceID]; exists {
		client = c
		delete(m.clients, instanceID)
	}
	delete(m.currentQRs, instanceID)
	delete(m.sessionReady, instanceID)
	delete(m.pairingSuccess, instanceID)
	m.mu.Unlock()

	if client == nil {
		m.log.Debug("cliente não encontrado em memória, tentando restaurar para logout", zap.String("instance_id", instanceID))
		if restoredClient, err := m.restoreSessionIfExists(context.Background(), instanceID); err == nil && restoredClient != nil {
			client = restoredClient

			m.mu.Lock()
			delete(m.clients, instanceID)
			delete(m.sessionReady, instanceID)
			m.mu.Unlock()
		}
	}

	if client != nil {
		m.logoutClient(instanceID, client)
	}

	m.logConnectionEvent(instanceID, "manual_disconnect", `{"message":"Desconectado manualmente pelo usuário"}`)

	if m.instanceRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		inst, err := m.instanceRepo.GetByID(ctx, instanceID)
		if err == nil {
			inst.WhatsAppJID = ""
			inst.Status = model.InstanceStatusDisconnected
			if _, err := m.instanceRepo.Update(ctx, inst); err != nil {
				m.log.Warn("erro ao limpar JID da instância no DeleteSession",
					zap.String("instance_id", instanceID),
					zap.Error(err))
			} else {
				m.log.Info("JID e status limpos com sucesso no DeleteSession",
					zap.String("instance_id", instanceID))
			}
		}
	}

	if m.storageDriver == "postgres" && m.pgConnString != "" {
		if err := m.deletePostgresSession(instanceID); err != nil {
			return err
		}
		return nil
	}

	dbPath := filepath.Join(m.baseDir, instanceID+".db")
	if _, err := os.Stat(dbPath); err == nil {
		if err := os.Remove(dbPath); err != nil {
			m.log.Warn("erro ao deletar arquivo SQLite",
				zap.String("instance_id", instanceID),
				zap.String("db_path", dbPath),
				zap.Error(err),
			)
		} else {
			m.log.Info("arquivo SQLite deletado",
				zap.String("instance_id", instanceID),
				zap.String("db_path", dbPath),
			)
		}
	}

	return nil
}

func (m *Manager) deletePostgresSession(instanceID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var jidStr string
	if m.instanceRepo != nil {
		inst, err := m.instanceRepo.GetByID(ctx, instanceID)
		if err != nil {
			m.log.Warn("instância não encontrada para deletar sessão PostgreSQL",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			return nil
		}
		jidStr = inst.WhatsAppJID
	}

	if jidStr == "" {
		m.log.Debug("instância sem JID salvo, nada a deletar no PostgreSQL",
			zap.String("instance_id", instanceID),
		)
		return nil
	}

	if m.sharedContainer == nil {
		m.log.Error("container PostgreSQL compartilhado não disponível para deleção", zap.String("instance_id", instanceID))
		return nil
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("whatsmeow: parse device JID: %w", err)
	}

	store, err := m.sharedContainer.GetDevice(ctx, jid)
	if err != nil {
		return fmt.Errorf("whatsmeow: obter device PostgreSQL: %w", err)
	}
	if store == nil {
		m.log.Debug("sessão PostgreSQL já removida",
			zap.String("instance_id", instanceID),
		)
		return nil
	}

	if err := store.Delete(ctx); err != nil {
		return fmt.Errorf("whatsmeow: deletar device PostgreSQL: %w", err)
	}

	m.log.Info("sessão removida do PostgreSQL",
		zap.String("instance_id", instanceID),
		zap.String("jid", jidStr),
	)
	return nil
}

func (m *Manager) GetClient(instanceID string) (*whatsmeow.Client, error) {
	m.log.Debug("GetClient chamado",
		zap.String("instance_id", instanceID),
	)

	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if exists {
		m.log.Debug("cliente encontrado no map",
			zap.String("instance_id", instanceID),
		)

		isLoggedIn := client.IsLoggedIn()
		if !isLoggedIn {
			m.log.Warn("cliente existe mas não está logado, verificando status de pareamento",
				zap.String("instance_id", instanceID),
			)

			m.mu.RLock()
			_, hasQR := m.qrContexts[instanceID]
			pairingTime, hasPairing := m.pairingSuccess[instanceID]
			m.mu.RUnlock()

			if hasQR {
				m.log.Debug("contexto QR ativo, mantendo cliente no map", zap.String("instance_id", instanceID))
				return client, nil
			}
			if hasPairing && time.Since(pairingTime) < 60*time.Second {
				m.log.Debug("pareamento recente detectado, mantendo cliente no map",
					zap.String("instance_id", instanceID),
					zap.Duration("since_pairing", time.Since(pairingTime)),
				)
				return client, nil
			}

			m.log.Warn("cliente sem sessão ativa e sem pareamento recente, removendo", zap.String("instance_id", instanceID))

			m.mu.Lock()
			delete(m.clients, instanceID)
			m.mu.Unlock()

			return m.restoreSessionIfExists(context.Background(), instanceID)
		}
		m.log.Debug("cliente está logado e pronto",
			zap.String("instance_id", instanceID),
		)
		return client, nil
	}

	m.log.Debug("cliente não encontrado no map, tentando restaurar sessão",
		zap.String("instance_id", instanceID),
	)

	return m.restoreSessionIfExists(context.Background(), instanceID)
}

func (m *Manager) restoreSessionIfExists(ctx context.Context, instanceID string) (*whatsmeow.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, exists := m.clients[instanceID]; exists {
		return client, nil
	}

	clientLog := &zapLogger{log: m.log, module: "whatsmeow"}
	var container *sqlstore.Container
	var deviceStore *store.Device
	var err error

	if m.storageDriver == "postgres" && m.pgConnString != "" {
		m.log.Info("tentando restaurar sessão do PostgreSQL",
			zap.String("instance_id", instanceID),
		)

		if m.instanceRepo == nil {
			m.log.Error("instanceRepo não configurado para restauração PostgreSQL",
				zap.String("instance_id", instanceID),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		inst, err := m.instanceRepo.GetByID(ctx, instanceID)
		if err != nil {
			m.log.Error("erro ao buscar instância para restauração",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		if inst.WhatsAppJID == "" {
			m.log.Warn("instância sem JID salvo, não é possível restaurar sessão PostgreSQL",
				zap.String("instance_id", instanceID),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		if m.sharedContainer == nil {
			m.log.Error("container PostgreSQL não incializado",
				zap.String("instance_id", instanceID),
				zap.Bool("has_conn_string", m.pgConnString != ""),
			)
			return nil, fmt.Errorf("sessão não encontrada (DB error)")
		}

		container = m.sharedContainer

		jid, err := types.ParseJID(inst.WhatsAppJID)
		if err != nil {
			m.log.Error("erro ao parsear JID para restauração",
				zap.String("instance_id", instanceID),
				zap.String("jid", inst.WhatsAppJID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		deviceStore, err = container.GetDevice(ctx, jid)
		if err != nil {
			m.log.Error("erro ao buscar device por JID",
				zap.String("instance_id", instanceID),
				zap.String("jid", inst.WhatsAppJID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		if deviceStore == nil {
			m.log.Warn("device não encontrado no PostgreSQL",
				zap.String("instance_id", instanceID),
				zap.String("jid", inst.WhatsAppJID),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		m.log.Debug("device encontrado no PostgreSQL",
			zap.String("instance_id", instanceID),
			zap.String("jid", inst.WhatsAppJID),
		)
	} else {
		dbPath := filepath.Join(m.baseDir, instanceID+".db")

		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("sessão não encontrada")
		}

		m.log.Info("tentando restaurar sessão do SQLite",
			zap.String("instance_id", instanceID),
			zap.String("db_path", dbPath),
		)

		sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
		container, err = sqlstore.New(ctx, "sqlite3", sqlitePath, clientLog)
		if err != nil {
			m.log.Error("erro ao criar container do SQLite",
				zap.String("instance_id", instanceID),
				zap.String("db_path", dbPath),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

		deviceStore, err = container.GetFirstDevice(ctx)
		if err != nil {
			m.log.Error("erro ao obter device store",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}
	}

	if deviceStore.ID == nil || deviceStore.ID.IsEmpty() {
		m.log.Warn("sessão não está logada (sem JID)",
			zap.String("instance_id", instanceID),
			zap.String("storage", m.storageDriver),
		)
		return nil, fmt.Errorf("sessão não encontrada")
	}

	m.log.Debug("device store obtido com sucesso",
		zap.String("instance_id", instanceID),
		zap.String("jid", deviceStore.ID.String()),
		zap.String("push_name", deviceStore.PushName),
	)

	if cleanup := m.applyDeviceConfig(instanceID, deviceStore); cleanup != nil {
		cleanup()
	}

	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AutomaticMessageRerequestFromPhone = true

	client.GetMessageForRetry = m.getMessageForRetryCallback(instanceID)

	client.AddEventHandler(func(evt any) {
		m.handleEvent(instanceID, evt)
	})

	m.log.Debug("conectando cliente restaurado", zap.String("instance_id", instanceID))
	var connectErr error
	for i := 0; i < 3; i++ {
		connectErr = client.Connect()
		if connectErr == nil {
			break
		}
		m.log.Warn("falha na tentativa de conexão do cliente restaurado",
			zap.String("instance_id", instanceID),
			zap.Int("tentativa", i+1),
			zap.Error(connectErr))

		if i < 2 {
			time.Sleep(2 * time.Second)
		}
	}

	if connectErr != nil {
		m.log.Error("erro ao conectar cliente restaurado após retentativas",
			zap.String("instance_id", instanceID),
			zap.Error(connectErr),
		)
		return nil, fmt.Errorf("erro ao restaurar sessão após 3 tentativas: %w", connectErr)
	}

	m.log.Debug("cliente conectado, verificando se está logado",
		zap.String("instance_id", instanceID),
	)

	time.Sleep(1 * time.Second)
	isLoggedIn := client.IsLoggedIn()
	m.log.Debug("verificação inicial de login",
		zap.String("instance_id", instanceID),
		zap.Bool("is_logged_in", isLoggedIn),
	)

	if !isLoggedIn {
		m.log.Warn("cliente restaurado mas não está logado ainda, aguardando...",
			zap.String("instance_id", instanceID),
		)

		time.Sleep(2 * time.Second)
		isLoggedIn = client.IsLoggedIn()
		m.log.Debug("verificação após espera",
			zap.String("instance_id", instanceID),
			zap.Bool("is_logged_in", isLoggedIn),
		)
		if !isLoggedIn {
			m.log.Error("cliente restaurado não conseguiu fazer login após espera",
				zap.String("instance_id", instanceID),
			)

			if m.instanceRepo != nil {
				inst, err := m.instanceRepo.GetByID(ctx, instanceID)
				if err == nil {
					inst.Status = model.InstanceStatusError
					_, _ = m.instanceRepo.Update(ctx, inst)
					m.log.Info("status atualizado para error no banco",
						zap.String("instance_id", instanceID),
					)
				}
			}
			return nil, fmt.Errorf("sessão não está logada")
		}
	}

	m.clients[instanceID] = client

	go func() {
		for attempt := 1; attempt <= 5; attempt++ {
			time.Sleep(time.Duration(attempt*2) * time.Second)
			if !client.IsLoggedIn() {
				return
			}
			if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
				if attempt == 5 {
					m.log.Debug("PushName não sincronizado na restauração", zap.String("instance_id", instanceID))
				}
			} else {
				m.log.Info("presence enviado com sucesso", zap.String("instance_id", instanceID))

				m.mu.Lock()
				m.sessionReady[instanceID] = true
				m.mu.Unlock()
				m.log.Info("sessão restaurada e pronta para mensagens", zap.String("instance_id", instanceID))
				return
			}
		}

		m.mu.Lock()
		m.sessionReady[instanceID] = true
		m.mu.Unlock()
		m.log.Warn("sessão restaurada marcada como pronta após falha no presence", zap.String("instance_id", instanceID))
	}()

	m.log.Info("sessão restaurada com sucesso", zap.String("instance_id", instanceID), zap.Bool("is_logged_in", client.IsLoggedIn()))
	return client, nil
}

func (m *Manager) RestoreAllSessions(ctx context.Context, instanceIDs []string) {
	m.log.Info("iniciando restauração escalonada de sessões",
		zap.Int("total_instances", len(instanceIDs)),
		zap.String("storage", m.storageDriver),
	)

	var toRestore []string
	for _, instanceID := range instanceIDs {
		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()

		if exists && client != nil && client.IsLoggedIn() {
			m.log.Debug("sessão já está em memória e logada, pulando",
				zap.String("instance_id", instanceID),
			)
			continue
		}

		if m.storageDriver != "postgres" {
			dbPath := filepath.Join(m.baseDir, instanceID+".db")
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				m.log.Debug("arquivo SQLite não existe, pulando",
					zap.String("instance_id", instanceID),
				)
				continue
			}
		}

		toRestore = append(toRestore, instanceID)
	}

	if len(toRestore) == 0 {
		m.log.Info("nenhuma sessão para restaurar")
		return
	}

	const batchSize = 5
	totalBatches := (len(toRestore) + batchSize - 1) / batchSize

	go func() {
		for i := 0; i < len(toRestore); i += batchSize {
			end := i + batchSize
			if end > len(toRestore) {
				end = len(toRestore)
			}

			batch := toRestore[i:end]
			batchNum := i/batchSize + 1

			m.log.Info("restaurando lote de sessões",
				zap.Int("batch", batchNum),
				zap.Int("total_batches", totalBatches),
				zap.Int("sessions_in_batch", len(batch)),
			)

			for _, id := range batch {
				go func(instanceID string) {
					client, err := m.restoreSessionIfExists(ctx, instanceID)
					if err != nil {
						m.log.Debug("não foi possível restaurar sessão na inicialização",
							zap.String("instance_id", instanceID),
							zap.Error(err),
						)
						return
					}
					if client != nil && client.IsLoggedIn() {
						m.log.Info("sessão restaurada com sucesso",
							zap.String("instance_id", instanceID),
						)
					}
				}(id)
			}

			if end < len(toRestore) {
				delay := 10 + rand.Intn(21) // 10-30 seconds
				m.log.Info("aguardando antes do próximo lote",
					zap.Int("next_batch", batchNum+1),
					zap.Int("delay_seconds", delay),
				)
				time.Sleep(time.Duration(delay) * time.Second)
			}
		}

		m.log.Info("restauração escalonada concluída",
			zap.Int("total_restored", len(toRestore)),
			zap.Int("total_batches", totalBatches),
		)
	}()

	m.log.Info("restauração escalonada iniciada em background",
		zap.Int("sessions", len(toRestore)),
		zap.Int("batches", totalBatches),
	)
}

func (m *Manager) SaveSessionBlob(instanceID string) ([]byte, error) {
	m.mu.RLock()
	_, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("sessão não encontrada")
	}

	data := []byte(fmt.Sprintf("session:%s", instanceID))

	encrypted, err := crypto.Encrypt(data, m.encKey)
	if err != nil {
		return nil, fmt.Errorf("whatsmeow: criptografar: %w", err)
	}

	return encrypted, nil
}
