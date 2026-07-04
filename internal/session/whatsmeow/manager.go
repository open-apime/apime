package whatsmeow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"

	"github.com/open-apime/apime/internal/pkg/sentryx"
	"github.com/open-apime/apime/internal/storage"
)

const sentryLevelError = sentry.LevelError

// Normal WhatsApp websocket reconnects (auto-recovered by whatsmeow).
// The library logs these as Error, but they are expected and not incidents —
// we downgrade them to Debug and skip Sentry reporting to avoid noise.
var expectedReconnectNoise = []string{
	"failed to read frame header",
	"error reading from websocket",
	"websocket not connected",
	"autoreconnect",
	"keepalive timeout",
}

func isExpectedReconnectNoise(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range expectedReconnectNoise {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

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
	formatted := fmt.Sprintf(msg, args...)
	// Expected reconnect noise: downgrade to Debug and skip Sentry reporting.
	if isExpectedReconnectNoise(formatted) {
		z.log.Debug(formatted, zap.String("module", z.module))
		return
	}
	z.log.Error(formatted, zap.String("module", z.module))
	sentryx.CaptureMessage(formatted, sentryLevelError, map[string]string{
		"source": "whatsmeow",
		"module": z.module,
	})
}
func (z *zapLogger) Sub(module string) waLog.Logger {
	return &zapLogger{log: z.log, module: module}
}

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
	instanceRepo       storage.InstanceRepository
	historySyncRepo    storage.HistorySyncRepository
	eventLogRepo       storage.EventLogRepository
	onStatusChange     func(instanceID string, status string)
	eventHandler       EventHandler
	syncWorkers        map[string]context.CancelFunc
	disconnectDebounce map[string]*time.Timer
	expectedDisconnect map[string]bool
	connectedAt        map[string]time.Time
	messageRepo        storage.MessageRepository
	sharedContainer    *sqlstore.Container
	outgoingMsgCache   sync.Map // msgID -> outgoingMsgEntry, for retry of any type (including media)
}

func NewManager(log *zap.Logger, encKey, storageDriver, baseDir, pgConnString string, instanceRepo storage.InstanceRepository, historySyncRepo storage.HistorySyncRepository, messageRepo storage.MessageRepository, eventLogRepo storage.EventLogRepository) *Manager {
	// Limit history sync to RECENT data (~3 days). Without a limit (the default), the phone
	// tries to prepare the ENTIRE history at pairing time, hanging the QR "forever". Requesting
	// only recent data keeps the sync small and fast (releases the QR immediately) and runs in
	// the background — while still delivering the NctSalt + privacy tokens (needed for the
	// tctoken/cstoken that avoids error 463) and recent messages/contacts to serve via webhook.
	if store.DeviceProps.HistorySyncConfig != nil {
		store.DeviceProps.HistorySyncConfig.FullSyncDaysLimit = proto.Uint32(3)
		store.DeviceProps.HistorySyncConfig.FullSyncSizeMbLimit = proto.Uint32(20)
		store.DeviceProps.HistorySyncConfig.RecentSyncDaysLimit = proto.Uint32(3)
		store.DeviceProps.HistorySyncConfig.InitialSyncMaxMessagesPerChat = proto.Uint32(50)
		store.DeviceProps.HistorySyncConfig.StorageQuotaMb = proto.Uint32(512)
		log.Info("HistorySyncConfig limitado ao recente (~3 dias) — pareamento rápido + sync leve em background")
	}

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
		instanceRepo:       instanceRepo,
		historySyncRepo:    historySyncRepo,
		eventLogRepo:       eventLogRepo,
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

func (m *Manager) ListInstances() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.clients))
	for id := range m.clients {
		ids = append(ids, id)
	}
	return ids
}
