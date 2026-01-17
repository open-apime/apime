package instance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

var ErrInvalidName = errors.New("nome da instância inválido")

type Service struct {
	repo         storage.InstanceRepository
	messageRepo  storage.MessageRepository
	eventLogRepo storage.EventLogRepository
	session      SessionManager
}

type SessionManager interface {
	CreateSession(ctx context.Context, instanceID string) (string, error)
	GetQR(ctx context.Context, instanceID string) (string, error)
	RestoreSession(ctx context.Context, instanceID string, encryptedBlob []byte) error
	Disconnect(instanceID string) error
	DeleteSession(instanceID string) error
	SaveSessionBlob(instanceID string) ([]byte, error)
}

func NewService(repo storage.InstanceRepository) *Service {
	return &Service{repo: repo}
}

func NewServiceWithSession(repo storage.InstanceRepository, session SessionManager) *Service {
	return &Service{repo: repo, session: session}
}

func NewServiceWithSessionAndMessages(repo storage.InstanceRepository, messageRepo storage.MessageRepository, session SessionManager) *Service {
	return &Service{repo: repo, messageRepo: messageRepo, session: session}
}

func NewServiceWithSessionMessagesAndEventLogs(repo storage.InstanceRepository, messageRepo storage.MessageRepository, eventLogRepo storage.EventLogRepository, session SessionManager) *Service {
	return &Service{repo: repo, messageRepo: messageRepo, eventLogRepo: eventLogRepo, session: session}
}

type CreateInput struct {
	Name          string
	WebhookURL    string
	WebhookSecret string
	OwnerUserID   string
}

type UpdateInput struct {
	Name          string
	WebhookURL    string
	WebhookSecret string
	OwnerUserID   string
}

func (s *Service) Create(ctx context.Context, input CreateInput) (model.Instance, error) {
	if strings.TrimSpace(input.Name) == "" {
		return model.Instance{}, ErrInvalidName
	}
	if strings.TrimSpace(input.WebhookURL) != "" && !strings.HasPrefix(strings.TrimSpace(input.WebhookURL), "http") {
		return model.Instance{}, errors.New("webhook inválido")
	}

	plainToken := uuid.NewString()
	hashBytes := sha256.Sum256([]byte(plainToken))
	hash := hex.EncodeToString(hashBytes[:])
	now := time.Now().UTC()

	instance := model.Instance{
		ID:             uuid.NewString(),
		Name:           input.Name,
		OwnerUserID:    input.OwnerUserID,
		WebhookURL:     strings.TrimSpace(input.WebhookURL),
		WebhookSecret:  strings.TrimSpace(input.WebhookSecret),
		TokenHash:      hash,
		TokenUpdatedAt: &now,
		Status:         model.InstanceStatusPending,
	}
	created, err := s.repo.Create(ctx, instance)
	if err != nil {
		return model.Instance{}, err
	}

	return created, nil
}

func (s *Service) List(ctx context.Context) ([]model.Instance, error) {
	return s.repo.List(ctx)
}

func (s *Service) ListByUser(ctx context.Context, userID string, userRole string) ([]model.Instance, error) {
	if userRole == "admin" {
		return s.repo.List(ctx)
	}
	return s.repo.ListByOwner(ctx, userID)
}

func (s *Service) Get(ctx context.Context, id string) (model.Instance, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) GetByUser(ctx context.Context, id string, userID string, userRole string) (model.Instance, error) {
	instance, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return model.Instance{}, err
	}

	if userRole != "admin" && instance.OwnerUserID != userID {
		return model.Instance{}, storage.ErrNotFound
	}

	return instance, nil
}

func (s *Service) Update(ctx context.Context, id string, input UpdateInput) (model.Instance, error) {
	inst, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return model.Instance{}, err
	}
	if strings.TrimSpace(input.Name) == "" {
		return model.Instance{}, ErrInvalidName
	}
	if strings.TrimSpace(input.WebhookURL) != "" && !strings.HasPrefix(strings.TrimSpace(input.WebhookURL), "http") {
		return model.Instance{}, errors.New("webhook inválido")
	}
	inst.Name = strings.TrimSpace(input.Name)
	inst.WebhookURL = strings.TrimSpace(input.WebhookURL)
	inst.WebhookSecret = strings.TrimSpace(input.WebhookSecret)
	return s.repo.Update(ctx, inst)
}

