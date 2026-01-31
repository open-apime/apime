package whatsmeow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waCompanionReg "go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/open-apime/apime/internal/pkg/crypto"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

type zapLogger struct {
	log    *zap.Logger
	module string
}

func (z *zapLogger) Debugf(msg string, args ...interface{}) {
	z.log.Debug(fmt.Sprintf(msg, args...), zap.String("module", z.module))
}
func (z *zapLogger) Infof(msg string, args ...interface{}) {
	z.log.Info(fmt.Sprintf(msg, args...), zap.String("module", z.module))
}
func (z *zapLogger) Warnf(msg string, args ...interface{}) {
	z.log.Warn(fmt.Sprintf(msg, args...), zap.String("module", z.module))
}
func (z *zapLogger) Errorf(msg string, args ...interface{}) {
	z.log.Error(fmt.Sprintf(msg, args...), zap.String("module", z.module))
}
func (z *zapLogger) Sub(module string) waLog.Logger {
	return &zapLogger{log: z.log, module: module}
}

var (
	deviceConfigMu sync.Mutex
)

type EventHandler interface {
	Handle(ctx context.Context, instanceID string, instanceJID string, client *whatsmeow.Client, evt any)
}

type Manager struct {
	clients            map[string]*whatsmeow.Client
	currentQRs         map[string]string
	qrContexts         map[string]context.CancelFunc
	pairingSuccess     map[string]time.Time
	sessionReady       map[string]bool
	mu                 sync.RWMutex
	log                *zap.Logger
	encKey             string
	storageDriver      string
	baseDir            string
	pgConnString       string
	deviceConfigRepo   storage.DeviceConfigRepository
	instanceRepo       storage.InstanceRepository
	historySyncRepo    storage.HistorySyncRepository
	onStatusChange     func(instanceID string, status string)
	eventHandler       EventHandler
	syncWorkers        map[string]context.CancelFunc
	disconnectDebounce map[string]*time.Timer
	expectedDisconnect map[string]bool
	connectedAt        map[string]time.Time
	messageRepo        storage.MessageRepository
	sharedContainer    *sqlstore.Container
}

func NewManager(log *zap.Logger, encKey, storageDriver, baseDir, pgConnString string, deviceConfigRepo storage.DeviceConfigRepository, instanceRepo storage.InstanceRepository, historySyncRepo storage.HistorySyncRepository, messageRepo storage.MessageRepository) *Manager {
	var sharedContainer *sqlstore.Container

	if storageDriver != "postgres" {
		if baseDir == "" {
			baseDir = "/app/data/sessions"
			log.Warn("sessionDir não definido, usando diretório padrão do container", zap.String("dir", baseDir))
		} else {
			log.Info("Usando diretório de sessões configurado", zap.String("dir", baseDir))
		}
		os.MkdirAll(baseDir, 0755)
	} else if pgConnString != "" {
		clientLog := &zapLogger{log: log, module: "whatsmeow-sqlstore"}
		container, err := sqlstore.New(context.Background(), "postgres", pgConnString, clientLog)
		if err != nil {
			log.Error("CRÍTICO: erro ao inicializar container PostgreSQL compartilhado", zap.Error(err))
		} else {
			sharedContainer = container
			log.Info("Container PostgreSQL compartilhado inicializado com sucesso")
		}
	}

	return &Manager{
		clients:            make(map[string]*whatsmeow.Client),
		currentQRs:         make(map[string]string),
		qrContexts:         make(map[string]context.CancelFunc),
		pairingSuccess:     make(map[string]time.Time),
		sessionReady:       make(map[string]bool),
		log:                log,
		encKey:             encKey,
		storageDriver:      storageDriver,
		baseDir:            baseDir,
		pgConnString:       pgConnString,
		deviceConfigRepo:   deviceConfigRepo,
		instanceRepo:       instanceRepo,
		historySyncRepo:    historySyncRepo,
		syncWorkers:        make(map[string]context.CancelFunc),
		disconnectDebounce: make(map[string]*time.Timer),
		expectedDisconnect: make(map[string]bool),
		connectedAt:        make(map[string]time.Time),
		messageRepo:        messageRepo,
		sharedContainer:    sharedContainer,
	}
}

