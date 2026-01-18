package whatsmeow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/lib/pq"           // PostgreSQL driver for WhatsMeow sessions
	_ "github.com/mattn/go-sqlite3" // SQLite driver for WhatsMeow sessions
	"go.mau.fi/whatsmeow"
	waCompanionReg "go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/pkg/crypto"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

type noopLogger struct{}

func (n *noopLogger) Debugf(msg string, args ...interface{}) {}
func (n *noopLogger) Infof(msg string, args ...interface{})  {}
func (n *noopLogger) Warnf(msg string, args ...interface{})  {}
func (n *noopLogger) Errorf(msg string, args ...interface{}) {}
func (n *noopLogger) Sub(module string) waLog.Logger         { return n }

var (
	deviceConfigMu sync.Mutex
)

type EventHandler interface {
	Handle(ctx context.Context, instanceID string, instanceJID string, client *whatsmeow.Client, evt any)
}

type Manager struct {
	clients          map[string]*whatsmeow.Client
	currentQRs       map[string]string
	qrContexts       map[string]context.CancelFunc
	pairingSuccess   map[string]time.Time
	mu               sync.RWMutex
	log              *zap.Logger
	encKey           string
	storageDriver    string
	baseDir          string
	pgConnString     string
	deviceConfigRepo storage.DeviceConfigRepository
	instanceRepo     storage.InstanceRepository
	historySyncRepo  storage.HistorySyncRepository
	onStatusChange   func(instanceID string, status string)
	eventHandler     EventHandler
	syncWorkers      map[string]context.CancelFunc
}

func NewManager(log *zap.Logger, encKey, storageDriver, baseDir, pgConnString string, deviceConfigRepo storage.DeviceConfigRepository, instanceRepo storage.InstanceRepository, historySyncRepo storage.HistorySyncRepository) *Manager {
	if storageDriver != "postgres" {
		if baseDir == "" {
			baseDir = "/app/data/sessions"
			log.Warn("sessionDir não definido, usando diretório padrão do container", zap.String("dir", baseDir))
		} else {
			log.Info("Usando diretório de sessões configurado", zap.String("dir", baseDir))
		}
		os.MkdirAll(baseDir, 0755)
	}

	return &Manager{
		clients:          make(map[string]*whatsmeow.Client),
		currentQRs:       make(map[string]string),
		qrContexts:       make(map[string]context.CancelFunc),
		pairingSuccess:   make(map[string]time.Time),
		log:              log,
		encKey:           encKey,
		storageDriver:    storageDriver,
		baseDir:          baseDir,
		pgConnString:     pgConnString,
		deviceConfigRepo: deviceConfigRepo,
		instanceRepo:     instanceRepo,
		historySyncRepo:  historySyncRepo,
		syncWorkers:      make(map[string]context.CancelFunc),
	}
}