func (s *Service) UpdateByUser(ctx context.Context, id string, input UpdateInput) (model.Instance, error) {
	inst, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return model.Instance{}, err
	}

	if input.OwnerUserID != "admin" && inst.OwnerUserID != input.OwnerUserID {
		return model.Instance{}, storage.ErrNotFound
	}

	if strings.TrimSpace(input.Name) == "" {
		return model.Instance{}, ErrInvalidName
	}
	if strings.TrimSpace(input.WebhookURL) != "" && !strings.HasPrefix(strings.TrimSpace(input.WebhookURL), "http") {
		return model.Instance{}, errors.New("webhook inválido")
	}
	inst.Name = strings.TrimSpace(input.Name)
	inst.WebhookURL = strings.TrimSpace(input.WebhookURL)
	inst.WebhookSecret = strings.TrimSpace(input.WebhookSecret)
	return s.repo.Update(ctx, inst)
}

func (s *Service) UpdateStatus(ctx context.Context, id string, status model.InstanceStatus) (model.Instance, error) {
	instance, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return model.Instance{}, err
	}
	instance.Status = status
	return s.repo.Update(ctx, instance)
}

func (s *Service) GetQR(ctx context.Context, id string) (string, error) {
	if s.session == nil {
		return "", errors.New("session manager não configurado")
	}

	_, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}

	return s.session.GetQR(ctx, id)
}

func (s *Service) GetQRByUser(ctx context.Context, id string, userID string, userRole string) (string, error) {
	_, err := s.GetByUser(ctx, id, userID, userRole)
	if err != nil {
		return "", err
	}
	return s.GetQR(ctx, id)
}

func (s *Service) Disconnect(ctx context.Context, id string) error {
	if s.session == nil {
		return errors.New("session manager não configurado")
	}
	if err := s.session.Disconnect(id); err != nil {
		return err
	}
	_, err := s.UpdateStatus(ctx, id, model.InstanceStatusDisconnected)
	return err
}

func (s *Service) DisconnectByUser(ctx context.Context, id string, userID string, userRole string) error {
	_, err := s.GetByUser(ctx, id, userID, userRole)
	if err != nil {
		return err
	}
	return s.Disconnect(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	_, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	// Deletar sessão completamente (incluindo arquivo SQLite) se existir
	if s.session != nil {
		_ = s.session.DeleteSession(id)
	}

	if s.messageRepo != nil {
		if err := s.messageRepo.DeleteByInstanceID(ctx, id); err != nil {
			return err
		}
	}

	if s.eventLogRepo != nil {
		if err := s.eventLogRepo.DeleteByInstanceID(ctx, id); err != nil {
			return err
		}
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) DeleteByUser(ctx context.Context, id string, userID string, userRole string) error {
	inst, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if userRole != "admin" && inst.OwnerUserID != userID {
		return storage.ErrNotFound
	}

	if s.session != nil {
		_ = s.session.DeleteSession(id)
	}

	if s.messageRepo != nil {
		if err := s.messageRepo.DeleteByInstanceID(ctx, id); err != nil {
			return err
		}
	}

	if s.eventLogRepo != nil {
		if err := s.eventLogRepo.DeleteByInstanceID(ctx, id); err != nil {
			return err
		}
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) RotateToken(ctx context.Context, id string) (string, error) {
	inst, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}
	return s.rotateToken(ctx, inst)
}

func (s *Service) RotateTokenByUser(ctx context.Context, id string, userID string, userRole string) (string, error) {
	inst, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}

	if userRole != "admin" && inst.OwnerUserID != userID {
		return "", storage.ErrNotFound
	}

	return s.rotateToken(ctx, inst)
}

func (s *Service) rotateToken(ctx context.Context, inst model.Instance) (string, error) {
	plain := uuid.NewString()
	hashBytes := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(hashBytes[:])
	now := time.Now().UTC()
	inst.TokenHash = hash
	inst.TokenUpdatedAt = &now
	if _, err := s.repo.Update(ctx, inst); err != nil {
		return "", err
	}
	return plain, nil
}

