package whatsmeow

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	waCompanionReg "go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	"go.uber.org/zap"
)

var (
	deviceConfigMu sync.Mutex
)

type deviceProfile struct {
	PlatformType string
	OSName       string
	Version      [3]uint32
}

var deviceProfiles = []deviceProfile{
	// Chrome on Windows (most common)
	{PlatformType: "CHROME", OSName: "Windows", Version: [3]uint32{10, 0, 0}},
	{PlatformType: "CHROME", OSName: "Windows", Version: [3]uint32{10, 0, 0}},
	// Chrome on macOS
	{PlatformType: "CHROME", OSName: "Mac OS", Version: [3]uint32{15, 3, 0}},
	{PlatformType: "CHROME", OSName: "Mac OS", Version: [3]uint32{14, 7, 0}},
	// Chrome on Linux
	{PlatformType: "CHROME", OSName: "Linux", Version: [3]uint32{6, 8, 0}},
	// Firefox on Windows
	{PlatformType: "FIREFOX", OSName: "Windows", Version: [3]uint32{10, 0, 0}},
	// Firefox on macOS
	{PlatformType: "FIREFOX", OSName: "Mac OS", Version: [3]uint32{15, 3, 0}},
	// Firefox on Linux
	{PlatformType: "FIREFOX", OSName: "Linux", Version: [3]uint32{6, 12, 0}},
	// Safari on macOS
	{PlatformType: "SAFARI", OSName: "Mac OS", Version: [3]uint32{15, 3, 0}},
	// Edge on Windows
	{PlatformType: "EDGE", OSName: "Windows", Version: [3]uint32{10, 0, 0}},
}

func tempBanReasonPT(code events.TempBanReason) string {
	switch code {
	case events.TempBanSentToTooManyPeople:
		return "Enviou mensagens para muitas pessoas que não têm seu número salvo"
	case events.TempBanBlockedByUsers:
		return "Muitas pessoas bloquearam este número"
	case events.TempBanCreatedTooManyGroups:
		return "Criou muitos grupos com pessoas que não têm seu número salvo"
	case events.TempBanSentTooManySameMessage:
		return "Enviou a mesma mensagem para muitas pessoas"
	case events.TempBanBroadcastList:
		return "Enviou muitas mensagens para lista de transmissão"
	default:
		return "Possível violação dos termos de serviço"
	}
}