func (m *Manager) SetStatusChangeCallback(fn func(instanceID string, status string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStatusChange = fn
}

func (m *Manager) SetEventHandler(handler EventHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventHandler = handler
	m.log.Info("event handler configurado para webhooks")
}

type SessionStorageInfo struct {
	Type     string
	Location string
}

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

func (m *Manager) IsSessionReady(instanceID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessionReady[instanceID]
}

func (m *Manager) GetConnectedAt(instanceID string) time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connectedAt[instanceID]
}

func (m *Manager) GetPreKeyCount(instanceID string) (int, error) {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return 0, fmt.Errorf("instância não encontrada em memória")
	}

	if client.Store == nil || client.Store.PreKeys == nil {
		return 0, fmt.Errorf("prekey store não disponível")
	}

	return client.Store.PreKeys.UploadedPreKeyCount(context.Background())
}

func (m *Manager) HasSession(instanceID string, jid types.JID) (bool, error) {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return false, fmt.Errorf("instância não encontrada em memória")
	}

	if client.Store == nil || client.Store.Sessions == nil {
		return false, fmt.Errorf("sessão store não disponível")
	}

	return client.Store.Sessions.HasSession(context.Background(), jid.SignalAddress().String())
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

	clientLog := &zapLogger{log: m.log, module: "whatsmeow"}
	var container *sqlstore.Container
	var deviceStore *store.Device
	var err error

	if m.storageDriver == "postgres" && m.pgConnString != "" {
		m.log.Debug("usando store PostgreSQL compartilhado", zap.String("instance_id", instanceID))

		if m.sharedContainer == nil {
			m.log.Error("container PostgreSQL compartilhado não foi inicializado", zap.String("instance_id", instanceID))
			return "", fmt.Errorf("whatsmeow: store PostgreSQL não disponível")
		}

		container = m.sharedContainer
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

	// Configurar callback para recuperar mensagens para retry via banco de dados
	client.GetMessageForRetry = m.getMessageForRetryCallback(instanceID)

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
				if m.instanceRepo != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					inst, err := m.instanceRepo.GetByID(ctx, instanceID)
					if err != nil {
						m.log.Error("erro ao buscar instância para salvar JID",
							zap.String("instance_id", instanceID),
							zap.Error(err),
						)
						cancel()
					} else {
						inst.WhatsAppJID = client.Store.ID.String()
						_, err = m.instanceRepo.Update(ctx, inst)
						cancel()
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
				}
			}

			go m.initHistorySyncCycle(instanceID)
			if client != nil {

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

	if err := m.applyDeviceConfig(ctx, deviceStore); err != nil {
		m.log.Warn("erro ao aplicar configurações de dispositivo", zap.String("instance_id", instanceID), zap.Error(err))

	}

	client := whatsmeow.NewClient(deviceStore, clientLog)

	// Configurar callback para recuperar mensagens para retry via banco de dados
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
	m.log.Info("iniciando restauração de todas as sessões",
		zap.Int("total_instances", len(instanceIDs)),
		zap.String("storage", m.storageDriver),
	)

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

		go func(id string) {
			client, err := m.restoreSessionIfExists(ctx, id)
			if err != nil {
				m.log.Debug("não foi possível restaurar sessão na inicialização",
					zap.String("instance_id", id),
					zap.Error(err),
				)

				return
			}

			if client != nil && client.IsLoggedIn() {
				m.log.Info("sessão restaurada com sucesso na inicialização",
					zap.String("instance_id", id),
				)

			}
		}(instanceID)
	}

	m.log.Info("restauração de sessões iniciada em background")
}

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

	storageInfo := m.GetSessionStorageInfo(instanceID)
	diag.StorageType = storageInfo.Type
	diag.StorageLocation = storageInfo.Location

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

	if m.storageDriver == "postgres" && m.pgConnString != "" {

		if inst != nil && inst.WhatsAppJID != "" {
			diag.DeviceJID = inst.WhatsAppJID
			diag.HasDeviceStore = true

			if m.sharedContainer != nil {
				jid, err := types.ParseJID(inst.WhatsAppJID)
				if err == nil {
					deviceStore, err := m.sharedContainer.GetDevice(context.Background(), jid)
					if err == nil && deviceStore != nil {
						diag.DevicePushName = deviceStore.PushName
					}
				}
			}
		}
	} else {
		dbPath := filepath.Join(m.baseDir, instanceID+".db")
		if fileInfo, err := os.Stat(dbPath); err == nil {
			diag.HasSQLiteFile = true
			diag.SQLiteFilePath = dbPath
			diag.SQLiteFileSize = fileInfo.Size()
			diag.SQLiteFileModTime = fileInfo.ModTime()

			ctx := context.Background()
			sqlitePath := fmt.Sprintf("file:%s?_foreign_keys=on", dbPath)
			container, err := sqlstore.New(ctx, "sqlite3", sqlitePath, &zapLogger{log: m.log, module: "whatsmeow"})
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

func (m *Manager) handleEvent(instanceID string, evt any) {
	m.mu.RLock()
	callback := m.onStatusChange
	handler := m.eventHandler
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	instanceJID := ""
	if exists && client != nil && client.Store != nil && client.Store.ID != nil {
		instanceJID = client.Store.ID.String()
	}

	if handler != nil {

		switch evt.(type) {
		case *events.Message, *events.Receipt, *events.Presence:
			go handler.Handle(context.Background(), instanceID, instanceJID, client, evt)
		}
	}

	if receipt, ok := evt.(*events.Receipt); ok {
		m.log.Debug("receipt recebido",
			zap.String("instance_id", instanceID),
			zap.String("type", string(receipt.Type)),
			zap.Int("message_count", len(receipt.MessageIDs)))

		if receipt.Type == types.ReceiptTypeRetry {
			m.log.Warn("RECEBIDO RETRY RECEIPT - Possível falha de decriptação no destino. Acionando reset proativo.",
				zap.String("instance_id", instanceID),
				zap.Strings("msg_ids", receipt.MessageIDs),
				zap.String("chat", receipt.Chat.String()))

			// Resetar sessão imediatamente para o contato
			go func() {
				_ = m.ResetContactSession(context.Background(), instanceID, receipt.Chat.String())
			}()
		}
	}

	switch v := evt.(type) {
	case *events.Connected:
		m.log.Info("instância conectada - dispositivo liberado, sincronizando dados essenciais em background",
			zap.String("instance_id", instanceID))

		m.mu.Lock()
		if timer, exists := m.disconnectDebounce[instanceID]; exists {
			timer.Stop()
			delete(m.disconnectDebounce, instanceID)
			m.log.Info("reconexão silenciosa detectada (instabilidade de rede recuperada)", zap.String("instance_id", instanceID))
		}
		m.expectedDisconnect[instanceID] = false
		m.connectedAt[instanceID] = time.Now()
		m.mu.Unlock()

		if handler != nil {
			go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
		}

		m.mu.RLock()
		client, exists := m.clients[instanceID]
		m.mu.RUnlock()
		if exists && client != nil {
			go func() {
				for attempt := 1; attempt <= 10; attempt++ {
					time.Sleep(time.Duration(attempt) * time.Second)
					if !client.IsLoggedIn() {
						return
					}
					m.log.Debug("tentando enviar presence de ativação", zap.String("instance_id", instanceID), zap.Int("attempt", attempt))
					if err := client.SendPresence(context.Background(), types.PresenceAvailable); err != nil {
						m.log.Warn("falha ao enviar presence no Connected", zap.String("instance_id", instanceID), zap.Error(err))
						if attempt == 10 {
							m.log.Error("limite de tentativas de presence atingido", zap.String("instance_id", instanceID))
						}
						continue
					}

					m.log.Info("presence enviado com sucesso - aguardando sincronização de app state", zap.String("instance_id", instanceID))

					go func() {
						m.log.Debug("safety timer iniciado: aguardando critical_block", zap.String("instance_id", instanceID))
						time.Sleep(20 * time.Second)

						m.mu.RLock()
						isReady := m.sessionReady[instanceID]
						m.mu.RUnlock()

						if !isReady {
							m.mu.Lock()
							m.sessionReady[instanceID] = true
							m.mu.Unlock()
							m.log.Warn("safety timer trigger: sessão marcada como pronta (critical_block excedeu 20s)",
								zap.String("instance_id", instanceID))
						}
					}()
					return
				}

				time.Sleep(5 * time.Second)
				m.mu.Lock()
				m.sessionReady[instanceID] = true
				m.mu.Unlock()
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
		m.mu.Lock()
		delete(m.connectedAt, instanceID)
		if m.expectedDisconnect[instanceID] {
			m.log.Info("desconexão intencional processada imediatamente", zap.String("instance_id", instanceID))
			m.expectedDisconnect[instanceID] = false
			m.mu.Unlock()

			if handler != nil {
				go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
			}
		} else {
			m.log.Info("instância desconectada, iniciando debounce", zap.String("instance_id", instanceID))
			if t, exists := m.disconnectDebounce[instanceID]; exists {
				t.Stop()
			}

			timer := time.AfterFunc(5*time.Second, func() {
				m.mu.Lock()
				delete(m.disconnectDebounce, instanceID)
				m.mu.Unlock()

				m.log.Warn("debounce expirado, confirmando desconexão", zap.String("instance_id", instanceID))
				m.updateInstanceStatus(instanceID, model.InstanceStatusError)
				if callback != nil {
					callback(instanceID, "error")
				}

				if handler != nil {
					go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
				}
			})
			m.disconnectDebounce[instanceID] = timer
			m.mu.Unlock()
			return
		}

		m.log.Warn("instância desconectada",
			zap.String("instance_id", instanceID),
		)
		m.updateInstanceStatus(instanceID, model.InstanceStatusError)
		if callback != nil {
			callback(instanceID, "error")
		}
	case *events.LoggedOut:
		m.mu.Lock()
		if timer, exists := m.disconnectDebounce[instanceID]; exists {
			timer.Stop()
			delete(m.disconnectDebounce, instanceID)
		}
		m.mu.Unlock()

		m.log.Warn("instância deslogada",
			zap.String("instance_id", instanceID),
			zap.String("reason", v.Reason.String()),
		)

		if handler != nil {
			go handler.Handle(context.Background(), instanceID, instanceJID, client, v)
		}

		m.updateInstanceStatus(instanceID, model.InstanceStatusDisconnected)
		if callback != nil {
			callback(instanceID, "disconnected")
		}

		go func() {
			time.Sleep(1 * time.Second)
			m.DeleteSession(instanceID)
		}()
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
			zap.Uint64("version", v.Version),
		)

		if v.Name == "critical_block" || v.Name == "critical_unblock_low" {
			m.log.Info("sincronização crítica concluída - prekeys E2E prontas",
				zap.String("instance_id", instanceID),
				zap.String("sync_name", string(v.Name)))

			m.mu.Lock()
			m.sessionReady[instanceID] = true
			m.mu.Unlock()
			m.log.Info("sessão pronta para enviar mensagens (app state sync completo)",
				zap.String("instance_id", instanceID))
		}
	case *events.AppStateSyncError:
		m.log.Error("erro no app state sync",
			zap.String("instance_id", instanceID),
			zap.String("name", string(v.Name)),
			zap.Error(v.Error),
		)
	case *events.StreamReplaced:
		m.log.Warn("stream substituído pelo servidor - limpando cache de dispositivos",
			zap.String("instance_id", instanceID))

		if client != nil {

			go func() {
				time.Sleep(2 * time.Second)
				if client.IsLoggedIn() {
					_ = client.SendPresence(context.Background(), types.PresenceAvailable)
					m.log.Debug("presence reenviado após stream replaced", zap.String("instance_id", instanceID))
				}
			}()
		}
	case *events.KeepAliveTimeout:
		m.log.Warn("keepalive timeout detectado - possível reconexão iminente",
			zap.String("instance_id", instanceID),
			zap.Int("error_count", v.ErrorCount))
	case *events.KeepAliveRestored:
		m.log.Info("keepalive restaurado - limpando cache de dispositivos",
			zap.String("instance_id", instanceID))

	default:

		m.log.Debug("evento recebido",
			zap.String("instance_id", instanceID),
			zap.String("event_type", fmt.Sprintf("%T", evt)),
		)
	}
}

func (m *Manager) FetchUserDevices(ctx context.Context, instanceID string, jids []string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return fmt.Errorf("cliente não encontrado")
	}

	parsedJIDs := make([]types.JID, 0, len(jids))
	for _, j := range jids {
		jid, err := types.ParseJID(j)
		if err == nil {
			parsedJIDs = append(parsedJIDs, jid)
		}
	}

	if len(parsedJIDs) == 0 {
		return nil
	}

	devices, err := client.GetUserDevices(ctx, parsedJIDs)
	if err != nil {
		m.log.Error("erro ao buscar dispositivos dos usuários",
			zap.String("instance_id", instanceID),
			zap.Error(err),
		)
		return err
	}

	m.log.Debug("lista de dispositivos atualizada com sucesso",
		zap.String("instance_id", instanceID),
		zap.Int("count", len(devices)),
	)
	return nil
}

func (m *Manager) ClearDeviceCache(instanceID string, jidStr string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil {
		return fmt.Errorf("cliente não encontrado")
	}

	if _, err := types.ParseJID(jidStr); err != nil {
		return err
	}

	m.log.Debug("cache de dispositivos limpo",
		zap.String("instance_id", instanceID),
		zap.String("jid", jidStr),
	)
	return nil
}

func (m *Manager) SendChatPresence(ctx context.Context, instanceID, jidStr string, state types.Presence) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil || !client.IsLoggedIn() {
		return fmt.Errorf("cliente não conectado")
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return err
	}

	return client.SendChatPresence(ctx, jid, types.ChatPresence(state), types.ChatPresenceMediaText)
}

func (m *Manager) ResetContactSession(ctx context.Context, instanceID, jidStr string) error {
	m.mu.RLock()
	client, exists := m.clients[instanceID]
	m.mu.RUnlock()

	if !exists || client == nil || client.Store == nil {
		return fmt.Errorf("store não disponível")
	}

	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return err
	}

	err = client.Store.Sessions.DeleteSession(ctx, jid.User+"@"+jid.Server)
	if err != nil {
		m.log.Error("erro ao resetar sessão do contato",
			zap.String("instance_id", instanceID),
			zap.String("target_jid", jidStr),
			zap.Error(err),
		)
		return err
	}

	m.log.Info("sessão de criptografia resetada para o contato",
		zap.String("instance_id", instanceID),
		zap.String("target_jid", jidStr),
	)
	return nil
}

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