// SetStatusChangeCallback registra o callback de status de instância.
func (m *Manager) SetStatusChangeCallback(fn func(instanceID string, status string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStatusChange = fn
}

// SetEventHandler registra o handler de eventos (ex.: webhooks).
func (m *Manager) SetEventHandler(handler EventHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventHandler = handler
	m.log.Info("event handler configurado para webhooks")
}

// SessionStorageInfo contains information about session storage for dashboard display.
type SessionStorageInfo struct {
	Type     string // "sqlite" or "postgres"
	Location string // file path for SQLite, table name for PostgreSQL
}

// GetSessionStorageInfo returns information about session storage for an instance.
func (m *Manager) GetSessionStorageInfo(instanceID string) SessionStorageInfo {
	if m.storageDriver == "postgres" && m.pgConnString != "" {
		return SessionStorageInfo{
			Type:     "postgres",
			Location: "PostgreSQL",
		}
	}
	dbPath := filepath.Join(m.baseDir, instanceID+".db")
	return SessionStorageInfo{
		Type:     "sqlite",
		Location: dbPath,
	}
}

func (m *Manager) CreateSession(ctx context.Context, instanceID string) (string, error) {
	return m.createSession(ctx, instanceID, false)
}

func (m *Manager) createSession(ctx context.Context, instanceID string, forceRecreate bool) (string, error) {
	m.mu.Lock()

	if existingClient, exists := m.clients[instanceID]; exists {
		if !forceRecreate {
			m.mu.Unlock()
			m.log.Warn("tentativa de criar sessão que já existe", zap.String("instance_id", instanceID))
			return "", fmt.Errorf("sessão já existe para instância %s", instanceID)
		}
		m.log.Info("desconectando sessão existente para recriar", zap.String("instance_id", instanceID))
		existingClient.Disconnect()
		delete(m.clients, instanceID)
	}

	m.mu.Unlock()

	m.log.Info("criando nova sessão WhatsMeow", zap.String("instance_id", instanceID))

	clientLog := &noopLogger{}
	var container *sqlstore.Container
	var deviceStore *store.Device
	var err error

	if m.storageDriver == "postgres" && m.pgConnString != "" {
		m.log.Debug("criando store PostgreSQL", zap.String("instance_id", instanceID))
		container, err = sqlstore.New(ctx, "postgres", m.pgConnString, clientLog)
		if err != nil {
			m.log.Error("erro ao criar store PostgreSQL", zap.String("instance_id", instanceID), zap.Error(err))
			return "", fmt.Errorf("whatsmeow: criar store PostgreSQL: %w", err)
		}

		deviceStore = container.NewDevice()
		m.log.Debug("novo device criado para PostgreSQL", zap.String("instance_id", instanceID))
	} else {
		dbPath := filepath.Join(m.baseDir, instanceID+".db")
		if _, err := os.Stat(dbPath); err == nil {
			sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
			container, err = sqlstore.New(ctx, "sqlite3", sqlitePath, clientLog)
			if err == nil {
				deviceStore, err := container.GetFirstDevice(ctx)
				if err == nil && deviceStore.ID != nil && !deviceStore.ID.IsEmpty() {
					m.log.Info("arquivo SQLite com sessão válida encontrado, tentando restaurar",
						zap.String("instance_id", instanceID),
					)
					client, err := m.restoreSessionIfExists(ctx, instanceID)
					if err == nil && client != nil && client.IsLoggedIn() {
						return "", fmt.Errorf("instância já conectada, não é necessário QR code")
					}
					m.log.Warn("restauração falhou, deletando arquivo SQLite e criando nova sessão",
						zap.String("instance_id", instanceID),
					)
					_ = os.Remove(dbPath)
				}
			}
		}

		dbPath = filepath.Join(m.baseDir, instanceID+".db")
		sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
		m.log.Debug("criando store SQLite", zap.String("instance_id", instanceID), zap.String("db_path", sqlitePath))
		container, err = sqlstore.New(ctx, "sqlite3", sqlitePath, clientLog)
		if err != nil {
			m.log.Error("erro ao criar store", zap.String("instance_id", instanceID), zap.Error(err))
			return "", fmt.Errorf("whatsmeow: criar store: %w", err)
		}

		deviceStore, err = container.GetFirstDevice(ctx)
		if err != nil {
			m.log.Error("erro ao obter device store", zap.String("instance_id", instanceID), zap.Error(err))
			return "", fmt.Errorf("whatsmeow: obter device: %w", err)
		}

		if deviceStore.ID != nil && !deviceStore.ID.IsEmpty() {
			m.log.Warn("deviceStore já tem JID, tentando restaurar ao invés de criar nova sessão",
				zap.String("instance_id", instanceID),
				zap.String("jid", deviceStore.ID.String()),
			)
			client, err := m.restoreSessionIfExists(ctx, instanceID)
			if err == nil && client != nil && client.IsLoggedIn() {
				return "", fmt.Errorf("instância já conectada, não é necessário QR code")
			}

			dbPath := filepath.Join(m.baseDir, instanceID+".db")
			m.log.Warn("restauração falhou, deletando arquivo SQLite e criando nova sessão",
				zap.String("instance_id", instanceID),
			)
			_ = os.Remove(dbPath)
			sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
			container, err = sqlstore.New(ctx, "sqlite3", sqlitePath, clientLog)
			if err != nil {
				m.log.Error("erro ao recriar store após deletar", zap.String("instance_id", instanceID), zap.Error(err))
				return "", fmt.Errorf("whatsmeow: recriar store: %w", err)
			}
			deviceStore, err = container.GetFirstDevice(ctx)
			if err != nil {
				m.log.Error("erro ao obter device store após recriar", zap.String("instance_id", instanceID), zap.Error(err))
				return "", fmt.Errorf("whatsmeow: obter device: %w", err)
			}
		}
	}

	if err := m.applyDeviceConfig(ctx, deviceStore); err != nil {
		m.log.Warn("erro ao aplicar configurações de dispositivo", zap.String("instance_id", instanceID), zap.Error(err))
	}

	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.EnableAutoReconnect = true
	client.ManualHistorySyncDownload = true

	client.AddEventHandler(func(evt any) {
		m.handleEvent(instanceID, evt)
	})

	qrCtx, qrCancel := context.WithCancel(context.Background())
	qrChan, err := client.GetQRChannel(qrCtx)
	if err != nil {
		qrCancel()
		m.log.Error("erro ao obter canal QR", zap.String("instance_id", instanceID), zap.Error(err))
		return "", fmt.Errorf("whatsmeow: obter canal QR: %w", err)
	}

	m.log.Debug("conectando cliente WhatsMeow", zap.String("instance_id", instanceID))
	err = client.Connect()
	if err != nil {
		qrCancel()
		m.log.Error("erro ao conectar cliente", zap.String("instance_id", instanceID), zap.Error(err))
		return "", fmt.Errorf("whatsmeow: conectar: %w", err)
	}

	m.mu.Lock()
	m.clients[instanceID] = client
	m.qrContexts[instanceID] = qrCancel
	m.mu.Unlock()

	go m.monitorQRChannel(instanceID, client, qrChan, qrCancel)

	m.log.Info("cliente conectado, aguardando QR code", zap.String("instance_id", instanceID))

	maxWait := 30 * time.Second
	checkInterval := 500 * time.Millisecond
	startTime := time.Now()

	for {
		m.mu.RLock()
		currentQR, hasQR := m.currentQRs[instanceID]
		_, hasQRContext := m.qrContexts[instanceID]
		m.mu.RUnlock()

		if hasQR && currentQR != "" {
			m.log.Info("QR code gerado com sucesso", zap.String("instance_id", instanceID))
			return currentQR, nil
		}

		if !hasQRContext {
			m.log.Warn("canal QR foi fechado antes de receber código", zap.String("instance_id", instanceID))
			m.mu.RLock()
			currentQR, hasQR = m.currentQRs[instanceID]
			m.mu.RUnlock()
			if hasQR && currentQR != "" {
				return currentQR, nil
			}
			return "", fmt.Errorf("whatsmeow: canal QR fechado antes de receber código")
		}

		if time.Since(startTime) > maxWait {
			m.log.Error("timeout ao aguardar primeiro QR code", zap.String("instance_id", instanceID))
			m.mu.RLock()
			currentQR, hasQR = m.currentQRs[instanceID]
			m.mu.RUnlock()
			if hasQR && currentQR != "" {
				return currentQR, nil
			}
			return "", fmt.Errorf("whatsmeow: timeout ao aguardar QR code")
		}

		time.Sleep(checkInterval)
	}
}

func (m *Manager) monitorQRChannel(instanceID string, client *whatsmeow.Client, qrChan <-chan whatsmeow.QRChannelItem, cancel context.CancelFunc) {
	defer cancel()

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			if evt.Code != "" {
				m.mu.Lock()
				m.currentQRs[instanceID] = evt.Code
				m.mu.Unlock()
				m.log.Info("QR code recebido e armazenado", zap.String("instance_id", instanceID), zap.Duration("timeout", evt.Timeout))
			} else {
				m.log.Warn("QR code recebido mas vazio", zap.String("instance_id", instanceID))
			}

		case "success":
			m.log.Info("pareamento concluído com sucesso", zap.String("instance_id", instanceID))

			m.mu.Lock()
			m.pairingSuccess[instanceID] = time.Now()
			m.mu.Unlock()

			if client != nil && client.Store != nil && client.Store.ID != nil {
				go func() {
					if m.instanceRepo != nil {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()

						inst, err := m.instanceRepo.GetByID(ctx, instanceID)
						if err != nil {
							m.log.Error("erro ao buscar instância para salvar JID",
								zap.String("instance_id", instanceID),
								zap.Error(err),
							)
							return
						}

						inst.WhatsAppJID = client.Store.ID.String()
						_, err = m.instanceRepo.Update(ctx, inst)
						if err != nil {
							m.log.Error("erro ao salvar JID da instância",
								zap.String("instance_id", instanceID),
								zap.String("jid", inst.WhatsAppJID),
								zap.Error(err),
							)
						} else {
							m.log.Info("JID salvo com sucesso",
								zap.String("instance_id", instanceID),
								zap.String("jid", inst.WhatsAppJID),
							)
						}
					}
				}()
			}

			go m.initHistorySyncCycle(instanceID)
			if client != nil {
				go func() {
					for attempt := 1; attempt <= 5; attempt++ {
						time.Sleep(time.Duration(attempt*2) * time.Second)
						if !client.IsLoggedIn() {
							return
						}
						if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
							if attempt < 5 {
								m.log.Debug("aguardando PushName para enviar presence",
									zap.String("instance_id", instanceID),
									zap.Int("attempt", attempt),
								)
							}
						} else {
							m.log.Info("presence enviado com sucesso", zap.String("instance_id", instanceID))
							return
						}
					}
				}()

				go func() {
					time.Sleep(10 * time.Second)
					m.mu.RLock()
					cli, ok := m.clients[instanceID]
					callback := m.onStatusChange
					m.mu.RUnlock()

					if !ok || cli == nil {
						m.log.Warn("verificação pós-pareamento: cliente não encontrado",
							zap.String("instance_id", instanceID))
						if callback != nil {
							callback(instanceID, "error")
						}
						return
					}

					if !cli.IsLoggedIn() {
						m.log.Warn("verificação pós-pareamento: cliente não está logado",
							zap.String("instance_id", instanceID))
						if callback != nil {
							callback(instanceID, "error")
						}
					} else {
						m.log.Info("verificação pós-pareamento: conexão confirmada",
							zap.String("instance_id", instanceID))
					}
				}()
			}
		}
	}
}

