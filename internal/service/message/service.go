package message

import (
	"context"
	"errors"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.uber.org/zap"

	"github.com/open-apime/apime/internal/config"
	"github.com/open-apime/apime/internal/pkg/queue"
	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

var (
	ErrInvalidPayload       = errors.New("payload inválido")
	ErrInstanceNotConnected = errors.New("instância não conectada")
	ErrInvalidJID           = errors.New("JID inválido")
	ErrUnsupportedMediaType = errors.New("tipo de mídia não suportado")
	ErrSessionUnavailable   = errors.New("sessão indisponível")
)

type Service struct {
	repo         storage.MessageRepository
	sessionMgr   SessionManager
	instanceRepo storage.InstanceRepository
	contactRepo  storage.ContactRepository
	eventLogRepo storage.EventLogRepository
	queue        queue.Queue
	webhookQueue queue.Queue
	cfg          config.WhatsAppConfig
	log          *zap.Logger
}

type SessionManager interface {
	GetClient(instanceID string) (*whatsmeow.Client, error)
	IsSessionReady(instanceID string) bool
	HasSession(instanceID string, jid types.JID) (bool, error)
	GetPreKeyCount(instanceID string) (int, error)
	GetConnectedAt(instanceID string) time.Time
	CacheOutgoingMessage(id string, msg *waE2E.Message)
}

func NewService(repo storage.MessageRepository, q queue.Queue, cfg config.WhatsAppConfig, log *zap.Logger) *Service {
	return &Service{
		repo:  repo,
		queue: q,
		cfg:   cfg,
		log:   log,
	}
}

func NewServiceWithSession(repo storage.MessageRepository, sessionMgr SessionManager, instanceRepo storage.InstanceRepository, contactRepo storage.ContactRepository, eventLogRepo storage.EventLogRepository, q queue.Queue, webhookQueue queue.Queue, cfg config.WhatsAppConfig, log *zap.Logger) *Service {
	return &Service{
		repo:         repo,
		sessionMgr:   sessionMgr,
		instanceRepo: instanceRepo,
		contactRepo:  contactRepo,
		eventLogRepo: eventLogRepo,
		queue:        q,
		webhookQueue: webhookQueue,
		cfg:          cfg,
		log:          log,
	}
}

func (s *Service) List(ctx context.Context, instanceID string) ([]model.Message, error) {
	return s.repo.ListByInstance(ctx, instanceID)
}