func (m *Manager) ListInstances() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	return ids
}

func (m *Manager) getMessageForRetryCallback(instanceID string) func(types.JID, types.JID, string) *waE2E.Message {
	return func(chat, sender types.JID, id string) *waE2E.Message {
		m.log.Debug("WhatsMeow solicitou mensagem para retry",
			zap.String("instance_id", instanceID),
			zap.String("msg_id", id),
			zap.String("chat", chat.String()))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		msg, err := m.messageRepo.GetByWhatsAppID(ctx, id)
		if err != nil {
			m.log.Debug("mensagem não encontrada para retry no banco",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id),
				zap.Error(err))
			return nil
		}

		waMsg := &waE2E.Message{}
		switch msg.Type {
		case "text":
			waMsg.Conversation = proto.String(msg.Payload)
			m.log.Info("mensagem de TEXTO reconstruída para retry com sucesso",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id))
			return waMsg
		case "image", "video", "audio", "document":
			m.log.Warn("não foi possível reconstruir mensagem de MÍDIA para retry sem Raw protobuf (futura implementação)",
				zap.String("instance_id", instanceID),
				zap.String("msg_id", id),
				zap.String("type", msg.Type))
		default:
			m.log.Warn("tipo de mensagem desconhecido para reconstrução de retry",
				zap.String("type", msg.Type),
				zap.String("msg_id", id))
		}

		return nil
	}
}
