package whatsmeow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"github.com/open-apime/apime/internal/storage/model"
)

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