func (m *Manager) GetQR(ctx context.Context, instanceID string) (string, error) {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	currentQR, hasQR := m.currentQRs[instanceID]
	_, hasQRContext := m.qrContexts[instanceID]
	m.mu.RUnlock()

	if hasQR && currentQR != "" {
		return currentQR, nil
	}

	if exists && client != nil {
		if client.IsLoggedIn() {
			return "", fmt.Errorf("instância já conectada, não é possível gerar QR code")
		}

		if hasQRContext {
			m.log.Debug("sessão sendo criada, aguardando QR code",
				zap.String("instance_id", instanceID),
			)
			select {
			case <-time.After(5 * time.Second):
				m.mu.RLock()
				currentQR, hasQR = m.currentQRs[instanceID]
				m.mu.RUnlock()
				if hasQR && currentQR != "" {
					return currentQR, nil
				}
				return "", fmt.Errorf("QR code ainda não disponível, aguarde alguns segundos")
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		m.mu.RLock()
		pairingTime, hasPairing := m.pairingSuccess[instanceID]
		m.mu.RUnlock()

		if hasPairing && time.Since(pairingTime) < 30*time.Second {
			m.log.Info("pareamento recente detectado, aguardando conexão",
				zap.String("instance_id", instanceID),
				zap.Duration("since_pairing", time.Since(pairingTime)),
			)
			return "", fmt.Errorf("pareamento em andamento, aguarde a conexão ser estabelecida")
		}

		m.log.Info("cliente existe mas não está logado e sem QR ativo, desconectando para recriar sessão",
			zap.String("instance_id", instanceID),
		)
		client.Disconnect()
		m.mu.Lock()
		delete(m.clients, instanceID)
		delete(m.currentQRs, instanceID)
		delete(m.pairingSuccess, instanceID)
		if cancel, exists := m.qrContexts[instanceID]; exists {
			cancel()
			delete(m.qrContexts, instanceID)
		}
		m.mu.Unlock()
		if m.storageDriver != "postgres" {
			dbPath := filepath.Join(m.baseDir, instanceID+".db")
			if _, err := os.Stat(dbPath); err == nil {
				m.log.Info("deletando arquivo SQLite inválido antes de criar nova sessão",
					zap.String("instance_id", instanceID),
				)
				_ = os.Remove(dbPath)
			}
		}
		return m.CreateSession(ctx, instanceID)
	}

	dbPath := filepath.Join(m.baseDir, instanceID+".db")
	if _, err := os.Stat(dbPath); err == nil {
		client, err := m.restoreSessionIfExists(ctx, instanceID)
		if err == nil && client != nil && client.IsLoggedIn() {
			return "", fmt.Errorf("instância já conectada, não é necessário QR code")
		}
		m.log.Info("sessão SQLite inválida ou expirada, deletando e criando nova",
			zap.String("instance_id", instanceID),
		)
		_ = os.Remove(dbPath)
	}

	return m.CreateSession(ctx, instanceID)
}

func (m *Manager) RestoreSession(ctx context.Context, instanceID string, encryptedBlob []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	_, err := crypto.Decrypt(encryptedBlob, m.encKey)
	if err != nil {
		return fmt.Errorf("whatsmeow: descriptografar: %w", err)
	}

	clientLog := &noopLogger{}
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

// Disconnect encerra a sessão atual removendo também o store associado.
func (m *Manager) Disconnect(instanceID string) error {
	return m.DeleteSession(instanceID)
}

func (m *Manager) DeleteSession(instanceID string) error {
	var client *whatsmeow.Client

	m.mu.Lock()
	if cancel, exists := m.qrContexts[instanceID]; exists {
		cancel()
		delete(m.qrContexts, instanceID)
	}
	if c, exists := m.clients[instanceID]; exists {
		client = c
		delete(m.clients, instanceID)
	}
	delete(m.currentQRs, instanceID)
	m.mu.Unlock()

	// Se o cliente não estiver em memória, tentar restaurar para realizar o logout
	if client == nil {
		m.log.Debug("cliente não encontrado em memória, tentando restaurar para logout", zap.String("instance_id", instanceID))
		if restoredClient, err := m.restoreSessionIfExists(context.Background(), instanceID); err == nil && restoredClient != nil {
			client = restoredClient
			// Remove do map novamente pois restoreSessionIfExists adiciona
			m.mu.Lock()
			delete(m.clients, instanceID)
			m.mu.Unlock()
		}
	}

	if client != nil {
		m.logoutClient(instanceID, client)
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

	container, err := sqlstore.New(ctx, "postgres", m.pgConnString, &noopLogger{})
	if err != nil {
		return fmt.Errorf("whatsmeow: criar container PostgreSQL: %w", err)
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("whatsmeow: parse device JID: %w", err)
	}

	store, err := container.GetDevice(ctx, jid)
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
		// Verificar se o cliente está realmente logado
		isLoggedIn := client.IsLoggedIn()
		if !isLoggedIn {
			m.log.Warn("cliente existe mas não está logado, verificando status de pareamento",
				zap.String("instance_id", instanceID),
			)

			// Verificar se há contexto de QR ativo ou pareamento recente
			m.mu.RLock()
			_, hasQR := m.qrContexts[instanceID]
			pairingTime, hasPairing := m.pairingSuccess[instanceID]
			m.mu.RUnlock()

			// Se tiver QR ativo ou pareamento recente (menos de 60s), não deleta
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
			// Remover do map e tentar restaurar
			m.mu.Lock()
			delete(m.clients, instanceID)
			m.mu.Unlock()
			// Tentar restaurar novamente
			return m.restoreSessionIfExists(context.Background(), instanceID)
		}
		m.log.Debug("cliente está logado e pronto",
			zap.String("instance_id", instanceID),
		)
		return client, nil
	}

	m.log.Debug("cliente não encontrado no map, tentando restaurar do SQLite",
		zap.String("instance_id", instanceID),
	)

	// Tentar restaurar sessão do SQLite
	return m.restoreSessionIfExists(context.Background(), instanceID)
}

// restoreSessionIfExists tenta restaurar uma sessão existente do SQLite ou PostgreSQL
func (m *Manager) restoreSessionIfExists(ctx context.Context, instanceID string) (*whatsmeow.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Verificar novamente após lock (pode ter sido adicionado por outra goroutine)
	if client, exists := m.clients[instanceID]; exists {
		return client, nil
	}

	clientLog := &noopLogger{}
	var container *sqlstore.Container
	var deviceStore *store.Device
	var err error

	// Use PostgreSQL or SQLite based on storage driver
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

		container, err = sqlstore.New(ctx, "postgres", m.pgConnString, clientLog)
		if err != nil {
			m.log.Error("erro ao criar container PostgreSQL",
				zap.String("instance_id", instanceID),
				zap.Error(err),
			)
			return nil, fmt.Errorf("sessão não encontrada")
		}

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

		// Verificar se o arquivo existe
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

	// Verificar se a sessão está logada (tem JID)
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

	// Aplicar configurações de dispositivo ANTES de criar o client
	if err := m.applyDeviceConfig(ctx, deviceStore); err != nil {
		m.log.Warn("erro ao aplicar configurações de dispositivo", zap.String("instance_id", instanceID), zap.Error(err))
		// Continuar mesmo com erro (usar valores padrão)
	}

	client := whatsmeow.NewClient(deviceStore, clientLog)

	// Adicionar event handler
	client.AddEventHandler(func(evt any) {
		m.handleEvent(instanceID, evt)
	})

	// Conectar
	m.log.Debug("conectando cliente restaurado", zap.String("instance_id", instanceID))
	err = client.Connect()
	if err != nil {
		m.log.Error("erro ao conectar cliente restaurado",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return nil, fmt.Errorf("erro ao restaurar sessão: %w", err)
	}

	m.log.Debug("cliente conectado, verificando se está logado",
		zap.String("instance_id", instanceID),
	)

	// Aguardar um pouco para garantir que a conexão foi estabelecida
	// e verificar se está realmente logado
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
		// Não retornar erro ainda - pode estar conectando
		// Aguardar mais um pouco
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
			// Atualizar status no banco se tiver instanceRepo
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

	// Adicionar ao map
	m.clients[instanceID] = client

	// Enviar presence após conexão bem-sucedida (com retry)
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
				return
			}
		}
	}()

	m.log.Info("sessão restaurada com sucesso", zap.String("instance_id", instanceID), zap.Bool("is_logged_in", client.IsLoggedIn()))
	return client, nil
}

// RestoreAllSessions tenta restaurar todas as sessões que têm arquivo SQLite válido
// independente do status no banco de dados
func (m *Manager) RestoreAllSessions(ctx context.Context, instanceIDs []string) {
	m.log.Info("iniciando restauração de todas as sessões",
		zap.Int("total_instances", len(instanceIDs)),
		zap.String("storage", m.storageDriver),
	)

	for _, instanceID := range instanceIDs {
		// Verificar se já existe em memória
		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()

		if exists && client != nil && client.IsLoggedIn() {
			m.log.Debug("sessão já está em memória e logada, pulando",
				zap.String("instance_id", instanceID),
			)
			continue
		}

		// Para SQLite, verificar se existe arquivo
		if m.storageDriver != "postgres" {
			dbPath := filepath.Join(m.baseDir, instanceID+".db")
			if _, err := os.Stat(dbPath); os.IsNotExist(err) {
				m.log.Debug("arquivo SQLite não existe, pulando",
					zap.String("instance_id", instanceID),
				)
				continue
			}
		}
		// Para PostgreSQL, sempre tenta restaurar (a sessão pode existir no banco)

		// Tentar restaurar em background (não bloquear a inicialização)
		go func(id string) {
			client, err := m.restoreSessionIfExists(ctx, id)
			if err != nil {
				m.log.Debug("não foi possível restaurar sessão na inicialização",
					zap.String("instance_id", id),
					zap.Error(err),
				)
				// O callback será chamado pelo handleEvent se houver erro
				return
			}

			if client != nil && client.IsLoggedIn() {
				m.log.Info("sessão restaurada com sucesso na inicialização",
					zap.String("instance_id", id),
				)
				// O callback será chamado automaticamente pelo handleEvent
			}
		}(instanceID)
	}

	m.log.Info("restauração de sessões iniciada em background")
}

// DiagnosticsInfo contém informações de diagnóstico sobre uma instância
type DiagnosticsInfo struct {
	InstanceID           string     `json:"instanceId"`
	HasClientInMemory    bool       `json:"hasClientInMemory"`
	IsLoggedIn           bool       `json:"isLoggedIn"`
	HasSQLiteFile        bool       `json:"hasSQLiteFile"`
	SQLiteFilePath       string     `json:"sqliteFilePath"`
	SQLiteFileSize       int64      `json:"sqliteFileSize"`
	SQLiteFileModTime    time.Time  `json:"sqliteFileModTime"`
	StorageType          string     `json:"storageType"`
	StorageLocation      string     `json:"storageLocation"`
	HasDeviceStore       bool       `json:"hasDeviceStore"`
	DeviceJID            string     `json:"deviceJid"`
	DevicePushName       string     `json:"devicePushName"`
	HasQRCode            bool       `json:"hasQRCode"`
	LastError            string     `json:"lastError,omitempty"`
	ClientConnected      bool       `json:"clientConnected"`
	HistorySyncStatus    string     `json:"historySyncStatus,omitempty"`
	HistorySyncCycleID   string     `json:"historySyncCycleId,omitempty"`
	HistorySyncUpdatedAt *time.Time `json:"historySyncUpdatedAt,omitempty"`
	PendingPayloads      int        `json:"pendingPayloads"`
}

// GetDiagnostics retorna informações de diagnóstico sobre uma instância
func (m *Manager) GetDiagnostics(instanceID string) interface{} {
	diag := DiagnosticsInfo{
		InstanceID: instanceID,
	}

	m.mu.RLock()
	client, hasClient := m.clients[instanceID]
	hasQR := m.currentQRs[instanceID] != ""
	m.mu.RUnlock()

	diag.HasClientInMemory = hasClient
	diag.HasQRCode = hasQR

	if hasClient {
		diag.IsLoggedIn = client.IsLoggedIn()
		diag.ClientConnected = client.IsLoggedIn()
	}

	// Set storage info
	storageInfo := m.GetSessionStorageInfo(instanceID)
	diag.StorageType = storageInfo.Type
	diag.StorageLocation = storageInfo.Location

	// Buscar informações da instância (history sync e JID)
	var inst *model.Instance
	if m.instanceRepo != nil {
		ctx := context.Background()
		if i, err := m.instanceRepo.GetByID(ctx, instanceID); err == nil {
			inst = &i
			diag.HistorySyncStatus = string(inst.HistorySyncStatus)
			diag.HistorySyncCycleID = inst.HistorySyncCycleID
			diag.HistorySyncUpdatedAt = inst.HistorySyncUpdatedAt
		}
	}

	// Verificar device store baseado no driver de storage
	if m.storageDriver == "postgres" && m.pgConnString != "" {
		// PostgreSQL: usar JID salvo na instância
		if inst != nil && inst.WhatsAppJID != "" {
			diag.DeviceJID = inst.WhatsAppJID
			diag.HasDeviceStore = true

			ctx := context.Background()
			container, err := sqlstore.New(ctx, "postgres", m.pgConnString, &noopLogger{})
			if err == nil {
				jid, err := types.ParseJID(inst.WhatsAppJID)
				if err == nil {
					deviceStore, err := container.GetDevice(ctx, jid)
					if err == nil && deviceStore != nil {
						diag.DevicePushName = deviceStore.PushName
					}
				}
			}
		}
	} else {
		// SQLite: verificar arquivo de sessão
		dbPath := filepath.Join(m.baseDir, instanceID+".db")
		if fileInfo, err := os.Stat(dbPath); err == nil {
			diag.HasSQLiteFile = true
			diag.SQLiteFilePath = dbPath
			diag.SQLiteFileSize = fileInfo.Size()
			diag.SQLiteFileModTime = fileInfo.ModTime()

			ctx := context.Background()
			sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
			container, err := sqlstore.New(ctx, "sqlite3", sqlitePath, &noopLogger{})
			if err == nil {
				deviceStore, err := container.GetFirstDevice(ctx)
				if err == nil {
					diag.HasDeviceStore = true
					if deviceStore.ID != nil && !deviceStore.ID.IsEmpty() {
						diag.DeviceJID = deviceStore.ID.String()
					}
					diag.DevicePushName = deviceStore.PushName
				}
			}
		}
	}

	if m.historySyncRepo != nil {
		ctx := context.Background()
		if payloads, err := m.historySyncRepo.ListPendingByInstance(ctx, instanceID); err == nil {
			diag.PendingPayloads = len(payloads)
		}
	}

	return diag
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

// mapPlatformType mapeia string do banco para enum do WhatsMeow
func mapPlatformType(platformType string) *waCompanionReg.DeviceProps_PlatformType {
	switch platformType {
	case "CHROME":
		return waCompanionReg.DeviceProps_CHROME.Enum()
	case "FIREFOX":
		return waCompanionReg.DeviceProps_FIREFOX.Enum()
	case "SAFARI":
		return waCompanionReg.DeviceProps_SAFARI.Enum()
	case "EDGE":
		return waCompanionReg.DeviceProps_EDGE.Enum()
	case "IPAD":
		return waCompanionReg.DeviceProps_IPAD.Enum()
	case "ANDROID_PHONE":
		return waCompanionReg.DeviceProps_ANDROID_PHONE.Enum()
	case "IOS_PHONE":
		return waCompanionReg.DeviceProps_IOS_PHONE.Enum()
	case "DESKTOP":
		return waCompanionReg.DeviceProps_DESKTOP.Enum()
	default:
		return waCompanionReg.DeviceProps_DESKTOP.Enum()
	}
}

func (m *Manager) applyDeviceConfig(ctx context.Context, deviceStore *store.Device) error {
	if m.deviceConfigRepo == nil {
		return nil
	}

	config, err := m.deviceConfigRepo.Get(ctx)
	if err != nil {
		m.log.Warn("erro ao buscar configurações, usando padrão", zap.Error(err))
		return nil
	}

	if deviceStore.ID == nil || deviceStore.ID.IsEmpty() {
		m.applyDevicePropsForRegistration(ctx, config)
	}

	m.log.Info("configurações de dispositivo aplicadas",
		zap.String("platform_type", config.PlatformType),
		zap.String("os_name", config.OSName),
	)

	return nil
}

func (m *Manager) applyDevicePropsForRegistration(ctx context.Context, config model.DeviceConfig) {
	deviceConfigMu.Lock()
	defer deviceConfigMu.Unlock()

	originalPlatformType := store.DeviceProps.PlatformType
	originalOS := store.DeviceProps.Os
	originalVersionPrimary := store.DeviceProps.Version.Primary
	originalVersionSecondary := store.DeviceProps.Version.Secondary
	originalVersionTertiary := store.DeviceProps.Version.Tertiary

	store.SetOSInfo(config.OSName, [3]uint32{1, 0, 0})

	store.DeviceProps.PlatformType = mapPlatformType(config.PlatformType)

	m.log.Debug("configurações globais aplicadas para registro",
		zap.String("platform_type", config.PlatformType),
		zap.String("os_name", config.OSName),
	)

	go func() {
		time.Sleep(10 * time.Second)

		deviceConfigMu.Lock()
		defer deviceConfigMu.Unlock()

		store.DeviceProps.PlatformType = originalPlatformType
		store.DeviceProps.Os = originalOS
		if store.DeviceProps.Version != nil {
			store.DeviceProps.Version.Primary = originalVersionPrimary
			store.DeviceProps.Version.Secondary = originalVersionSecondary
			store.DeviceProps.Version.Tertiary = originalVersionTertiary
		}

		m.log.Debug("valores globais do WhatsMeow restaurados após registro")
	}()
}

// handleEvent processa eventos do WhatsMeow.
func (m *Manager) handleEvent(instanceID string, evt any) {
	m.mu.RLock()
	callback := m.onStatusChange
	handler := m.eventHandler
	m.mu.RUnlock()

	// Enviar evento para o webhook handler se configurado
	if handler != nil {
		// Obter JID da instância conectada
		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()

		instanceJID := ""
		if exists && client != nil && client.Store != nil && client.Store.ID != nil {
			instanceJID = client.Store.ID.String()
		}

		// Enviar apenas eventos relevantes para webhooks (mensagens, recibos, presença)
		switch evt.(type) {
		case *events.Message, *events.Receipt, *events.Presence, *events.Connected, *events.Disconnected:
			go handler.Handle(context.Background(), instanceID, instanceJID, client, evt)
		}
	}

	switch v := evt.(type) {
	case *events.Connected:
		m.log.Info("instância conectada - dispositivo liberado, sincronizando dados essenciais em background",
			zap.String("instance_id", instanceID))

		// Enviar presence em background
		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()
		if exists && client != nil {
			go func() {
				for attempt := 1; attempt <= 5; attempt++ {
					time.Sleep(time.Duration(attempt*2) * time.Second)
					if !client.IsLoggedIn() {
						return
					}
					if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
						if attempt == 5 {
							m.log.Debug("PushName não sincronizado no Connected", zap.String("instance_id", instanceID))
						}
					} else {
						m.log.Info("presence enviado - instância totalmente ativa", zap.String("instance_id", instanceID))
						return
					}
				}
			}()
		}

		if callback != nil {
			callback(instanceID, "active")
		}
	case *events.PairSuccess:
		m.log.Info("pareamento concluído",
			zap.String("instance_id", instanceID),
			zap.String("user_jid", v.ID.String()),
		)
		if callback != nil {
			callback(instanceID, "active")
		}
	case *events.Disconnected:
		m.log.Warn("instância desconectada",
			zap.String("instance_id", instanceID),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.LoggedOut:
		m.log.Warn("instância deslogada",
			zap.String("instance_id", instanceID),
			zap.String("reason", v.Reason.String()),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusDisconnected)
		if callback != nil {
			callback(instanceID, "disconnected")
		}
	case *events.TemporaryBan:
		m.log.Error("instância temporariamente banida pelo WhatsApp",
			zap.String("instance_id", instanceID),
			zap.String("code", v.Code.String()),
			zap.Duration("expire", v.Expire),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.ConnectFailure:
		m.log.Error("falha ao conectar instância",
			zap.String("instance_id", instanceID),
			zap.String("reason", v.Reason.String()),
			zap.String("message", v.Message),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.StreamError:
		m.log.Error("erro de stream na instância",
			zap.String("instance_id", instanceID),
			zap.String("code", v.Code),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.PairError:
		m.log.Error("erro no pareamento",
			zap.String("instance_id", instanceID),
			zap.Error(v.Error),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.ClientOutdated:
		m.log.Error("cliente WhatsMeow desatualizado",
			zap.String("instance_id", instanceID),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.HistorySync:
		m.log.Info("history sync recebido",
			zap.String("instance_id", instanceID),
			zap.Int("conversations", len(v.Data.GetConversations())),
		)
	case *events.AppStateSyncComplete:
		m.log.Debug("app state sync completo",
			zap.String("instance_id", instanceID),
			zap.String("name", string(v.Name)),
		)

		// Quando critical_block sync completa, a instância está pronta para uso
		if v.Name == "critical_block" {
			m.log.Info("sincronização crítica concluída - instância pronta para receber mensagens",
				zap.String("instance_id", instanceID))
		}
	default:
		// Outros eventos não tratados explicitamente
		m.log.Debug("evento recebido",
			zap.String("instance_id", instanceID),
			zap.String("event_type", fmt.Sprintf("%T", evt)),
		)
	}
}

// updateInstanceStatus atualiza o status da instância no banco de dados
func (m *Manager) updateInstanceStatus(instanceID string, status model.InstanceStatus) {
	if m.instanceRepo == nil {
		return
	}

	ctx := context.Background()
	inst, err := m.instanceRepo.GetByID(ctx, instanceID)
	if err != nil {
		m.log.Warn("erro ao buscar instância para atualizar status",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return
	}

	inst.Status = status
	if _, err := m.instanceRepo.Update(ctx, inst); err != nil {
		m.log.Warn("erro ao atualizar status da instância no banco",
			zap.String("instance_id", instanceID),
			zap.String("status", string(status)),
			zap.Error(err),
		)
	} else {
		m.log.Info("status da instância atualizado no banco",
			zap.String("instance_id", instanceID),
			zap.String("status", string(status)),
		)
	}
}
