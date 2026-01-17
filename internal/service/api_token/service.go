package api_token

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/open-apime/apime/internal/storage"
	"github.com/open-apime/apime/internal/storage/model"
)

var (
	ErrTokenNotFound = errors.New("token não encontrado")
	ErrTokenExpired  = errors.New("token expirado")
	ErrTokenInactive = errors.New("token inativo")
)

type Service struct {
	repo storage.APITokenRepository
}

func NewService(repo storage.APITokenRepository) *Service {
	return &Service{repo: repo}
}

func (s *Service) GenerateToken() (plainToken string, hash string) {
	plainToken = "apime_" + uuid.New().String()
	hashBytes := sha256.Sum256([]byte(plainToken))
	hash = hex.EncodeToString(hashBytes[:])
	return plainToken, hash
}

func (s *Service) Create(ctx context.Context, userID, name string, expiresAt *time.Time) (model.APIToken, string, error) {
	plainToken, hash := s.GenerateToken()

	token := model.APIToken{
		ID:        uuid.New().String(),
		Name:      name,
		TokenHash: hash,
		UserID:    userID,
		ExpiresAt: expiresAt,
		IsActive:  true,
	}

	created, err := s.repo.Create(ctx, token)
	if err != nil {
		return model.APIToken{}, "", err
	}

	return created, plainToken, nil
}

func (s *Service) ValidateToken(ctx context.Context, plainToken string) (model.APIToken, error) {
	hashBytes := sha256.Sum256([]byte(plainToken))
	hash := hex.EncodeToString(hashBytes[:])

	token, err := s.repo.GetByTokenHash(ctx, hash)
	if err != nil {
		return model.APIToken{}, ErrTokenNotFound
	}

	if !token.IsActive {
		return model.APIToken{}, ErrTokenInactive
	}

	if token.ExpiresAt != nil && token.ExpiresAt.Before(time.Now()) {
		return model.APIToken{}, ErrTokenExpired
	}

	now := time.Now()
	token.LastUsedAt = &now
	_, err = s.repo.Update(ctx, token)
	if err != nil {
	}

	return token, nil
}

func (s *Service) ListByUser(ctx context.Context, userID string) ([]model.APIToken, error) {
	return s.repo.ListByUser(ctx, userID)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

func (s *Service) Update(ctx context.Context, token model.APIToken) (model.APIToken, error) {
	return s.repo.Update(ctx, token)
}
