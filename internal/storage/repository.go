package storage

import (
	"context"
	"errors"
	"time"

	"github.com/open-apime/apime/internal/storage/model"
)

var ErrNotFound = errors.New("not found")

type InstanceRepository interface {
	Create(ctx context.Context, instance model.Instance) (model.Instance, error)
	GetByID(ctx context.Context, id string) (model.Instance, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (model.Instance, error)
	List(ctx context.Context) ([]model.Instance, error)
	ListByOwner(ctx context.Context, ownerUserID string) ([]model.Instance, error)
	Update(ctx context.Context, instance model.Instance) (model.Instance, error)
	Delete(ctx context.Context, id string) error
}

type MessageRepository interface {
	Create(ctx context.Context, message model.Message) (model.Message, error)
	ListByInstance(ctx context.Context, instanceID string) ([]model.Message, error)
	Update(ctx context.Context, msg model.Message) error
	UpdateStatusByWhatsAppID(ctx context.Context, whatsappID string, status string) error
	GetByWhatsAppID(ctx context.Context, whatsappID string) (model.Message, error)
	DeleteByInstanceID(ctx context.Context, instanceID string) error
}

type UserRepository interface {
	Create(ctx context.Context, user model.User) (model.User, error)
	GetByID(ctx context.Context, id string) (model.User, error)
	GetByEmail(ctx context.Context, email string) (model.User, error)
	List(ctx context.Context) ([]model.User, error)
	UpdatePassword(ctx context.Context, id, passwordHash string) error
	Delete(ctx context.Context, id string) error
}

type EventLogRepository interface {
	Create(ctx context.Context, eventLog model.EventLog) (model.EventLog, error)
	ListByInstance(ctx context.Context, instanceID string) ([]model.EventLog, error)
	DeleteByInstanceID(ctx context.Context, instanceID string) error
}

type APITokenRepository interface {
	Create(ctx context.Context, token model.APIToken) (model.APIToken, error)
	GetByID(ctx context.Context, id string) (model.APIToken, error)
	GetByTokenHash(ctx context.Context, tokenHash string) (model.APIToken, error)
	ListByUser(ctx context.Context, userID string) ([]model.APIToken, error)
	Update(ctx context.Context, token model.APIToken) (model.APIToken, error)
	Delete(ctx context.Context, id string) error
}

type DeviceConfigRepository interface {
	Get(ctx context.Context) (model.DeviceConfig, error)
	Update(ctx context.Context, config model.DeviceConfig) (model.DeviceConfig, error)
}

type HistorySyncRepository interface {
	Create(ctx context.Context, payload model.WhatsappHistorySync) (model.WhatsappHistorySync, error)
	ListPendingByInstance(ctx context.Context, instanceID string) ([]model.WhatsappHistorySync, error)
	ListPendingByCycle(ctx context.Context, instanceID, cycleID string) ([]model.WhatsappHistorySync, error)
	UpdateStatus(ctx context.Context, id string, status model.HistorySyncPayloadStatus, processedAt *time.Time) error
	DeleteByInstance(ctx context.Context, instanceID string) error
}

type ContactRepository interface {
	Upsert(ctx context.Context, contact model.Contact) error
	GetByPhone(ctx context.Context, phone string) (model.Contact, error)
}