func getDeviceProfile(instanceID string) deviceProfile {
	h := fnv.New32a()
	h.Write([]byte(instanceID))
	idx := int(h.Sum32()) % len(deviceProfiles)
	return deviceProfiles[idx]
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

func (m *Manager) applyDeviceConfig(instanceID string, deviceStore *store.Device) func() {
	if deviceStore.ID != nil && !deviceStore.ID.IsEmpty() {
		return nil
	}

	profile := getDeviceProfile(instanceID)

	deviceConfigMu.Lock()

	originalPlatformType := store.DeviceProps.PlatformType
	originalOS := store.DeviceProps.Os
	originalVersionPrimary := store.DeviceProps.Version.Primary
	originalVersionSecondary := store.DeviceProps.Version.Secondary
	originalVersionTertiary := store.DeviceProps.Version.Tertiary

	store.SetOSInfo(profile.OSName, profile.Version)
	store.DeviceProps.PlatformType = mapPlatformType(profile.PlatformType)

	deviceConfigMu.Unlock()

	m.log.Info("perfil de dispositivo aplicado para registro",
		zap.String("instance_id", instanceID),
		zap.String("platform_type", profile.PlatformType),
		zap.String("os_name", profile.OSName),
		zap.String("version", fmt.Sprintf("%d.%d.%d", profile.Version[0], profile.Version[1], profile.Version[2])),
	)

	return func() {
		deviceConfigMu.Lock()
		store.DeviceProps.PlatformType = originalPlatformType
		store.DeviceProps.Os = originalOS
		if store.DeviceProps.Version != nil {
			store.DeviceProps.Version.Primary = originalVersionPrimary
			store.DeviceProps.Version.Secondary = originalVersionSecondary
			store.DeviceProps.Version.Tertiary = originalVersionTertiary
		}
		deviceConfigMu.Unlock()

		m.log.Debug("valores globais do WhatsMeow restaurados após registro",
			zap.String("instance_id", instanceID))
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

	pairingCleanup := m.applyDeviceConfig(instanceID, deviceStore)

	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.EnableAutoReconnect = true
	client.ManualHistorySyncDownload = false // downloads history (recent/limited) in the background → delivers NctSalt + tokens (fix 463) and recent messages
	client.AutomaticMessageRerequestFromPhone = true

	client.GetMessageForRetry = m.getMessageForRetryCallback(instanceID)

	client.AddEventHandler(func(evt any) {
		m.handleEvent(instanceID, evt)
	})

	qrCtx, qrCancel := context.WithCancel(context.Background())
	qrChan, err := client.GetQRChannel(qrCtx)
	if err != nil {
		qrCancel()
		if pairingCleanup != nil {
			pairingCleanup()
		}
		m.log.Error("erro ao obter canal QR", zap.String("instance_id", instanceID), zap.Error(err))
		return "", fmt.Errorf("whatsmeow: obter canal QR: %w", err)
	}

	m.log.Debug("conectando cliente WhatsMeow", zap.String("instance_id", instanceID))
	err = client.Connect()
	if err != nil {
		qrCancel()
		if pairingCleanup != nil {
			pairingCleanup()
		}
		m.log.Error("erro ao conectar cliente", zap.String("instance_id", instanceID), zap.Error(err))
		return "", fmt.Errorf("whatsmeow: conectar: %w", err)
	}

	m.mu.Lock()
	m.clients[instanceID] = client
	m.qrContexts[instanceID] = qrCancel
	m.mu.Unlock()

	go m.monitorQRChannel(instanceID, client, qrChan, qrCancel, pairingCleanup)

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

func (m *Manager) monitorQRChannel(instanceID string, client *whatsmeow.Client, qrChan <-chan whatsmeow.QRChannelItem, cancel context.CancelFunc, pairingCleanup func()) {
	pairingSucceeded := false

	defer func() {
		if pairingCleanup != nil {
			pairingCleanup()
		}

		cancel()
		m.mu.Lock()
		delete(m.qrContexts, instanceID)
		delete(m.currentQRs, instanceID)
		m.mu.Unlock()

		if !pairingSucceeded {
			m.log.Warn("canal QR encerrado sem pareamento, limpando estado",
				zap.String("instance_id", instanceID))

			if client != nil && !client.IsLoggedIn() {
				client.Disconnect()
				m.mu.Lock()
				delete(m.clients, instanceID)
				m.mu.Unlock()

				if m.storageDriver != "postgres" {
					dbPath := filepath.Join(m.baseDir, instanceID+".db")
					if _, err := os.Stat(dbPath); err == nil {
						m.log.Info("removendo arquivo SQLite de sessão parcial",
							zap.String("instance_id", instanceID),
							zap.String("db_path", dbPath))
						_ = os.Remove(dbPath)
					}
				}
			}
		} else {
			m.log.Info("canal QR encerrado após pareamento bem-sucedido",
				zap.String("instance_id", instanceID))
		}
	}()

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

		case "timeout":
			m.log.Warn("QR code expirou (timeout do WhatsApp)",
				zap.String("instance_id", instanceID))
			m.mu.Lock()
			delete(m.currentQRs, instanceID)
			m.mu.Unlock()

		case "success":
			m.log.Info("pareamento concluído com sucesso", zap.String("instance_id", instanceID))
			pairingSucceeded = true

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
				_, stillHasQRContext := m.qrContexts[instanceID]
				m.mu.RUnlock()
				if hasQR && currentQR != "" {
					return currentQR, nil
				}
				if !stillHasQRContext {
					m.log.Info("contexto QR expirou durante espera, recriando sessão",
						zap.String("instance_id", instanceID))
					return m.CreateSession(ctx, instanceID)
				}
				// The QR expired (WhatsApp timeout cleared currentQRs) but the channel
				// stays open and silent — recent whatsmeow versions keep the qrChannel
				// alive after the timeout instead of closing it. Without this, GetQR gets
				// stuck returning "wait" forever and the frontend never receives a new QR.
				// We recreate the session to force a new channel.
				m.log.Warn("canal QR vivo mas sem código após timeout, recriando sessão",
					zap.String("instance_id", instanceID))
				client.Disconnect()
				m.mu.Lock()
				delete(m.clients, instanceID)
				delete(m.currentQRs, instanceID)
				delete(m.pairingSuccess, instanceID)
				if cancel, ok := m.qrContexts[instanceID]; ok {
					cancel()
					delete(m.qrContexts, instanceID)
				}
				m.mu.Unlock()
				if m.storageDriver != "postgres" {
					dbPath := filepath.Join(m.baseDir, instanceID+".db")
					if _, err := os.Stat(dbPath); err == nil {
						_ = os.Remove(dbPath)
					}
				}
				return m.CreateSession(ctx, instanceID)
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

	client, err := m.restoreSessionIfExists(ctx, instanceID)
	if err == nil && client != nil && client.IsLoggedIn() {
		return "", fmt.Errorf("instância já conectada, não é necessário QR code")
	}

	if m.storageDriver != "postgres" {
		dbPath := filepath.Join(m.baseDir, instanceID+".db")
		if _, err := os.Stat(dbPath); err == nil {
			m.log.Info("sessão SQLite inválida ou expirada, deletando e criando nova",
				zap.String("instance_id", instanceID),
			)
			_ = os.Remove(dbPath)
		}
	}

	return m.CreateSession(ctx, instanceID)
}
